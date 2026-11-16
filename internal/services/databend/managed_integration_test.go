package databend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/driverstore"
)

type managedFixture struct {
	localTransportFixture
	Archive, SHA256, Version string
}

func managedNativeFixture(t *testing.T) managedFixture {
	t.Helper()
	if os.Getenv("NYADB_DATABEND_MANAGED_IT") != "1" {
		t.Skip("requires an owned actual Databend and reproducible original managed package")
	}
	var f managedFixture
	raw, err := os.ReadFile(os.Getenv("NYADB_DATABEND_MANAGED_FIXTURE"))
	if err != nil || json.Unmarshal(raw, &f) != nil || !filepath.IsAbs(f.Archive) || len(f.SHA256) != 64 || f.Endpoint == "" {
		t.Fatal("original managed fixture missing", err)
	}
	return f
}

func managedNativeStore(t *testing.T, f managedFixture) *driverstore.Store {
	t.Helper()
	s, err := driverstore.New(filepath.Join(f.Directory, t.Name(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(f.Archive)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Install(context.Background(), input, f.SHA256)
	input.Close()
	if err != nil {
		t.Fatal("original complete managed archive install", err)
	}
	return s
}

func startManagedNative(t *testing.T, f managedFixture, store *driverstore.Store, endpoint string) *ManagedProcess {
	t.Helper()
	config := connections.DatabendManagedConfig{Transport: connections.DatabendConfig{Endpoint: endpoint, Username: f.Username, ConnectTimeoutSeconds: 60}, Database: f.Database, DriverVersion: f.Version, DriverSHA256: f.SHA256, MaxConnections: 3, MetadataReserve: 1}
	p, err := StartManaged(context.Background(), store, config, localTransportSecret(t, f.localTransportFixture), func(pid int) error {
		return recordManagedNativeProcess(t, f, pid)
	})
	if err != nil {
		t.Fatal("original managed process start", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	recordTransportReceipt(t, f.localTransportFixture, "managed-handshake", p.StartupReceipt())
	return p
}

func recordManagedNativeProcess(t *testing.T, f managedFixture, pid int) error {
	t.Helper()
	exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil || !strings.HasPrefix(exe, f.Directory+string(os.PathSeparator)) || !strings.HasSuffix(exe, "/jre/bin/java") {
		return errors.New("original installed Java executable identity differs")
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return err
	}
	fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
	if len(fields) < 20 {
		return errors.New("original process start identity missing")
	}
	journal, err := os.OpenFile(filepath.Join(f.Directory, "processes.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer journal.Close()
	record, _ := json.Marshal(map[string]any{"pid": pid, "start_ticks": fields[19], "executable": exe})
	_, err = journal.Write(append(record, '\n'))
	return err
}

func openManagedNative(t *testing.T, f managedFixture, p *ManagedProcess, metadata bool) *ManagedSession {
	t.Helper()
	s, receipts, err := p.OpenSession(context.Background(), metadata)
	for _, receipt := range receipts {
		recordTransportReceipt(t, f.localTransportFixture, "managed-open-initialize", receipt)
	}
	if err != nil || !s.Alive() {
		t.Fatal("original managed physical session open", err)
	}
	t.Cleanup(func() { _, _ = s.Close() })
	return s
}

func managedNativeCall(t *testing.T, f managedFixture, s *ManagedSession, source string) agentbridge.Receipt {
	t.Helper()
	receipt, err := s.Call(context.Background(), "execute_query", map[string]any{"sql": source, "schema": f.Database, "maxRows": 1000})
	recordTransportReceipt(t, f.localTransportFixture, "managed-query", receipt)
	if err != nil || !receipt.ExecutorAcknowledged || !json.Valid(receipt.OriginalResponse()) {
		t.Fatal("original managed executor acknowledgement missing", err)
	}
	return receipt
}

func managedNativeQuery(t *testing.T, f managedFixture, s *ManagedSession, source string) agentbridge.QuerySnapshot {
	t.Helper()
	q, err := agentbridge.DecodeQuery(managedNativeCall(t, f, s, source), DecodeJDBCValue)
	if err != nil {
		t.Fatal("managed exact native projection", err)
	}
	return q
}

func TestDatabendManagedOriginalInstallPoolMetadataCursorAndReceipts(t *testing.T) {
	f := managedNativeFixture(t)
	store := managedNativeStore(t, f)
	p := startManagedNative(t, f, store, f.Endpoint)
	first, second := openManagedNative(t, f, p, false), openManagedNative(t, f, p, false)
	for _, s := range []*ManagedSession{first, second} {
		q := managedNativeQuery(t, f, s, "SELECT value FROM system.settings WHERE name='timezone'")
		if q.SQL.Sets[0].Rows[0][0].Text != "Asia/Shanghai" {
			t.Fatal("managed initialization changed original non-UTC server defaults")
		}
	}
	managedNativeCall(t, f, first, "SET max_threads=2")
	managedNativeCall(t, f, second, "SET max_threads=3")
	for i, s := range []*ManagedSession{first, second} {
		q := managedNativeQuery(t, f, s, "SELECT value FROM system.settings WHERE name='max_threads'")
		if len(q.SQL.Sets[0].Rows) != 1 || q.SQL.Sets[0].Rows[0][0].Text != fmt.Sprint(i+2) {
			t.Fatal("original physical session settings were shared", i)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if s, receipts, err := p.OpenSession(ctx, false); s != nil || len(receipts) != 0 || !errors.Is(err, context.Canceled) {
		t.Fatal("canceled workload admission dispatched or borrowed another session", err)
	}
	metadata := openManagedNative(t, f, p, true)
	queued, release := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer release()
	if s, receipts, err := p.OpenSession(queued, false); s != nil || len(receipts) != 0 || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("full workload pool dispatched a canceled queued opening", err)
	}
	if q := managedNativeQuery(t, f, metadata, "SELECT version()"); !strings.Contains(q.SQL.Sets[0].Rows[0][0].Text, "1.2.881") {
		t.Fatal("reserved original metadata capacity unavailable")
	}
	q := managedNativeQuery(t, f, first, "SELECT id,exact,payload,absent FROM managed_values")
	row := q.SQL.Sets[0].Rows[0]
	if row[0].Text != "18446744073709551615" || row[1].Text != "9007199254740993.12345678901234567890" || !bytes.Equal(row[2].Bytes, []byte{0, 255, 128}) || row[3].Kind != contract.Null {
		t.Fatal("managed package lost unsigned/decimal/binary/NULL values")
	}
	cursor, page, err := first.OpenQueryCursor(context.Background(), agentbridge.QueryCursorOptions{SQL: "SELECT id,payload FROM managed_pages ORDER BY id", Schema: f.Database, PageSize: 100, MaxRows: 1000})
	if err != nil {
		t.Fatal(err)
	}
	defer cursor.Close()
	var sizes []int
	ordinal := 0
	for {
		recordTransportReceipt(t, f.localTransportFixture, "managed-cursor", page.Receipt)
		sizes = append(sizes, len(page.SQL.Sets[0].Rows))
		for _, row := range page.SQL.Sets[0].Rows {
			if row[0].Text != fmt.Sprint(ordinal) || !bytes.Equal(row[1].Bytes, []byte{0, 255}) {
				t.Fatal("managed original cursor repeated/lost typed data", ordinal)
			}
			ordinal++
		}
		if !page.HasMore {
			break
		}
		page, err = cursor.Fetch(context.Background(), 100)
		if err != nil {
			t.Fatal(err)
		}
	}
	if ordinal != 205 || fmt.Sprint(sizes) != "[100 100 5]" {
		t.Fatal("managed original cursor bounds differ")
	}
	receipt := managedNativeCall(t, f, first, "INSERT INTO managed_writes VALUES (17)")
	if observed, err := DecodeMutationReceipt("insert", receipt); err != nil || observed.State != contract.DataWriteAcknowledged || observed.AffectedRows != 1 {
		t.Fatal("managed native insertion acknowledgement changed", err)
	}
	if rows := localNativeQuery(t, f.localTransportFixture, "SELECT count() FROM \""+f.Database+"\".managed_writes WHERE id=17"); len(rows) != 1 || rows[0][0] != "1" {
		t.Fatal("managed original insertion was not durable exactly once")
	}
	if err := store.Remove(context.Background(), "databend", f.Version); !errors.Is(err, driverstore.ErrBusy) {
		t.Fatal("live original process did not pin its installed package", err)
	}
	if err := p.Close(); err != nil {
		t.Fatal("original managed physical cleanup", err)
	}
	if _, err := first.Call(context.Background(), "execute_query", map[string]any{"sql": "SELECT 1"}); !errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatal("closed original managed session recovered implicitly", err)
	}
	if err := store.Remove(context.Background(), "databend", f.Version); err != nil {
		t.Fatal("original process exit did not release its package", err)
	}
}

func TestDatabendManagedOriginalLateOpenCloseAndExplicitProcessRecovery(t *testing.T) {
	f := managedNativeFixture(t)
	store := managedNativeStore(t, f)
	started, finish := make(chan struct{}), make(chan struct{})
	var finishOnce sync.Once
	releaseLogin := func() { finishOnce.Do(func() { close(finish) }) }
	var blocked atomic.Bool
	product := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/v1/session/login" && blocked.CompareAndSwap(false, true) {
			close(started)
			<-finish
		}
		code, headers, body := forwardFixture(t, f.Endpoint, req)
		fixtureResponse(w, code, headers, body)
	}))
	defer product.Close()
	defer releaseLogin()
	original := startManagedNative(t, f, store, product.URL)
	type outcome struct {
		receipts []agentbridge.Receipt
		err      error
	}
	opened := make(chan outcome, 1)
	go func() {
		s, receipts, err := original.OpenSession(context.Background(), false)
		if s != nil {
			_, _ = s.Close()
		}
		opened <- outcome{receipts, err}
	}()
	select {
	case <-started:
	case <-time.After(15 * time.Second):
		releaseLogin()
		t.Fatal("original managed login was not captured")
	}
	closed := make(chan error, 1)
	go func() { closed <- original.Close() }()
	deadline := time.Now().Add(time.Second)
	for original.Alive() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if original.Alive() || !original.runtime.Client.Alive() {
		t.Fatal("original close did not retain its live captured executor while opening")
	}
	select {
	case <-closed:
		releaseLogin()
		t.Fatal("managed Close released the process before its original opening settled")
	default:
	}
	releaseLogin()
	var result outcome
	select {
	case result = <-opened:
	case <-time.After(30 * time.Second):
		t.Fatal("original opening did not settle after native login was released")
	}
	if result.err == nil || len(result.receipts) == 0 || !result.receipts[0].ExecutorAcknowledged {
		t.Fatal("late managed opening lost its captured executor receipt", result.err)
	}
	for _, receipt := range result.receipts {
		recordTransportReceipt(t, f.localTransportFixture, "managed-late-open", receipt)
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal("late managed opening physical cleanup", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("original close did not settle after opening acknowledgement")
	}
	first := startManagedNative(t, f, store, f.Endpoint)
	firstSession := openManagedNative(t, f, first, false)
	managedNativeCall(t, f, firstSession, "INSERT INTO managed_writes VALUES (29)")
	independent := startManagedNative(t, f, store, f.Endpoint)
	other := openManagedNative(t, f, independent, false)
	process, err := os.FindProcess(first.PID())
	if err != nil || process.Kill() != nil {
		t.Fatal("original process fault could not be injected")
	}
	deadline = time.Now().Add(5 * time.Second)
	for first.Alive() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if first.Alive() {
		t.Fatal("original managed process exit not observed")
	}
	if _, err := firstSession.Call(context.Background(), "execute_query", map[string]any{"sql": "SELECT 111"}); !errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatal("dead original session recovered or replayed", err)
	}
	_ = first.Close()
	if q := managedNativeQuery(t, f, other, "SELECT 747474"); q.SQL.Sets[0].Rows[0][0].Text != "747474" {
		t.Fatal("original process failure retired independent process")
	}
	recovered := startManagedNative(t, f, store, f.Endpoint)
	recovery := openManagedNative(t, f, recovered, false)
	if q := managedNativeQuery(t, f, recovery, "SELECT count() FROM managed_writes WHERE id=29"); q.SQL.Sets[0].Rows[0][0].Text != "1" {
		t.Fatal("explicit new process recovery changed/replayed durable writes")
	}
	if rows := localNativeQuery(t, f.localTransportFixture, "SELECT count() FROM \""+f.Database+"\".managed_writes WHERE id=29"); len(rows) != 1 || rows[0][0] != "1" {
		t.Fatal("explicit managed recovery replayed original durable write")
	}
	if err := independent.Close(); err != nil {
		t.Fatal(err)
	}
	if err := recovered.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(context.Background(), "databend", f.Version); err != nil {
		t.Fatal("original managed leases retained after physical exit", err)
	}
}

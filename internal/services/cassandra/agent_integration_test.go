package cassandra

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/driverstore"
)

type agentFixture struct{ Archive, Binary, SHA256, Version, Endpoint, Keyspace, Directory string }

func agentReceiptEvidence(r agentbridge.Receipt) map[string]any {
	raw := r.OriginalResponse()
	h := sha256.Sum256(raw)
	return map[string]any{"receipt": r, "native_bytes": []byte(raw), "native_sha256": hex.EncodeToString(h[:])}
}

func readAgentFixture(t *testing.T) (agentFixture, Options, AgentConfig) {
	t.Helper()
	local := strings.HasPrefix(t.Name(), "TestCassandraAgentLocal")
	gate := "NYADB_CASSANDRA_AGENT_IT"
	if local {
		gate = "NYADB_CASSANDRA_AGENT_LOCAL_IT"
	}
	if os.Getenv(gate) != "1" {
		t.Skip("requires owned Cassandra and original managed native agent")
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_CASSANDRA_AGENT_FIXTURE"))
	var f agentFixture
	if err != nil || json.Unmarshal(raw, &f) != nil {
		t.Fatal("owned native agent fixture unavailable", err)
	}
	host, port, err := net.SplitHostPort(f.Endpoint)
	if err != nil || !net.ParseIP(host).IsLoopback() || !strings.HasPrefix(filepath.Base(f.Directory), "managed-") {
		t.Fatal("owned original endpoint unavailable")
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	var d connections.ManagedDriver
	expectedExecutable := f.Binary
	if !local {
		store, err := driverstore.New(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		d, err = store.Import(context.Background(), driverstore.Product{Key: "cassandra", Runtime: "native"}, f.Archive)
		if err != nil || d.Version != f.Version || d.SHA256 != f.SHA256 {
			t.Fatal("native original package pin changed", err)
		}
		t.Cleanup(func() {
			if err := store.Remove(context.Background(), d.Key, d.Version); err != nil {
				t.Error("original package remained leased", err)
			}
		})
		expectedExecutable = filepath.Join(d.Directory, "cassandra@"+d.Version, "bin", "agent")
	} else if !filepath.IsAbs(f.Binary) || filepath.Dir(f.Binary) != f.Directory || len(f.SHA256) != 64 {
		t.Fatal("local original binary identity unavailable")
	}
	observe := func(pid int) error {
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return err
		}
		fields := strings.Fields(string(stat)[strings.LastIndexByte(string(stat), ')')+1:])
		exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
		if err != nil || len(fields) < 20 || exe != expectedExecutable {
			return errors.New("original native process identity changed")
		}
		line, _ := json.Marshal(map[string]any{"pid": pid, "start_ticks": fields[19], "executable": exe})
		journal, err := os.OpenFile(filepath.Join(f.Directory, "processes.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = journal.Write(append(line, '\n'))
		closed := journal.Close()
		return errors.Join(err, closed)
	}
	c, err := connections.NormalizeCassandraAgentConfig(AgentConfig{ContactPoints: []string{host}, Port: n, Keyspace: f.Keyspace})
	if err != nil {
		t.Fatal(err)
	}
	return f, Options{Driver: d, Started: observe}, c
}

func openAgent(t *testing.T, f agentFixture, c AgentConfig, o Options) *Process {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var p *Process
	var err error
	if strings.HasPrefix(t.Name(), "TestCassandraAgentLocal") {
		client, startErr := agentbridge.Start(ctx, agentbridge.LaunchSpec{Executable: f.Binary, Started: o.Started,
			Artifacts: []agentbridge.Artifact{{Path: f.Binary, SHA256: f.SHA256}}},
			agentbridge.Options{MaxFrameBytes: 32 << 20, CancelWait: 5 * time.Second, RetainOriginalResponses: true, ErrorCodes: agentbridge.CQLCodes})
		if startErr != nil {
			t.Fatal("local original process start", startErr)
		}
		p, err = openOriginalProcess(ctx, c, "", client, client.Close)
	} else {
		p, err = OpenManaged(ctx, c, "", o)
	}
	if err != nil {
		t.Fatal("original managed process open", err)
	}
	t.Cleanup(func() {
		if err := p.Close(); err != nil {
			t.Error("original process close", err)
		}
	})
	return p
}

func TestCassandraAgentManagedOriginalReceiptsValuesCursorAndClose(t *testing.T) {
	testAgentOriginalReceiptsValuesCursorAndClose(t)
}

func TestCassandraAgentLocalOriginalReceiptsValuesCursorAndClose(t *testing.T) {
	testAgentOriginalReceiptsValuesCursorAndClose(t)
}

func testAgentOriginalReceiptsValuesCursorAndClose(t *testing.T) {
	f, o, c := readAgentFixture(t)
	p := openAgent(t, f, c, o)
	if !json.Valid(p.ReadyResponse()) || len(p.OpeningReceipts()) != 3 {
		t.Fatal("original startup receipts missing")
	}
	for _, r := range p.OpeningReceipts() {
		if !json.Valid(r.OriginalResponse()) || !r.ExecutorAcknowledged {
			t.Fatal("original opening envelope missing")
		}
	}
	opening := p.OpeningReceipts()
	opening[0].Result[0] = '!'
	if !json.Valid(p.OpeningReceipts()[0].Result) {
		t.Fatal("native opening receipt aliased caller")
	}
	q, err := p.Query(context.Background(), c.Keyspace, "SELECT big,exact,binary,huge,absent,pair FROM original_values WHERE id=1", 10, 15)
	if err != nil || len(q.SQL.Sets) != 1 || len(q.SQL.Sets[0].Rows) != 1 {
		t.Fatal("real native query projection", err)
	}
	row := q.SQL.Sets[0].Rows[0]
	if row[0].Kind != contract.Integer || row[0].Text != "9007199254740993" || row[1].Text != "9007199254740993.12345678901234567890" || !bytes.Equal(row[2].Bytes, []byte{0, 255, 128}) || row[3].Text != "1234567890123456789012345678901234567890" || row[4].Kind != contract.Null || row[5].Text != "(7, 'seven')" {
		t.Fatal("original native precision/bytes/NULL/CQL tuple lost")
	}
	if !json.Valid(q.Receipt.OriginalResponse()) || !bytes.Contains(q.Receipt.OriginalResponse(), q.Native) {
		t.Fatal("complete original result envelope lost")
	}
	syntax, err := p.Query(context.Background(), c.Keyspace, "SELECT !!! FROM original_values", 10, 15)
	if err == nil || !syntax.Receipt.ExecutorAcknowledged || syntax.Receipt.RemoteError == nil || syntax.Receipt.RemoteError.NativeCode != "0x2000" || syntax.Receipt.RemoteError.SQLState != "" || !json.Valid(syntax.Receipt.OriginalResponse()) || !p.Alive() {
		t.Fatal("original CQL syntax error lost its native code/receipt or retired a usable session", err)
	}
	metadata, err := p.Metadata(context.Background(), "get_table_ddl", c.Keyspace, "original_values")
	if err != nil || !bytes.Contains(metadata.Result, []byte("PRIMARY KEY")) {
		t.Fatal("qualified native DDL missing", err)
	}
	if r, err := p.Metadata(context.Background(), "execute_transaction", c.Keyspace, ""); err == nil || r.Dispatched {
		t.Fatal("native logged batch masqueraded as metadata/transaction")
	}
	options := agentbridge.QueryCursorOptions{SQL: "SELECT id,payload FROM pages WHERE bucket=1", Schema: c.Keyspace, PageSize: 100, MaxRows: 1000, TimeoutSeconds: 15}
	cursor, page, err := p.OpenCursor(context.Background(), options)
	if err != nil || !page.HasMore || len(page.SQL.Sets[0].Rows) != 100 {
		t.Fatal("original native first page", err)
	}
	t.Cleanup(func() { cursor.Close() })
	pages := []agentbridge.QueryPageSnapshot{page}
	count := len(page.SQL.Sets[0].Rows)
	for cursor.HasMore() {
		page, err = cursor.Fetch(context.Background(), 100)
		if err != nil {
			t.Fatal(err)
		}
		pages = append(pages, page)
		count += len(page.SQL.Sets[0].Rows)
	}
	if count != 205 || len(pages) != 3 || pages[2].HasMore {
		t.Fatal("original native cursor repeated/truncated rows")
	}
	ordinal := 0
	for _, page := range pages {
		for _, row := range page.SQL.Sets[0].Rows {
			if row[0].Kind != contract.Integer || row[0].Text != strconv.Itoa(ordinal) || row[1].Kind != contract.Binary || !bytes.Equal(row[1].Bytes, []byte{0, 255}) {
				t.Fatal("original cursor lost row identity/order/binary payload", ordinal)
			}
			ordinal++
		}
	}
	if _, err := cursor.Fetch(context.Background(), 100); !errors.Is(err, io.EOF) {
		t.Fatal("exhausted cursor admitted original work", err)
	}
	if err := cursor.Close(); err != nil {
		t.Fatal("original exhausted cursor close", err)
	}
	options.MaxRows = 150
	bounded, first, err := p.OpenCursor(context.Background(), options)
	if err != nil || len(first.SQL.Sets[0].Rows) != 100 {
		t.Fatal("bounded original page", err)
	}
	last, err := bounded.Fetch(context.Background(), 100)
	if err != nil || len(last.SQL.Sets[0].Rows) != 50 || last.HasMore || !last.SQL.Truncated {
		t.Fatal("original bounded cursor disposition", err)
	}
	if err := bounded.Close(); err != nil {
		t.Fatal("original bounded cursor close", err)
	}
	early, _, err := p.OpenCursor(context.Background(), options)
	if err != nil {
		t.Fatal("original early-close cursor", err)
	}
	if err := early.Close(); err != nil {
		t.Fatal("original early cursor did not acknowledge close", err)
	}
	if _, err := early.Fetch(context.Background(), 100); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed cursor dispatched another page", err)
	}
	options.SQL = "SELECT id,payload FROM pages WHERE bucket=2"
	empty, emptyPage, err := p.OpenCursor(context.Background(), options)
	if err != nil || len(emptyPage.SQL.Sets) != 1 || len(emptyPage.SQL.Sets[0].Rows) != 0 || emptyPage.HasMore {
		t.Fatal("empty native page lost column metadata or invented rows", err)
	}
	if err := empty.Close(); err != nil {
		t.Fatal("empty original cursor close", err)
	}
	if err := p.Close(); err != nil {
		t.Fatal("original physical process did not close", err)
	}
	select {
	case <-p.Exited():
	default:
		t.Fatal("package released before original process exit")
	}
	if _, err := p.Query(context.Background(), c.Keyspace, "SELECT id FROM pages", 1, 1); !errors.Is(err, agentbridge.ErrClosed) {
		t.Fatal("closed original process reopened/replayed", err)
	}
	openingEvidence := []map[string]any{}
	for _, r := range p.OpeningReceipts() {
		openingEvidence = append(openingEvidence, agentReceiptEvidence(r))
	}
	pageEvidence := []map[string]any{}
	for _, v := range pages {
		pageEvidence = append(pageEvidence, agentReceiptEvidence(v.Receipt))
	}
	record, _ := json.MarshalIndent(map[string]any{"opening": openingEvidence, "query": q, "query_receipt": agentReceiptEvidence(q.Receipt), "syntax_receipt": agentReceiptEvidence(syntax.Receipt), "metadata_receipt": agentReceiptEvidence(metadata), "pages": pages, "page_receipts": pageEvidence, "bounded_terminal": last, "bounded_receipt": agentReceiptEvidence(last.Receipt), "empty_page": emptyPage, "empty_receipt": agentReceiptEvidence(emptyPage.Receipt), "early_close_acknowledged": true}, "", "  ")
	if err := os.WriteFile(filepath.Join(f.Directory, "managed-values-cursors.json"), record, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCassandraAgentManagedDocumentIsolationAndOriginalCancellation(t *testing.T) {
	testAgentDocumentIsolationAndOriginalCancellation(t)
}

func TestCassandraAgentLocalDocumentIsolationAndOriginalCancellation(t *testing.T) {
	testAgentDocumentIsolationAndOriginalCancellation(t)
}

func testAgentDocumentIsolationAndOriginalCancellation(t *testing.T) {
	f, o, c := readAgentFixture(t)
	original := openAgent(t, f, c, o)
	parked := make(chan struct{}, 1)
	proxy := nativeFrameProxy(t, connections.CassandraConfig{Endpoint: f.Endpoint, TLSMode: "disable"}, func(op byte, body []byte, _ frame, front net.Conn) bool {
		if op == 9 && bytes.Contains(body, []byte("/* original-cancel */")) {
			parked <- struct{}{}
			_, _ = io.Copy(io.Discard, front)
			return true
		}
		return false
	})
	host, port, _ := net.SplitHostPort(proxy.Endpoint)
	n, _ := strconv.Atoi(port)
	c.ContactPoints, c.Port = []string{host}, n
	// This fixture's existing fault proxy implements v4 frames only. The
	// independent direct process keeps original automatic negotiation.
	c.ProtocolVersion = 4
	p := openAgent(t, f, c, o)
	if p.PID() == original.PID() || p.Configuration() == original.Configuration() {
		t.Fatal("physical document ownership was not independent")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	type result struct {
		query QueryResult
		err   error
	}
	done := make(chan result, 1)
	go func() {
		q, err := p.Query(ctx, c.Keyspace, "SELECT /* original-cancel */ id FROM pages WHERE bucket=1", 10, 15)
		done <- result{q, err}
	}()
	select {
	case <-parked:
	case <-time.After(10 * time.Second):
		t.Fatal("original executor did not reach real prepared response")
	}
	queued, stop := context.WithTimeout(context.Background(), 5*time.Millisecond)
	q, err := p.Query(queued, c.Keyspace, "SELECT id FROM pages", 1, 1)
	stop()
	if !errors.Is(err, context.DeadlineExceeded) || q.Receipt.Dispatched {
		t.Fatal("queued cancellation dispatched to active original executor", err)
	}
	if _, err := original.Query(context.Background(), f.Keyspace, "SELECT id FROM pages WHERE bucket=1 LIMIT 1", 1, 10); err != nil {
		t.Fatal("independent original process canceled", err)
	}
	cancel()
	select {
	case out := <-done:
		if !errors.Is(out.err, context.Canceled) || !out.query.Receipt.CancellationRequested || !out.query.Receipt.ExecutorAcknowledged || !json.Valid(out.query.Receipt.OriginalResponse()) {
			t.Fatal("cancel acknowledgement replaced original executor receipt", out.err)
		}
		data, _ := json.MarshalIndent(map[string]any{"original_receipt": agentReceiptEvidence(out.query.Receipt), "original_pid": p.PID(), "independent_pid": original.PID()}, "", "  ")
		if err := os.WriteFile(filepath.Join(f.Directory, "managed-cancellation.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("original cancellation did not acknowledge")
	}
	if err := p.Close(); err != nil {
		t.Fatal("original canceled process close", err)
	}
	if _, err := original.Query(context.Background(), f.Keyspace, "SELECT id FROM pages WHERE bucket=1 LIMIT 1", 1, 10); err != nil {
		t.Fatal("original close reached independent document", err)
	}
}

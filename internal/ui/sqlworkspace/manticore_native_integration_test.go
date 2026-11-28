package sqlworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/manticore"
	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func manticoreSQLConfig(t *testing.T) connections.ManticoreConfig {
	t.Helper()
	if os.Getenv("NYADB_MANTICORE_IT") != "1" {
		t.Skip("requires owned real Manticore fixture")
	}
	host, port, e := net.SplitHostPort(os.Getenv("NYADB_MANTICORE_ENDPOINT"))
	if e != nil {
		t.Fatal(e)
	}
	n, e := strconv.Atoi(port)
	if e != nil {
		t.Fatal(e)
	}
	return connections.ManticoreConfig{Host: host, Port: uint16(n), Username: "root", QueryTimeoutSeconds: 20}
}
func manticoreSQLSession(t *testing.T, c connections.ManticoreConfig) *manticore.Session {
	t.Helper()
	s, e := manticore.Open(context.Background(), 7301, c, "")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	if !strings.HasPrefix(s.Version(), os.Getenv("NYADB_MANTICORE_VERSION")+" ") {
		t.Fatal(s.Version())
	}
	return s
}
func manticoreSQLRun(t *testing.T, q contract.QueryLease, source string) contract.SQLResult {
	t.Helper()
	r, e := q.Execute(context.Background(), "Manticore", source)
	if e != nil {
		t.Fatal(source, e)
	}
	return r
}
func manticoreSQLWait(t *testing.T, s *State, done func() bool) {
	t.Helper()
	until := time.Now().Add(15 * time.Second)
	for !done() && time.Now().Before(until) {
		s.ConsumeDocuments(ui.Services{})
		s.Consume()
		time.Sleep(time.Millisecond)
	}
	if !done() {
		t.Fatal("native SQL workspace did not settle")
	}
}
func manticoreSQLState(t *testing.T, native *manticore.Session) (*State, *Document, *queries.Store, *memoryQueries, *memoryWorkspaceKey) {
	t.Helper()
	runtime := EngineRuntime(func(id int64) (contract.Session, error) {
		if id != native.ProfileID() || !native.Alive() {
			return nil, contract.ErrRevoked
		}
		return native, nil
	})
	s := New(Dependencies{Runtime: func() Runtime { return runtime }, Connected: runtime.IsConnected, QueryDialect: func(int64) string { return "mysql" }})
	t.Cleanup(s.CloseAll)
	repo := &memoryQueries{records: map[string]queries.Record{}}
	key := &memoryWorkspaceKey{}
	store := queries.NewStore(repo, key)
	s.SetPersistence(store, Files{}, nil)
	waitHistory(t, s, false)
	d := s.Open(7301, "SELECT 'unsaved original';", native.ProfileID(), "Manticore", false)
	s.Prepare(d)
	manticoreSQLWait(t, s, func() bool { return d.prepareResults == nil })
	if d.Error() != "" {
		t.Fatal(d.Error())
	}
	return s, d, store, repo, key
}
func manticoreSQLEvidence(t *testing.T, name string, v any) {
	t.Helper()
	raw, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	if dir := os.Getenv("NYADB_MANTICORE_EVIDENCE"); dir != "" {
		if e = os.WriteFile(filepath.Join(dir, name), raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
}
func TestManticoreNativeSQLFileHistoryNegativeCountsAndOriginalReceipt(t *testing.T) {
	native := manticoreSQLSession(t, manticoreSQLConfig(t))
	q, e := native.ReserveQuery(99)
	if e != nil {
		t.Fatal(e)
	}
	defer q.Release()
	table := fmt.Sprintf("nyadb_workspace_rt73_%d", time.Now().UnixNano())
	pq := table + "_pq"
	run := func(sql string) contract.SQLResult { return manticoreSQLRun(t, q, sql) }
	run("CREATE TABLE " + table + " (title text,qty integer)")
	defer run("DROP TABLE " + table)
	run("CREATE TABLE " + pq + " (title text) type='pq'")
	defer run("DROP TABLE " + pq)
	run("INSERT INTO " + pq + " (id,query,tags) VALUES (1,'original','before')")
	s, d, store, repo, key := manticoreSQLState(t, native)
	var status string
	s.deps.Status = func(v string) { status = v }
	source := "UPDATE " + pq + " SET tags='ignored' WHERE id=1; INSERT INTO " + table + " (id,qty) VALUES (1,7)"
	s.persistence.files = Files{OpenPath: func() (string, error) { return "original.sql", nil }, OpenReader: func(string) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(source)), nil }}
	s.RunFile(d)
	manticoreSQLWait(t, s, func() bool { return !d.Loading() })
	waitHistory(t, s, true)
	if d.Error() != "" || d.Editor.Buffer.String() != "SELECT 'unsaved original';" || d.Result() == nil || d.Result().AffectedRows != -1 || len(d.Result().ResultSets()) != 2 || !strings.Contains(status, "未报告影响行数") {
		t.Fatal("file result changed editor or reported invented count", d.Error(), status)
	}
	original := d.Result().NativeJSON
	h, e := store.LoadHistory(context.Background(), time.Now())
	if e != nil || len(h.Entries) != 2 {
		t.Fatal(h, e)
	}
	for _, entry := range h.Entries {
		if entry.State != "success" || entry.NativeJSON == "" {
			t.Fatal("completed file step became error", entry)
		}
		if strings.HasPrefix(entry.SQL, "UPDATE") && entry.AffectedRows != -1 {
			t.Fatal("original negative count lost", entry)
		}
	}
	if actual := run("SELECT tags FROM " + pq + " WHERE id=1"); actual.Rows[0][0] != "before" {
		t.Fatal(actual)
	}
	if !s.pinResult(d) {
		t.Fatal("native receipt snapshot rejected", d.Error())
	}
	snapshot := d.snapshots[0].Document
	if snapshot.Result().NativeJSON != original || resultSnapshotSize(snapshot.Result()) < int64(len(original)) {
		t.Fatal("snapshot dropped original receipt or budget")
	}
	locale := "zh-Hans"
	i18n.SetLocaleFunc(func() string { return locale })
	defer i18n.SetLocaleFunc(nil)
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c)
		s.Consume()
		if !d.closed {
			s.View(c, d)
		}
		s.Modals(c)
	}, 1280, 800)
	capture := func(name string) {
		t.Helper()
		tt.Frame()
		if dir := os.Getenv("NYADB_MANTICORE_EVIDENCE"); dir != "" {
			f, e := os.Create(filepath.Join(dir, name+".png"))
			if e != nil {
				t.Fatal(e)
			}
			e = png.Encode(f, tt.Image())
			ce := f.Close()
			if e != nil || ce != nil {
				t.Fatal(e, ce)
			}
		}
	}
	for _, lang := range []string{"zh-Hans", "en"} {
		locale = lang
		tt.Frame()
		capture(lang + "-sql-negative")
		if !tt.HasText(locales.Text("服务器未报告影响行数；请核对预期更改")) || tt.HasText("影响 -1 行") {
			t.Fatal("misleading native acknowledgement", tt.Texts())
		}
		if e := tt.Click(locales.Text("原始执行回执")); e != nil {
			t.Fatal(e)
		}
		capture(lang + "-sql-original-receipt")
		if s.nativeReceipt == nil || s.nativeReceipt.json != original {
			t.Fatal("display rebuilt original receipt")
		}
		if e := tt.Click(locales.Text("关闭")); e != nil {
			t.Fatal(e)
		}
	}
	// Export captures the original receipt even if the modal and result change.
	s.openNativeReceipt("original", original)
	started, release := make(chan struct{}), make(chan struct{})
	path := filepath.Join(t.TempDir(), "original-receipt.json")
	s.persistence.files.SavePath = func(string) (string, error) { close(started); <-release; return path, nil }
	s.persistence.files.WriteStream = func(path string, write func(io.Writer) error) error {
		f, e := os.Create(path)
		if e != nil {
			return e
		}
		e = write(f)
		ce := f.Close()
		if e != nil {
			return e
		}
		return ce
	}
	s.exportNativeReceipt()
	<-started
	fresh := run("SELECT qty FROM " + table + " WHERE id=1")
	d.setResult(&fresh)
	s.openNativeReceipt("later", fresh.NativeJSON)
	close(release)
	waitDocumentIO(t, s)
	exported, e := os.ReadFile(path)
	if e != nil || string(exported) != original || snapshot.Result().NativeJSON != original {
		t.Fatal("late export followed new result", e)
	}
	s.nativeReceipt = nil
	// Real server failure retains prefix and unsent suffix in history and receipt.
	source = "INSERT INTO " + table + " (id,qty) VALUES (2,20); INSERT INTO " + table + " (id,qty) VALUES (2,99); INSERT INTO " + table + " (id,qty) VALUES (3,30)"
	s.RunFile(d)
	manticoreSQLWait(t, s, func() bool { return !d.Loading() })
	waitHistory(t, s, true)
	if d.Error() == "" || d.NeedsRecovery() || d.Result().Steps[2].State != "not_executed" {
		t.Fatal("original failed file outcome", d.Error())
	}
	h, e = store.LoadHistory(context.Background(), time.Now())
	if e != nil || len(h.Entries) != 4 {
		t.Fatal(h, e)
	}
	for _, entry := range h.Entries {
		if strings.Contains(entry.SQL, "(2,20)") && (entry.State != "success" || entry.AffectedRows != 1) {
			t.Fatal(entry)
		}
		if strings.Contains(entry.SQL, "(2,99)") && (entry.State != "error" || entry.NativeJSON == "") {
			t.Fatal(entry)
		}
	}
	manticoreSQLEvidence(t, "native-sqlworkspace-file-history.json", h)
	manticoreSQLEvidence(t, "native-sqlworkspace-failed-file.json", d.Result())
	s.Close(d)
	restored, e := queries.NewStore(repo, key).LoadHistory(context.Background(), time.Now())
	if e != nil || len(restored.Entries) != 4 {
		t.Fatal("original history missing after close/reopen", e)
	}
	repo.mu.Lock()
	for _, record := range repo.records {
		if bytes.Contains(record.Payload, []byte(pq)) || bytes.Contains(record.Payload, []byte("NativeJSON")) {
			t.Error("original receipt leaked plaintext into storage")
		}
	}
	repo.mu.Unlock()
	s.OpenHistory(7301, nil)
	// The same history snapshot opens the original receipt without executing SQL.
	entry := restored.Entries[0]
	s.history.browsers[7301].selected = entry.ID
	tt.Frame()
	if e := tt.Click(locales.Text("原始执行回执")); e != nil {
		t.Fatal(e)
	}
	tt.Frame()
	if s.nativeReceipt == nil || s.nativeReceipt.json != entry.NativeJSON || !tt.HasText(locales.Text("复制完整回执")) || tt.HasText(locales.Text("SQL 历史与日志")) {
		t.Fatal("history obscured or reconstructed original receipt", tt.Texts())
	}
	capture("en-history-original-receipt")
	if e := tt.Click(locales.Text("关闭")); e != nil {
		t.Fatal(e)
	}
	tt.Frame()
	if s.nativeReceipt != nil || !tt.HasText(locales.Text("SQL 历史与日志")) {
		t.Fatal("closing receipt lost history", tt.Texts())
	}
}
func TestManticoreNativeSQLLostFileHistoryRecoveryAndNoReplay(t *testing.T) {
	cfg := manticoreSQLConfig(t)
	control := manticoreSQLSession(t, cfg)
	q, e := control.ReserveQuery(99)
	if e != nil {
		t.Fatal(e)
	}
	defer q.Release()
	table := fmt.Sprintf("nyadb_workspace_loss73_%d", time.Now().UnixNano())
	manticoreSQLRun(t, q, "CREATE TABLE "+table+" (title text,qty integer)")
	defer manticoreSQLRun(t, q, "DROP TABLE "+table)
	gate := newSQLPacketGate(t, net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port))))
	host, port, _ := net.SplitHostPort(gate.listener.Addr().String())
	n, _ := strconv.Atoi(port)
	cfg.Host, cfg.Port = host, uint16(n)
	native := manticoreSQLSession(t, cfg)
	s, d, store, repo, key := manticoreSQLState(t, native)
	statement := func(id int) string { return fmt.Sprintf("INSERT INTO %s (id,qty) VALUES (%d,7)", table, id) }
	source := statement(1) + "; " + statement(2) + "; " + statement(3)
	s.persistence.files = Files{OpenPath: func() (string, error) { return "original-loss.sql", nil }, OpenReader: func(string) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(source)), nil }}
	observed := gate.arm(statement(2), "hold")
	s.RunFile(d)
	select {
	case ack := <-observed:
		if len(ack) < 6 || ack[4] != 0 {
			t.Fatalf("not original OK %x", ack)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no original write acknowledgement")
	}
	s.RequestCancel(d)
	if !d.Loading() {
		t.Fatal("cancellation settled synchronously without executor")
	}
	manticoreSQLWait(t, s, func() bool { return !d.Loading() })
	waitHistory(t, s, true)
	if !d.NeedsRecovery() || d.Result() == nil || len(d.Result().Steps) != 3 || d.Result().Steps[1].State != "uncertain" || d.Result().Steps[2].State != "not_executed" {
		t.Fatal("original lost file outcome", d.Error())
	}
	original := d.Result().NativeJSON
	h, e := store.LoadHistory(context.Background(), time.Now())
	if e != nil || len(h.Entries) != 2 {
		t.Fatal(h, e)
	}
	uncertain := ""
	for _, entry := range h.Entries {
		if entry.State == "uncertain" {
			uncertain = entry.NativeJSON
		}
	}
	if uncertain == "" {
		t.Fatal("unknown native receipt became successful history")
	}
	s.RunFile(d)
	s.Prepare(d)
	if d.Loading() || gate.calls.Load() != 1 {
		t.Fatal("lost file replayed")
	}
	actual := manticoreSQLRun(t, q, "SELECT id FROM "+table+" ORDER BY id ASC")
	if len(actual.Rows) != 2 || actual.Rows[1][0] != "2" {
		t.Fatal(actual)
	}
	if d.Result().NativeJSON != original {
		t.Fatal("readback repaired original unknown")
	}
	s.Recover(d)
	manticoreSQLWait(t, s, func() bool { return d.prepareResults == nil })
	if d.NeedsRecovery() || d.Editor.Buffer.String() != "SELECT 'unsaved original';" || d.Result() == nil || d.Result().NativeJSON != original || gate.calls.Load() != 1 {
		t.Fatal("recovery replayed or discarded original receipt", d.Error())
	}
	manticoreSQLEvidence(t, "native-sqlworkspace-unknown-file.json", d.Result())
	manticoreSQLEvidence(t, "native-sqlworkspace-unknown-history.json", h)
	s.Close(d)
	restored, e := queries.NewStore(repo, key).LoadHistory(context.Background(), time.Now())
	if e != nil || len(restored.Entries) != 2 {
		t.Fatal(e)
	}
	found := false
	for _, entry := range restored.Entries {
		if entry.State == "uncertain" && entry.NativeJSON == uncertain {
			found = true
		}
	}
	if !found {
		t.Fatal("closed original unknown receipt lost")
	}
}

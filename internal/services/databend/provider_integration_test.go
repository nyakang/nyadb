package databend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/storage/localdb"
)

func nativeProviderProfile(t *testing.T, f managedFixture, id int64, directory string) connections.Profile {
	t.Helper()
	u, err := url.Parse(f.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := connections.Defaults(connections.Databend)
	p.ID, p.Name, p.Host, p.Port, p.Username, p.Database = id, "original Databend", host, uint16(n), f.Username, f.Database
	p.DatabendOptions.DriverVersion, p.DatabendOptions.DriverSHA256 = f.Version, f.SHA256
	p.DatabendOptions.DriverDirectory = directory
	p.DatabendOptions.ConnectTimeoutSeconds = 60
	return p
}

func openNativeProvider(t *testing.T, f managedFixture, p connections.Profile) *Session {
	t.Helper()
	provider := Provider{Started: func(pid int) error { return recordManagedNativeProcess(t, f, pid) },
		Observe: func(method string, receipt agentbridge.Receipt) {
			recordTransportReceipt(t, f.localTransportFixture, "provider-"+method, receipt)
		}}
	original, err := provider.Open(context.Background(), p, localTransportSecret(t, f.localTransportFixture))
	if err != nil {
		t.Fatal("actual original Databend provider open", err)
	}
	s := original.(*Session)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func reserveNativeQuery(t *testing.T, s *Session, document uint64) *queryLease {
	t.Helper()
	l, err := s.ReserveQuery(document)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Release)
	if err := l.Preconnect(context.Background()); err != nil {
		t.Fatal("original provider physical query lease", err)
	}
	return l.(*queryLease)
}

func providerQuery(t *testing.T, l *queryLease, database, source string) contract.SQLResult {
	t.Helper()
	r, err := l.Execute(context.Background(), database, source)
	if err != nil {
		t.Fatal("actual Databend SQL projection", err, source)
	}
	return r
}

func TestDatabendProviderOriginalNamespacesStructuresSQLAndPartialReceipts(t *testing.T) {
	f := managedNativeFixture(t)
	_ = managedNativeStore(t, f)
	p := nativeProviderProfile(t, f, 501, filepath.Join(f.Directory, t.Name(), "store"))
	s := openNativeProvider(t, f, p)
	namespaces, err := s.Namespaces(context.Background())
	if err != nil {
		t.Fatal("original namespace metadata", err)
	}
	decoy := strings.Replace(f.Database, "nyadb_transport_", "nyadbXtransport_", 1)
	found := map[string]bool{}
	for _, n := range namespaces {
		if n.Ref.ProfileID != p.ID || n.Ref.Catalog != "" || n.Label != n.Ref.Schema {
			t.Fatal("original namespace identity changed")
		}
		found[n.Ref.Schema] = true
	}
	if !found[f.Database] || !found[decoy] {
		t.Fatal("literal underscore database or independent database missing")
	}
	objects, err := s.Browse(context.Background(), s.Namespace(f.Database))
	if err != nil {
		t.Fatal("qualified original object metadata", err)
	}
	byName := map[string]contract.DBObject{}
	for _, object := range objects {
		if object.Ref.ProfileID != p.ID || object.Ref.Catalog != "" || object.Ref.Schema != f.Database || object.Name == "foreign_table" {
			t.Fatal("wildcard metadata leaked another database")
		}
		byName[object.Name] = object
	}
	if byName["managed_values"].Ref.Kind != "table" || byName["native_view"].Ref.Kind != "view" {
		t.Fatal("original table/view identity missing")
	}
	for _, name := range []string{"managed_values", "native_view"} {
		structure, err := s.TableMetadata(context.Background(), byName[name].Ref)
		if err != nil || structure == nil || structure.Object != byName[name].Ref || structure.Editable || len(structure.Columns) != 4 || structure.Definition == "" {
			t.Fatal("native DESCRIBE/SHOW CREATE projection", name, err)
		}
		if name == "managed_values" && (!strings.Contains(structure.Definition, "ENGINE=FUSE") || structure.Columns[0].ValueKind != contract.Integer || structure.Columns[1].ValueKind != contract.Decimal || structure.Columns[1].NumericPrecision == nil || *structure.Columns[1].NumericPrecision != 38 || *structure.Columns[1].NumericScale != 20 || structure.Columns[2].ValueKind != contract.Binary || !structure.Columns[3].Nullable) {
			t.Fatal("native engine/type/precision/nullability metadata lost")
		}
	}
	if _, err := s.Browse(context.Background(), contract.NamespaceRef{ProfileID: p.ID + 1, Schema: f.Database}); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("foreign profile metadata admitted", err)
	}
	first, second := reserveNativeQuery(t, s, 1), reserveNativeQuery(t, s, 2)
	providerQuery(t, first, f.Database, "SET max_threads=2")
	providerQuery(t, second, f.Database, "SET max_threads=3")
	for i, l := range []*queryLease{first, second} {
		q := providerQuery(t, l, f.Database, "SELECT current_database(), (SELECT value FROM system.settings WHERE name='max_threads'), (SELECT value FROM system.settings WHERE name='timezone')")
		row := q.Sets[0].Rows[0]
		if row[0].Text != f.Database || row[1].Text != strconv.Itoa(i+2) || row[2].Text != "Asia/Shanghai" {
			t.Fatal("provider shared physical settings or replaced server defaults")
		}
	}
	var rows int
	q, err := first.ExecuteStream(context.Background(), f.Database, "SELECT id,exact,payload,absent FROM managed_values", func(_ int, columns []contract.Column, row []contract.Cell) error {
		rows++
		if row[0].Text != "18446744073709551615" || row[1].Text != "9007199254740993.12345678901234567890" || !bytes.Equal(row[2].Bytes, []byte{0, 255, 128}) || row[3].Kind != contract.Null {
			t.Fatal("SQL stream lost native typed values")
		}
		columns[0].Name = "mutated sink"
		row[2].Bytes[0] = 99
		return nil
	})
	if err != nil || rows != 1 || q.Sets[0].Columns[0].Name != "id" || q.Sets[0].Rows[0][2].Bytes[0] != 0 {
		t.Fatal("stream callback modified cached results", err)
	}
	file := `-- original file
SELECT 'a\';still one'; SELECT $$body; preserved$$; -- terminal comment`
	q, err = first.ExecuteFile(context.Background(), f.Database, strings.NewReader(file))
	if err != nil || len(q.Steps) != 2 || len(q.Sets) != 2 || q.Sets[0].Rows[0][0].Text != "a';still one" || q.Sets[1].Rows[0][0].Text != "body; preserved" {
		t.Fatal("native SQL file/string/body statement boundaries differ", err)
	}
	source := "INSERT INTO managed_writes VALUES (41); SELECT missing_original_column; INSERT INTO managed_writes VALUES (42)"
	q, err = first.Execute(context.Background(), f.Database, source)
	receipts := first.OriginalQueryReceipts()
	if err == nil || len(q.Steps) != 3 || q.Steps[0].State != "completed" || q.Steps[0].AffectedRows != 1 || q.Steps[1].State != "uncertain" || q.Steps[2].State != "not_started" || q.AffectedRows != 1 || len(receipts) != 2 || receipts[1].RemoteError == nil || !receipts[1].ExecutorAcknowledged {
		t.Fatal("first durable write/error/unsent suffix or original failure receipt lost", err, q.Steps)
	}
	original := receipts[0].OriginalResponse()
	receipts[0].Result[0] = '?'
	if !bytes.Equal(original, first.OriginalQueryReceipts()[0].OriginalResponse()) || !json.Valid(first.OriginalQueryReceipts()[0].Result) {
		t.Fatal("receipt caller mutated retained original bytes")
	}
	if got := localNativeQuery(t, f.localTransportFixture, "SELECT id FROM \""+f.Database+"\".managed_writes WHERE id IN (41,42) ORDER BY id"); len(got) != 1 || got[0][0] != "41" {
		t.Fatal("native partial write was rolled back/replayed or suffix dispatched")
	}
	q = providerQuery(t, first, f.Database, "UPDATE managed_writes SET id=43 WHERE id=41; DELETE FROM managed_writes WHERE id=43")
	if len(q.Steps) != 2 || q.Steps[0].AffectedRows != 1 || q.Steps[1].AffectedRows != 1 || q.AffectedRows != 2 {
		t.Fatal("native UPDATE/DELETE count rows were replaced by JDBC affected_rows")
	}
}

func TestDatabendProviderOriginalPinnedProfileRestartCloneAndReconnect(t *testing.T) {
	f := managedNativeFixture(t)
	_ = managedNativeStore(t, f)
	directory := filepath.Join(f.Directory, t.Name())
	p := nativeProviderProfile(t, f, 0, filepath.Join(directory, "store"))
	path := filepath.Join(directory, "profiles.sqlite")
	db, err := localdb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	store := localdb.NewProfileStore(db)
	saved, err := store.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	first := openNativeProvider(t, f, saved)
	lease := reserveNativeQuery(t, first, 1)
	providerQuery(t, lease, f.Database, "SELECT current_database()")
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = localdb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store = localdb.NewProfileStore(db)
	restored, err := store.Get(context.Background(), saved.ID)
	if err != nil || restored.DatabendOptions != saved.DatabendOptions || restored.Database != f.Database {
		t.Fatal("original explicit driver/target settings changed on restart", err)
	}
	clone, err := connections.NewService(store).Duplicate(context.Background(), saved.ID, "original clone")
	if err != nil || clone.ID == saved.ID || clone.DatabendOptions != restored.DatabendOptions {
		t.Fatal("clone did not retain original explicit package", err)
	}
	for _, profile := range []connections.Profile{restored, clone} {
		s := openNativeProvider(t, f, profile)
		q := providerQuery(t, reserveNativeQuery(t, s, 1), f.Database, "SELECT current_database()")
		if q.Sets[0].Rows[0][0].Text != f.Database || s.ProfileID() != profile.ID {
			t.Fatal("restored/clone actual connection lost original target/owner")
		}
	}
	bytes, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(bytes), localTransportSecret(t, f.localTransportFixture)) {
		t.Fatal("profile persistence retained temporary credentials", err)
	}
}

func TestDatabendProviderOriginalSQLCancellationWaitsForExecutorAndKeepsIndependentLease(t *testing.T) {
	f := managedNativeFixture(t)
	pageStarted, finalized := make(chan struct{}), make(chan struct{})
	var originalID atomic.Value
	var blocked atomic.Bool
	var submitted, finalStatus atomic.Int32
	var finishOnce sync.Once
	product := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw, _ := io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(raw))
		if req.Method == "POST" && req.URL.Path == "/v1/query" && bytes.Contains(raw, []byte("SELECT sleep(2)")) {
			submitted.Add(1)
			originalID.Store(req.Header.Get(queryIDHeader))
		}
		id, _ := originalID.Load().(string)
		if id != "" && req.Method == "GET" && strings.HasPrefix(req.URL.Path, "/v1/query/"+id+"/page/") && blocked.CompareAndSwap(false, true) {
			close(pageStarted)
			select {
			case <-finalized:
			case <-time.After(8 * time.Second):
				t.Error("original provider JDBC did not finalize its captured query")
			}
		}
		code, headers, body := forwardFixture(t, f.Endpoint, req)
		if id != "" && req.Method == "GET" && req.URL.Path == "/v1/query/"+id+"/final" {
			finalStatus.Store(int32(code))
			finishOnce.Do(func() { close(finalized) })
		}
		fixtureResponse(w, code, headers, body)
	}))
	defer product.Close()
	_ = managedNativeStore(t, f)
	proxied := f
	proxied.Endpoint = product.URL
	s := openNativeProvider(t, proxied, nativeProviderProfile(t, proxied, 504, filepath.Join(f.Directory, t.Name(), "store")))
	first, other := reserveNativeQuery(t, s, 1), reserveNativeQuery(t, s, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type outcome struct {
		result contract.SQLResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		r, err := first.Execute(ctx, f.Database, "SELECT sleep(2); INSERT INTO managed_writes VALUES (59)")
		done <- outcome{r, err}
	}()
	select {
	case <-pageStarted:
	case <-time.After(15 * time.Second):
		cancel()
		t.Fatal("original provider SQL did not enter captured active page")
	}
	cancel()
	var got outcome
	select {
	case got = <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("provider cancellation did not settle its original executor")
	}
	receipts := first.OriginalQueryReceipts()
	if !errors.Is(got.err, context.Canceled) || len(receipts) != 1 || !receipts[0].Dispatched || !receipts[0].CancellationRequested || !receipts[0].ExecutorAcknowledged || receipts[0].ProcessExitConfirmed || submitted.Load() != 1 || finalStatus.Load() != 200 || len(got.result.Steps) != 2 || got.result.Steps[1].State != "not_started" {
		t.Fatal("provider cancellation returned without original acknowledgement or dispatched its suffix", got.err)
	}
	if err := first.CheckQueryLease(); err != nil {
		t.Fatal("acknowledged provider cancellation lost original physical lease", err)
	}
	providerQuery(t, other, f.Database, "SELECT 535353")
	providerQuery(t, first, f.Database, "SELECT 545454")
	if rows := localNativeQuery(t, f.localTransportFixture, "SELECT count() FROM \""+f.Database+"\".managed_writes WHERE id=59"); len(rows) != 1 || rows[0][0] != "0" {
		t.Fatal("canceled provider SQL replayed its unsent write")
	}
	record, _ := json.Marshal(map[string]any{"original_query_id": originalID.Load(), "submitted": submitted.Load(), "final_http_status": finalStatus.Load(), "executor_acknowledged": receipts[0].ExecutorAcknowledged, "native_kill_or_rollback_accepted": false})
	if err := os.WriteFile(filepath.Join(f.Directory, "provider-cancellation.json"), record, 0600); err != nil {
		t.Fatal(err)
	}
}

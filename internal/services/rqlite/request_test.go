package rqlite

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

const handshake = `{"results":[{"columns":["1","sqlite_version()"],"types":["integer","text"],"values":[[1,"3.54.1"]]}]}`

func testProfile(t *testing.T, endpoint string) connections.Profile {
	t.Helper()
	u, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.ParseUint(u.Port(), 10, 16)
	p, _ := connections.Defaults(connections.RQLite)
	p.ID, p.Name, p.Host, p.Port = 81, "original HTTP", u.Hostname(), uint16(port)
	return p
}
func mockSession(t *testing.T, handler http.HandlerFunc, change func(*connections.Profile)) *Session {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RQLITE-VERSION", "10.7.1")
		w.Header().Set("Content-Type", "application/json")
		var body []any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid request body")
			w.WriteHeader(400)
			return
		}
		if len(body) == 1 && body[0] == "SELECT 1,sqlite_version()" {
			fmt.Fprint(w, handshake)
			return
		}
		// Reconstruct the body for the scenario's actual handler.
		raw, _ := json.Marshal(body)
		r.Body = http.NoBody
		r.Body = ioBody(string(raw))
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	p := testProfile(t, server.URL)
	if change != nil {
		change(&p)
	}
	opened, err := OpenSession(context.Background(), p, "")
	if err != nil {
		t.Fatal(err)
	}
	s := opened.(*Session)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

type stringBody struct{ *strings.Reader }

func (stringBody) Close() error  { return nil }
func ioBody(s string) stringBody { return stringBody{strings.NewReader(s)} }
func lease(t *testing.T, s *Session, doc uint64) *queryLease {
	t.Helper()
	l, err := s.ReserveQuery(doc)
	if err != nil {
		t.Fatal(err)
	}
	return l.(*queryLease)
}

func TestNativeBatchReceiptTypedProjectionAndRollback(t *testing.T) {
	var requests atomic.Int32
	original := `{"results":[{"last_insert_id":9007199254740993,"rows_affected":1},{"columns":["id","raw","empty","nil"],"types":["integer","blob","text",""],"values":[[9007199254740993,[0,255,39],"",null]]}],"raft_index":987,"time":0.0123}`
	s := mockSession(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/db/request" || !r.URL.Query().Has("transaction") || !r.URL.Query().Has("blob_array") || r.URL.Query().Get("retries") != "0" || r.URL.Query().Get("db_timeout") != "30000ms" {
			t.Error("original request semantics changed")
		}
		if requests.Add(1) == 1 {
			fmt.Fprint(w, original)
		} else {
			fmt.Fprint(w, `{"results":[{"rows_affected":1},{"error":"UNIQUE private-secret","error_v2":{"message":"private-secret","sqlite":{"code":19,"extended_code":1555}}}]}`)
		}
	}, func(p *connections.Profile) { p.RQLiteOptions.BatchTransaction = true })
	l := lease(t, s, 1)
	r, err := l.Execute(context.Background(), "main", "WITH v AS (VALUES(1)) INSERT INTO t SELECT * FROM v;SELECT * FROM t")
	if err != nil || r.NativeJSON != original || len(r.Sets) != 1 || len(r.Steps) != 2 || r.AffectedRows != 1 {
		t.Fatalf("receipt/projection: %v", err)
	}
	c := r.Sets[0].Rows[0]
	if c[0].Text != "9007199254740993" || c[0].Kind != contract.Integer || c[1].Kind != contract.Binary || string(c[1].Bytes) != "\x00\xff'" || c[2].Kind != contract.Text || c[2].Text != "" || c[3].Kind != contract.Null {
		t.Fatal("native types changed")
	}
	r, err = l.Execute(context.Background(), "main", "INSERT INTO t VALUES(1);INSERT INTO t VALUES(1);SELECT * FROM t")
	if err == nil || strings.Contains(err.Error(), "private-secret") || r.AffectedRows != 0 || len(r.Steps) != 3 || r.Steps[0].State != "rolled_back" || r.Steps[1].State != "failed" || r.Steps[2].State != "not_executed" || !strings.Contains(r.NativeJSON, `"rows_affected":1`) {
		t.Fatal("rollback was confused with an earlier affected count")
	}
	if l.CheckQueryLease() != nil {
		t.Fatal("acknowledged statement failure must keep original lease usable")
	}
}

func TestCancellationAndConcurrentCloseWaitForOriginalReceipt(t *testing.T) {
	started, acknowledge := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-acknowledge:
		default:
			close(acknowledge)
		}
	}()
	s := mockSession(t, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-acknowledge
		fmt.Fprint(w, `{"results":[{"rows_affected":1}]}`)
	}, nil)
	l := lease(t, s, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		r, err := l.Execute(ctx, "main", "UPDATE t SET v=1")
		if err == nil && (r.AffectedRows != 1 || r.Steps[0].State != "completed") {
			err = errors.New("completed original receipt discarded")
		}
		result <- err
	}()
	<-started
	cancel()
	released := make(chan struct{})
	go func() { l.Release(); close(released) }()
	closed := make(chan error, 2)
	go func() { closed <- s.Close() }()
	go func() { closed <- s.Close() }()
	select {
	case <-result:
		t.Fatal("cancel returned before original executor receipt")
	case <-released:
		t.Fatal("release returned before receipt")
	case <-closed:
		t.Fatal("close returned before receipt")
	case <-time.After(30 * time.Millisecond):
	}
	close(acknowledge)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	<-released
	for range 2 {
		if err := <-closed; err != nil {
			t.Fatal(err)
		}
	}
	if !errors.Is(l.CheckQueryLease(), contract.ErrRevoked) {
		t.Fatal("released lease admitted work")
	}
}

func TestMissingWriteReceiptRequiresExplicitRecoveryWithoutReplay(t *testing.T) {
	var writes, reads atomic.Int32
	s := mockSession(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/db/query" {
			reads.Add(1)
			fmt.Fprint(w, `{"results":[{"columns":["1"],"types":["integer"],"values":[[1]]}]}`)
			return
		}
		writes.Add(1)
		c, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = c.Close()
	}, nil)
	l := lease(t, s, 1)
	r, err := l.Execute(context.Background(), "main", "UPDATE t SET v=1")
	if !errors.Is(err, contract.ErrSQLSessionLost) || len(r.Steps) != 1 || r.Steps[0].State != "uncertain" || r.AffectedRows != 0 {
		t.Fatal("missing original write receipt treated as failure/rollback")
	}
	if _, err := l.Execute(context.Background(), "main", "UPDATE t SET v=1"); !errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatal("lost lease admitted replay")
	}
	if err := l.Preconnect(context.Background()); !errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatal("lost lease silently recovered")
	}
	replacement, err := l.Replace()
	if err != nil {
		t.Fatal(err)
	}
	if writes.Load() != 1 || reads.Load() != 0 {
		t.Fatal("replacement replayed original work")
	}
	if err := replacement.Preconnect(context.Background()); err != nil || writes.Load() != 1 || reads.Load() != 1 {
		t.Fatal("explicit recovery did not retain original request isolation")
	}
}

func TestForeignRedirectNeverReceivesCredentialsOrSQL(t *testing.T) {
	var forwarded atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Add(1); w.WriteHeader(200) }))
	defer other.Close()
	s := mockSession(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", other.URL)
		w.WriteHeader(307)
	}, nil)
	l := lease(t, s, 1)
	_, err := l.Execute(context.Background(), "main", "SELECT 'private'")
	if err == nil || forwarded.Load() != 0 || strings.Contains(err.Error(), other.URL) {
		t.Fatal("foreign redirect followed or leaked its target")
	}
}

func TestIncompleteAndTopLevelNativeReceiptsNeverConfirmWrites(t *testing.T) {
	for _, raw := range []string{`{"results":[]}`, `{"error":"private-secret","results":[]}`, `{"results":[{}],"error":"private-secret"}`, `{"results":[`, `{"results":[{},{}]}`, `null`} {
		t.Run(raw, func(t *testing.T) {
			s := mockSession(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, raw) }, nil)
			l := lease(t, s, 1)
			result, err := l.Execute(context.Background(), "main", "UPDATE t SET v=1")
			if !errors.Is(err, contract.ErrSQLSessionLost) || len(result.Steps) != 1 || result.Steps[0].State != "uncertain" || strings.Contains(err.Error(), "private-secret") {
				t.Fatal("incomplete original receipt admitted")
			}
		})
	}
}

func TestSQLiteSplitterKeepsBracketIdentifiersAndTriggerBodies(t *testing.T) {
	source := "-- initial\nCREATE TEMP TRIGGER [trigger;name] AFTER INSERT ON [table;name] BEGIN INSERT INTO t VALUES(CASE WHEN 1 THEN 'a;''b' ELSE 'x' END); UPDATE t SET v='value'; END; SELECT [$name;column],$variable FROM [table;name]; -- tail"
	parts, err := splitSQL(source)
	if err != nil || len(parts) != 2 || !strings.Contains(parts[0], "UPDATE t") || !strings.Contains(parts[1], "[$name;column]") {
		t.Fatalf("SQLite trigger split: %v", err)
	}
	for _, source := range []string{"BEGIN;INSERT INTO t VALUES(1);COMMIT", "-- comments\nROLLBACK", "/* comments */ SAVEPOINT x", "SELECT [unterminated", "SELECT 'unterminated", "/* unterminated", "-- comments only"} {
		if _, err := splitSQL(source); err == nil {
			t.Fatal("unsupported/incomplete SQL admitted")
		}
	}
}

func TestCancelledAdmissionDoesNotDispatchAnotherRequest(t *testing.T) {
	started, acknowledge := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-acknowledge:
		default:
			close(acknowledge)
		}
	}()
	var count atomic.Int32
	s := mockSession(t, func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		close(started)
		<-acknowledge
		fmt.Fprint(w, `{"results":[{}]}`)
	}, func(p *connections.Profile) { p.RQLiteOptions.MaxRequests, p.RQLiteOptions.SQLMaxConnections = 1, 1 })
	l := lease(t, s, 1)
	if _, err := s.ReserveQuery(2); err == nil {
		t.Fatal("SQL admission limit ignored")
	}
	first := make(chan error, 1)
	go func() { _, err := l.Execute(context.Background(), "main", "CREATE TABLE t(id)"); first <- err }()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.request(ctx, "/db/query", []any{"SELECT 1"}, false, false); !errors.Is(err, context.Canceled) || count.Load() != 1 {
		t.Fatal("canceled admission dispatched another request")
	}
	close(acknowledge)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
}

func TestPresentationFailureRetainsAllOriginalStatementOutcomes(t *testing.T) {
	original := `{"results":[{"rows_affected":1},{"columns":["v"],"types":["real"],"values":[[1]]},{}]}`
	s := mockSession(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, original) }, nil)
	l := lease(t, s, 1)
	sinkError := errors.New("presentation consumer closed")
	r, err := l.ExecuteStream(context.Background(), "main", "UPDATE t SET v=1;SELECT v FROM t;CREATE TABLE other(id)", func(_ int, _ []contract.Column, cells []contract.Cell) error {
		if cells[0].Kind != contract.Floating {
			t.Error("real-valued integral token lost its native type")
		}
		return sinkError
	})
	if !errors.Is(err, sinkError) || r.NativeJSON != original || len(r.Steps) != 3 || r.AffectedRows != 1 || r.Steps[2].State != "completed" || l.CheckQueryLease() != nil {
		t.Fatal("presentation failure erased original completion evidence")
	}
}

func TestV2OnlyNativeTimeoutRemainsAcknowledged(t *testing.T) {
	s := mockSession(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"results":[{"error_v2":{"message":"query timeout"}}]}`)
	}, nil)
	l := lease(t, s, 1)
	r, err := l.Execute(context.Background(), "main", "SELECT long_query()")
	if !errors.Is(err, context.DeadlineExceeded) || len(r.Steps) != 1 || r.Steps[0].State != "failed" || l.CheckQueryLease() != nil {
		t.Fatal("v2-only timeout was confused with transport loss")
	}
}

func TestMalformedOriginalHTTPBodyRetainsExactEvidence(t *testing.T) {
	original := "broken response\x00\xffprivate-secret"
	s := mockSession(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(502); fmt.Fprint(w, original) }, nil)
	r, err := lease(t, s, 1).Execute(context.Background(), "main", "UPDATE t SET v=1")
	var captured struct {
		Status int    `json:"http_status"`
		Body   string `json:"body_base64"`
	}
	if !errors.Is(err, contract.ErrSQLSessionLost) || strings.Contains(err.Error(), "private-secret") || json.Unmarshal([]byte(r.NativeJSON), &captured) != nil || captured.Status != 502 {
		t.Fatal("malformed original HTTP response evidence lost")
	}
	raw, err := base64.StdEncoding.DecodeString(captured.Body)
	if err != nil || string(raw) != original {
		t.Fatal("complete original HTTP response bytes changed")
	}
}

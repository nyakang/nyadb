package qdrant

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

const sessionCollections = `{"status":"ok","result":{"collections":[{"name":"owned"}]}}`
const sessionCollection = `{"status":"ok","result":{"status":"green","config":{"params":{"vectors":{"dense":{"size":2,"distance":"Dot"}},"sparse_vectors":{"sparse":{}}},"future":9007199254740993},"payload_schema":{"text":{"data_type":"keyword"}},"points_count":9007199254740993}}`

func sessionProfile(t *testing.T, cfg connections.QdrantConfig) connections.Profile {
	t.Helper()
	cfg, err := connections.NormalizeQdrantConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(cfg.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.ParseUint(u.Port(), 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := connections.Defaults(connections.Qdrant)
	p.ID, p.Name, p.Host, p.Port = 71, "original native profile", u.Hostname(), uint16(port)
	if u.Scheme == "https" {
		p.TLSMode = "verify-full"
	}
	p.QdrantOptions = connections.QdrantOptions{Version: 1, Auth: cfg.Auth, ContextPath: u.Path, CAFile: cfg.CAFile, ServerName: cfg.ServerName, ClientCertFile: cfg.ClientCertFile, ClientKeyFile: cfg.ClientKeyFile, ConnectTimeoutSeconds: cfg.ConnectTimeoutSeconds, RequestTimeoutSeconds: cfg.RequestTimeoutSeconds}
	return p
}
func testSession(t *testing.T, cfg connections.QdrantConfig, secret string) *Session {
	t.Helper()
	s, err := OpenSession(context.Background(), sessionProfile(t, cfg), secret)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s.(*Session)
}
func sessionServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/":
			fmt.Fprint(w, unitIdentity)
		case "/collections":
			fmt.Fprint(w, sessionCollections)
		default:
			handler(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}
func waitSessionSignal(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("original executor did not settle")
	}
}

func TestQdrantSessionQualifiedCollectionsAndFullIndependentDefinitions(t *testing.T) {
	aliases := `{"status":"ok","result":{"aliases":[{"alias_name":"alias","collection_name":"owned"}]}}`
	var calls atomic.Int32
	server := sessionServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Path {
		case "/collections/owned":
			fmt.Fprint(w, sessionCollection)
		case "/collections/owned/aliases":
			fmt.Fprint(w, aliases)
		default:
			w.WriteHeader(404)
		}
	})
	s := testSession(t, connections.QdrantConfig{Endpoint: server.URL}, "")
	names, err := s.Namespaces(context.Background())
	if err != nil || len(names) != 1 || names[0].Ref != s.Namespace("owned") || names[0].Label != "owned" || s.Generation() != 1 {
		t.Fatal("qualified namespace", names, err)
	}
	objects, err := s.Browse(context.Background(), names[0].Ref)
	if err != nil || len(objects) != 3 || objects[0].Definition != sessionCollection {
		t.Fatal("complete native objects", objects, err)
	}
	for _, object := range objects {
		structure, err := s.TableMetadata(context.Background(), object.Ref)
		if err != nil || structure.Object != object.Ref || len(structure.Definitions) != 1 || len(structure.Columns) != 0 {
			t.Fatal("native structure became a SQL table", err)
		}
		want := sessionCollection
		if object.Ref.Kind == "aliases" {
			want = aliases
		}
		if structure.Definition != want || structure.Definitions[0].SQL != want {
			t.Fatal("native definition changed")
		}
	}
	before := calls.Load()
	for _, object := range []contract.ObjectRef{
		{ProfileID: 72, Catalog: "owned", Kind: "collection", Name: "owned"},
		{ProfileID: 71, Catalog: "owned", Schema: "fake", Kind: "collection", Name: "owned"},
		{ProfileID: 71, Catalog: "../other", Kind: "collection", Name: "owned"},
		{ProfileID: 71, Catalog: "owned", Kind: "collection", Name: "other"},
		{ProfileID: 71, Catalog: "owned", Kind: "points", Name: "other"},
		{ProfileID: 71, Catalog: "owned", Kind: "table", Name: "owned"},
	} {
		if _, err := s.TableMetadata(context.Background(), object); err == nil {
			t.Fatal("unqualified/unsupported identity accepted")
		}
	}
	if calls.Load() != before {
		t.Fatal("invalid identity dispatched")
	}
	if _, err := s.Browse(context.Background(), s.Namespace("alias")); !errors.Is(err, connections.ErrInvalid) || calls.Load() != before {
		t.Fatal("alias became a physical namespace", err)
	}
	c := s.Capabilities()
	if !c.Query || !c.Metadata || !c.Objects || c.EditableTables || c.Transactions || c.Count || c.Documents || c.Export {
		t.Fatal("unsupported data/SQL capabilities", c)
	}
}

func TestQdrantSessionMetadataBoundsAndAliasValidation(t *testing.T) {
	for _, raw := range []string{
		`{"status":"ok","result":{"aliases":null}}`,
		`{"status":"ok","result":{"aliases":[{"alias_name":"a","collection_name":"other"}]}}`,
		`{"status":"ok","result":{"aliases":[{"alias_name":"a","collection_name":"owned"},{"alias_name":"a","collection_name":"owned"}]}}`,
	} {
		if validCollectionAliases([]byte(raw), "owned") {
			t.Fatal("invalid alias definition accepted")
		}
	}
	var large atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			fmt.Fprint(w, unitIdentity)
			return
		}
		if !large.Load() {
			fmt.Fprint(w, emptyCollections)
			return
		}
		fmt.Fprint(w, `{"status":"ok","result":{"collections":[`)
		for i := 0; i <= metadataLimit; i++ {
			if i != 0 {
				fmt.Fprint(w, ",")
			}
			fmt.Fprintf(w, `{"name":"owned_%d"}`, i)
		}
		fmt.Fprint(w, `]}}`)
	}))
	defer server.Close()
	s := testSession(t, connections.QdrantConfig{Endpoint: server.URL}, "")
	large.Store(true)
	if names, err := s.Namespaces(context.Background()); !errors.Is(err, ErrBounds) || names != nil {
		t.Fatal("oversized metadata silently truncated", err)
	}
}

func TestQdrantQueryLeaseExactOutcomeReadFailureAndSinkFailure(t *testing.T) {
	var writes atomic.Int32
	body := `{"n":9007199254740993,"decimal":1.2300e-20,"text":"原生🐱"}`
	server := sessionServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/write":
			writes.Add(1)
			raw, err := io.ReadAll(r.Body)
			if err != nil || !bytes.Equal(raw, []byte(body)) {
				t.Error("original source changed")
			}
			fmt.Fprint(w, `{"status":"ok","result":{"operation_id":9007199254740993,"status":"acknowledged"}}`)
		case "/missing":
			w.WriteHeader(404)
			fmt.Fprint(w, `{"status":{"error":"absent"}}`)
		case "/metrics":
			w.Header().Set("Content-Type", "text/plain; version=0.0.4")
			fmt.Fprint(w, "native_metric 1\n")
		}
	})
	s := testSession(t, connections.QdrantConfig{Endpoint: server.URL}, "")
	l, _ := s.ReserveQuery(1)
	if err := l.Preconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := l.ExecuteStream(context.Background(), "owned", "POST /write\n"+body, func(int, []contract.Column, []contract.Cell) error { return errors.New("presentation failed") })
	if err == nil || result.Steps[0].State != contract.StatementEnqueued || result.AffectedRows != 0 || result.Sets[0].Rows[0][1].Kind != contract.JSON || !strings.Contains(result.Sets[0].Rows[0][1].Text, "9007199254740993") || result.Truncated {
		t.Fatal("native receipt altered by presentation", result, err)
	}
	result, err = l.Execute(context.Background(), "", "GET /missing")
	if err == nil || errors.Is(err, contract.ErrSQLSessionLost) || result.Steps[0].State != "failed" || result.Sets[0].Rows[0][0].Text != "404" || result.Truncated {
		t.Fatal("completed read failure retired lease", result, err)
	}
	result, err = l.Execute(context.Background(), "", "GET /metrics")
	if err != nil || result.Sets[0].Rows[0][1].Kind != contract.Text || result.Sets[0].Rows[0][1].Text != "native_metric 1\n" || writes.Load() != 1 {
		t.Fatal("non-JSON native response or write replay", result, err)
	}
	for _, tc := range []struct{ raw, state string }{
		{`{"status":"ok","result":true}`, "completed"},
		{`{"status":"ok","result":{"operation_id":1,"status":"completed"}}`, "completed"},
		{`{"status":"ok","result":[{"operation_id":1,"status":"completed"},{"operation_id":2,"status":"acknowledged"}]}`, contract.StatementEnqueued},
		{`{"status":"ok","result":{"status":"acknowledged"}}`, ""},
		{`{"status":"ok","result":{"operation_id":1,"status":"future"}}`, ""},
		{`{"status":{"error":"failed"}}`, ""},
	} {
		state, err := requestOutcome(Request{Method: "PUT"}, Response{Status: 200, Body: []byte(tc.raw)})
		if state != tc.state || (err != nil) != (tc.state == "") {
			t.Fatal("original receipt state", tc, state, err)
		}
	}
}

func TestQdrantLeaseCancelReleaseAndCloseWaitOriginalExecutor(t *testing.T) {
	for _, method := range []string{"GET", "PUT"} {
		for _, action := range []string{"cancel", "release", "close"} {
			t.Run(method+"/"+action, func(t *testing.T) {
				entered, acked := make(chan struct{}), make(chan struct{})
				var writes atomic.Int32
				server := sessionServer(t, func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/held" {
						_, _ = io.Copy(io.Discard, r.Body)
						writes.Add(1)
						close(entered)
						<-r.Context().Done()
						close(acked)
						return
					}
					fmt.Fprint(w, `{"status":"ok","result":true}`)
				})
				cfg := connections.QdrantConfig{Endpoint: server.URL}
				s, fresh := testSession(t, cfg, ""), testSession(t, cfg, "")
				first, _ := s.ReserveQuery(1)
				other, _ := s.ReserveQuery(2)
				independent, _ := fresh.ReserveQuery(1)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				settled := make(chan error, 1)
				go func() {
					source := method + " /held"
					if method == "PUT" {
						source += "\n{}"
					}
					result, err := first.Execute(ctx, "", source)
					want := "interrupted"
					if method == "PUT" {
						want = "uncertain"
					}
					if len(result.Steps) != 1 || result.Steps[0].State != want {
						settled <- errors.New("canceled native outcome changed")
						return
					}
					settled <- err
				}()
				waitSessionSignal(t, entered)
				if action == "cancel" {
					cancel()
				} else {
					var wg sync.WaitGroup
					for i := 0; i < 2; i++ {
						wg.Add(1)
						go func() {
							defer wg.Done()
							if action == "release" {
								first.Release()
							} else {
								_ = s.Close()
							}
						}()
					}
					wg.Wait()
				}
				waitSessionSignal(t, acked)
				select {
				case err := <-settled:
					if !errors.Is(err, context.Canceled) || !errors.Is(err, contract.ErrSQLSessionLost) {
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("owning executor did not return")
				}
				if action != "close" {
					if _, err := other.Execute(context.Background(), "", "GET /alive"); err != nil {
						t.Fatal("independent original lease canceled", err)
					}
				}
				if _, err := independent.Execute(context.Background(), "", "GET /alive"); err != nil {
					t.Fatal("independent session canceled", err)
				}
				if action == "cancel" {
					replacement, err := first.Replace()
					if err != nil {
						t.Fatal(err)
					}
					if _, err := replacement.Execute(context.Background(), "", "GET /alive"); err != nil {
						t.Fatal("explicit replacement failed", err)
					}
				}
				if writes.Load() != 1 {
					t.Fatal("original request replayed")
				}
			})
		}
	}
}

func TestQdrantReservationBoundAndChangedBinaryRevokesOnlyOriginalSession(t *testing.T) {
	var changed atomic.Bool
	var dispatched atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			identity := unitIdentity
			if changed.Load() {
				identity = strings.Replace(identity, "1.20.0", "1.20.1", 1)
			}
			fmt.Fprint(w, identity)
		case "/collections":
			fmt.Fprint(w, sessionCollections)
		default:
			dispatched.Add(1)
			fmt.Fprint(w, `{"status":"ok","result":true}`)
		}
	}))
	defer server.Close()
	s := testSession(t, connections.QdrantConfig{Endpoint: server.URL}, "")
	first, _ := s.ReserveQuery(1)
	if _, err := s.ReserveQuery(1); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("duplicate owning document admitted", err)
	}
	for i := uint64(2); i <= 32; i++ {
		if _, err := s.ReserveQuery(i); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ReserveQuery(33); !errors.Is(err, contract.ErrSQLSessionLimit) || dispatched.Load() != 0 {
		t.Fatal("quota or pure reservation", err)
	}
	if err := first.Preconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	changed.Store(true)
	if _, err := first.Execute(context.Background(), "", "PUT /write\n{}"); !errors.Is(err, contract.ErrSQLSessionLost) || s.Alive() || dispatched.Load() != 0 {
		t.Fatal("changed binary allowed original dispatch", err)
	}
	if _, err := first.Replace(); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("revoked session recovered implicitly", err)
	}
	fresh := testSession(t, connections.QdrantConfig{Endpoint: server.URL}, "")
	if fresh.Identity().Version != "1.20.1" || !fresh.Alive() {
		t.Fatal("explicit new observation failed")
	}
}

func TestQdrantSessionMalformedReceiptIsUnknownWithCompleteOriginalBody(t *testing.T) {
	var writes atomic.Int32
	bad := `{"status":"ok","result":{"status":"acknowledged"}}`
	server := sessionServer(t, func(w http.ResponseWriter, r *http.Request) {
		writes.Add(1)
		fmt.Fprint(w, bad)
	})
	s := testSession(t, connections.QdrantConfig{Endpoint: server.URL}, "")
	l, _ := s.ReserveQuery(1)
	result, err := l.Execute(context.Background(), "", "PUT /write\n{}")
	if !errors.Is(err, contract.ErrSQLSessionLost) || result.Steps[0].State != "uncertain" || result.Truncated || result.Sets[0].Rows[0][1].Truncated || result.Sets[0].Rows[0][1].Text != bad {
		t.Fatal("unknown write confused with incomplete response", result, err)
	}
	if _, err := l.Execute(context.Background(), "", "GET /"); !errors.Is(err, contract.ErrSQLSessionLost) || writes.Load() != 1 {
		t.Fatal("original write replayed implicitly", err)
	}
	replacement, err := l.Replace()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := replacement.Execute(context.Background(), "", "GET /"); err != nil {
		t.Fatal("explicit recovery failed", err)
	}
}

func TestQdrantSessionMetadataCloseWaitsCapturedOriginalOpen(t *testing.T) {
	var armed atomic.Bool
	entered, acked := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			fmt.Fprint(w, unitIdentity)
			return
		}
		if armed.Load() {
			close(entered)
			<-r.Context().Done()
			close(acked)
			return
		}
		fmt.Fprint(w, sessionCollections)
	}))
	t.Cleanup(server.Close)
	s := testSession(t, connections.QdrantConfig{Endpoint: server.URL}, "")
	armed.Store(true)
	result := make(chan error, 1)
	go func() { _, err := s.Namespaces(context.Background()); result <- err }()
	waitSessionSignal(t, entered)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	waitSessionSignal(t, acked)
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("original metadata cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("metadata executor not settled by close")
	}
	if _, err := s.ReserveQuery(1); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed metadata session admitted lease", err)
	}
	armed.Store(false)
	fresh := testSession(t, connections.QdrantConfig{Endpoint: server.URL}, "")
	if !fresh.Alive() {
		t.Fatal("old metadata close retired fresh original handle")
	}
}

func TestQdrantQueryBinaryResponseAndSinkOwnSeparateBytes(t *testing.T) {
	original := []byte{0, 0xff, 0x1f, 0x8b, 0, 0x80}
	server := sessionServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		if r.URL.Path == "/binary" {
			w.Write(original)
		}
	})
	s := testSession(t, connections.QdrantConfig{Endpoint: server.URL}, "")
	l, _ := s.ReserveQuery(1)
	result, err := l.ExecuteStream(context.Background(), "", "GET /binary", func(_ int, columns []contract.Column, cells []contract.Cell) error {
		if !bytes.Equal(cells[1].Bytes, original) {
			return errors.New("original binary response changed")
		}
		cells[1].Bytes[0] = 99
		columns[1].Name = "changed"
		return nil
	})
	if err != nil || result.Sets[0].Rows[0][1].Kind != contract.Binary || !bytes.Equal(result.Sets[0].Rows[0][1].Bytes, original) || result.Sets[0].Columns[1].Name != "response" || result.Truncated || result.Steps[0].State != "completed" {
		t.Fatal("binary evidence mutated by sink", result, err)
	}
	result, err = l.Execute(context.Background(), "", "GET /empty-binary")
	if err != nil || result.Sets[0].Rows[0][1].Bytes == nil || len(result.Sets[0].Rows[0][1].Bytes) != 0 || result.Sets[0].Rows[0][1].Kind != contract.Binary {
		t.Fatal("empty binary became NULL", result, err)
	}
}

func TestQdrantQueryAsyncSnapshotAndHTTPAcceptanceDoNotClaimCompletion(t *testing.T) {
	for _, path := range []string{
		"/snapshots?wait=false", "/collections/owned/snapshots?wait=false", "/collections/owned/shards/1/snapshots?wait=false",
	} {
		state, err := requestOutcome(Request{Method: "POST", Path: path}, Response{Status: 200, Body: []byte(`{"status":"ok","result":null}`)})
		if err != nil || state != contract.StatementEnqueued {
			t.Fatal("background snapshot reported completed", path, state, err)
		}
	}
	for _, tc := range []struct{ method, path string }{
		{"DELETE", "/collections/owned/snapshots/owned.snapshot?wait=false"},
		{"PUT", "/collections/owned/snapshots/recover?wait=false"},
		{"DELETE", "/snapshots/owned.snapshot?wait=false"},
	} {
		state, err := requestOutcome(Request{Method: tc.method, Path: tc.path}, Response{Status: 200, Body: []byte(`{"status":"ok","result":true}`)})
		if err != nil || state != contract.StatementEnqueued {
			t.Fatal("background snapshot management reported completed", tc, state, err)
		}
	}
	for _, path := range []string{"/collections/owned/snapshots?wait=true", "/collections/aliases?wait=false"} {
		state, err := requestOutcome(Request{Method: "POST", Path: path}, Response{Status: 200, Body: []byte(`{"status":"ok","result":true}`)})
		if err != nil || state != "completed" {
			t.Fatal("unknown wait parameter invented background operation", path, state, err)
		}
	}
	state, err := requestOutcome(Request{Method: "POST", Path: "/collections/owned/snapshots"}, Response{Status: 202, Body: []byte(`{"status":"ok","result":null}`)})
	if err != nil || state != contract.StatementEnqueued {
		t.Fatal("HTTP 202 claimed completion", state, err)
	}
	state, err = requestOutcome(Request{Method: "POST", Path: "/collections/owned/snapshots?wait=false"}, Response{Status: 202, Body: []byte(`{"status":"accepted","time":0.001}`)})
	if err != nil || state != contract.StatementEnqueued {
		t.Fatal("native accepted envelope required a fabricated result", state, err)
	}
	for _, raw := range []string{`{"status":"unknown"}`, `{"status":"ok"}`, `{"status":"accepted","status":"ok"}`} {
		if _, err := requestOutcome(Request{Method: "POST"}, Response{Status: 202, Body: []byte(raw)}); err == nil {
			t.Fatal("malformed accepted envelope admitted")
		}
	}
}

type sessionRoundTrip func(*http.Request) (*http.Response, error)

func (f sessionRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type observedBody struct {
	io.ReadCloser
	once *sync.Once
	read chan struct{}
}

func (b *observedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.once.Do(func() { close(b.read) })
	}
	return n, err
}

func TestQdrantQueryCanceledBodyKeepsPartialBytesAndOriginalCancellationCause(t *testing.T) {
	part := []byte{0, 0xff, 1, 2, 3, 4}
	acked, read := make(chan struct{}), make(chan struct{})
	server := sessionServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", "10000")
		w.WriteHeader(200)
		w.Write(part)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(acked)
	})
	s := testSession(t, connections.QdrantConfig{Endpoint: server.URL}, "")
	lease, _ := s.ReserveQuery(1)
	l := lease.(*queryLease)
	if err := l.Preconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	base := l.client.http.Transport
	once := &sync.Once{}
	l.client.http.Transport = sessionRoundTrip(func(r *http.Request) (*http.Response, error) {
		response, err := base.RoundTrip(r)
		if err == nil && r.URL.Path == "/held-binary" {
			response.Body = &observedBody{ReadCloser: response.Body, once: once, read: read}
		}
		return response, err
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type outcome struct {
		result contract.SQLResult
		err    error
	}
	settled := make(chan outcome, 1)
	go func() { result, err := l.Execute(ctx, "", "GET /held-binary"); settled <- outcome{result, err} }()
	waitSessionSignal(t, read)
	cancel()
	waitSessionSignal(t, acked)
	select {
	case o := <-settled:
		if !errors.Is(o.err, context.Canceled) || !errors.Is(o.err, contract.ErrSQLSessionLost) || strings.Contains(o.err.Error(), "MySQL") || o.result.Steps[0].State != "interrupted" || !o.result.Truncated || !bytes.Equal(o.result.Sets[0].Rows[0][1].Bytes, part) {
			t.Fatal("original body cancellation evidence", o.result, o.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled original body executor did not acknowledge")
	}
}

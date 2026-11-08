package couchdb

import (
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

func profileConfig(t *testing.T, cfg connections.CouchDBConfig) connections.Profile {
	t.Helper()
	cfg, err := connections.NormalizeCouchDBConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(cfg.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	p, err := connections.Defaults(connections.CouchDB)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.ParseUint(u.Port(), 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	p.ID, p.Host, p.Port = 71, u.Hostname(), uint16(port)
	p.Name = "owned native session"
	p.Username = cfg.Username
	if u.Scheme == "http" {
		p.TLSMode = "disable"
	} else {
		p.TLSMode = "verify-full"
	}
	p.CouchDBOptions = connections.CouchDBOptions{Version: 1, Auth: cfg.Auth, ContextPath: u.Path, CAFile: cfg.CAFile, ServerName: cfg.ServerName}
	return p
}
func testSession(t *testing.T, cfg connections.CouchDBConfig, secret string) *Session {
	t.Helper()
	raw, err := OpenSession(context.Background(), profileConfig(t, cfg), secret)
	if err != nil {
		t.Fatal(err)
	}
	s := raw.(*Session)
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func TestCouchDBQueryReleaseAndSessionCloseSettleOriginalExecutors(t *testing.T) {
	for _, closeSession := range []bool{false, true} {
		t.Run(strconv.FormatBool(closeSession), func(t *testing.T) {
			entered, acked := make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/" || r.URL.Path == "/_session" {
					if r.URL.Path == "/" {
						fmt.Fprint(w, mockIdentity)
					} else {
						fmt.Fprint(w, mockAnonymous)
					}
					return
				}
				if r.Method == "POST" {
					_, _ = io.Copy(io.Discard, r.Body)
					close(entered)
					<-r.Context().Done()
					close(acked)
					return
				}
				fmt.Fprint(w, `{}`)
			}))
			defer server.Close()
			cfg := connections.CouchDBConfig{Endpoint: server.URL}
			s := testSession(t, cfg, "")
			first, _ := s.ReserveQuery(1)
			other, _ := s.ReserveQuery(2)
			independent := testSession(t, cfg, "")
			fresh, _ := independent.ReserveQuery(1)
			done := make(chan error, 1)
			go func() { _, err := first.Execute(context.Background(), "", "POST /owned/update\n[]"); done <- err }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("executor never entered")
			}
			var wg sync.WaitGroup
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if closeSession {
						_ = s.Close()
					} else {
						first.Release()
					}
				}()
			}
			wg.Wait()
			select {
			case err := <-done:
				if !errors.Is(err, contract.ErrSQLSessionLost) || !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("executor not settled")
			}
			select {
			case <-acked:
			case <-time.After(5 * time.Second):
				t.Fatal("server cancellation not acknowledged")
			}
			if !closeSession {
				if _, err := other.Execute(context.Background(), "", "GET /owned/get"); err != nil {
					t.Fatal("independent lease closed", err)
				}
			}
			if _, err := fresh.Execute(context.Background(), "", "GET /owned/get"); err != nil {
				t.Fatal("other session closed", err)
			}
		})
	}
}

func sessionServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/":
			fmt.Fprint(w, mockIdentity)
		case "/_session":
			fmt.Fprint(w, mockAnonymous)
		default:
			handler(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

const designSource = `{"_id":"_design/shared","_rev":"1-native","views":{"猫":{"map":"function(doc) { emit(doc._id, doc.n); }","reduce":"_sum"}},"options":{"partitioned":false},"exact":9007199254740993}`

func TestCouchDBSessionQualifiedDatabasesAndCompleteNativeDefinitions(t *testing.T) {
	var calls atomic.Int32
	server := sessionServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Path {
		case "/_all_dbs":
			fmt.Fprint(w, `["other","owned","_users"]`)
		case "/owned", "/other":
			fmt.Fprintf(w, `{"db_name":%q,"doc_count":2,"update_seq":"opaque-native","instance_start_time":"0"}`, strings.TrimPrefix(r.URL.Path, "/"))
		case "/owned/_design_docs", "/other/_design_docs":
			fmt.Fprint(w, `{"rows":[{"id":"_design/shared","key":"_design/shared","doc":`+designSource+`}]}`)
		case "/owned/_design/shared", "/other/_design/shared":
			fmt.Fprint(w, designSource)
		default:
			w.WriteHeader(404)
		}
	})
	s := testSession(t, connections.CouchDBConfig{Endpoint: server.URL}, "")
	ns, err := s.Namespaces(context.Background())
	if err != nil || len(ns) != 3 || ns[2].Ref != s.Namespace("owned") {
		t.Fatal(ns, err)
	}
	var objects []contract.DBObject
	for _, name := range []string{"owned", "other"} {
		list, err := s.Browse(context.Background(), s.Namespace(name))
		if err != nil || len(list) != 2 {
			t.Fatal(list, err)
		}
		objects = append(objects, list[1])
		m, err := s.TableMetadata(context.Background(), list[1].Ref)
		if err != nil || m.Editable || len(m.Columns) != 0 || m.Definition != designSource || len(m.Definitions) != 2 || m.Definitions[1].Name != "猫" {
			t.Fatal(m, err)
		}
	}
	if objects[0].Ref == objects[1].Ref {
		t.Fatal("same-name objects aliased across databases")
	}
	before := calls.Load()
	for _, n := range []contract.NamespaceRef{{ProfileID: 72, Catalog: "owned"}, {ProfileID: 71, Catalog: "../escape"}, {ProfileID: 71, Catalog: "owned", Schema: "sql"}} {
		if _, err = s.Browse(context.Background(), n); !errors.Is(err, connections.ErrInvalid) {
			t.Fatal(err)
		}
	}
	if _, err = s.TableMetadata(context.Background(), s.Namespace("owned").Object("table", "shared")); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal(err)
	}
	if calls.Load() != before {
		t.Fatal("invalid identity dispatched")
	}
	c := s.Capabilities()
	if !c.Query || !c.Objects || !c.Metadata || c.Documents || c.EditableTables {
		t.Fatal(c)
	}
}
func TestCouchDBMetadataPermissionsMalformedResponsesAndBounds(t *testing.T) {
	for _, raw := range []string{`null`, `["owned","owned"]`, `["../other"]`, `["owned",null]`, strings.Repeat(" ", responseLimit) + `[]`} {
		server := sessionServer(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, raw) })
		s := testSession(t, connections.CouchDBConfig{Endpoint: server.URL}, "")
		if _, err := s.Namespaces(context.Background()); err == nil {
			t.Fatal("malformed/oversize metadata accepted")
		}
	}
	server := sessionServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		fmt.Fprint(w, `{"error":"forbidden"}`)
	})
	s := testSession(t, connections.CouchDBConfig{Endpoint: server.URL}, "")
	ns, err := s.Namespaces(context.Background())
	var denied *HTTPError
	if !errors.As(err, &denied) || denied.Status != 403 || ns != nil {
		t.Fatal("permission failure hidden", ns, err)
	}
}
func TestCouchDBDesignPaginationDoesNotSkipDeletedBoundary(t *testing.T) {
	var pages atomic.Int32
	server := sessionServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/owned" {
			fmt.Fprint(w, `{"db_name":"owned","doc_count":300}`)
			return
		}
		page := pages.Add(1)
		start, end := 0, 257
		if page == 2 {
			if r.URL.Query().Get("startkey") != `"_design/d0255"` || r.URL.Query().Has("skip") {
				t.Error("unsafe pagination boundary", r.URL.RawQuery)
			}
			// Original last row deleted before the next read: startkey is inclusive,
			// but skipping an unconditional row would omit d0256.
			start, end = 256, 300
		}
		fmt.Fprint(w, `{"rows":[`)
		for i := start; i < end; i++ {
			if i > start {
				fmt.Fprint(w, ",")
			}
			id := fmt.Sprintf("_design/d%04d", i)
			fmt.Fprintf(w, `{"id":%q,"key":%q,"doc":{"_id":%q,"_rev":"1-native"}}`, id, id, id)
		}
		fmt.Fprint(w, `]}`)
	})
	s := testSession(t, connections.CouchDBConfig{Endpoint: server.URL}, "")
	list, err := s.Browse(context.Background(), s.Namespace("owned"))
	if err != nil || len(list) != 301 || list[257].Name != "_design/d0256" || pages.Load() != 2 {
		t.Fatal(len(list), err, pages.Load())
	}
}
func TestCouchDBSessionNodePrincipalReplacementRevokesOriginalLeases(t *testing.T) {
	for _, change := range []string{"node", "principal", "roles", "denied"} {
		t.Run(change, func(t *testing.T) {
			var changed atomic.Bool
			var dispatched atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/" {
					raw := mockIdentity
					if changed.Load() && change == "node" {
						raw = strings.ReplaceAll(raw, "0123456789abcdef0123456789abcdef", "abcdef0123456789abcdef0123456789")
					}
					fmt.Fprint(w, raw)
					return
				}
				if r.URL.Path == "/_session" {
					raw := mockAnonymous
					if changed.Load() {
						switch change {
						case "principal":
							raw = strings.Replace(raw, "null", `"new-user"`, 1)
						case "roles":
							raw = strings.Replace(raw, "[]", `["new-role"]`, 1)
						case "denied":
							w.WriteHeader(401)
						}
					}
					fmt.Fprint(w, raw)
					return
				}
				dispatched.Add(1)
				fmt.Fprint(w, `{}`)
			}))
			defer server.Close()
			s := testSession(t, connections.CouchDBConfig{Endpoint: server.URL}, "")
			first, _ := s.ReserveQuery(1)
			second, _ := s.ReserveQuery(2)
			if err := first.Preconnect(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := second.Preconnect(context.Background()); err != nil {
				t.Fatal(err)
			}
			changed.Store(true)
			if _, err := first.Execute(context.Background(), "", "PUT /owned/doc\n{}"); !errors.Is(err, contract.ErrSQLSessionLost) {
				t.Fatal(err)
			}
			if _, err := second.Execute(context.Background(), "", "GET /owned/doc"); !errors.Is(err, contract.ErrRevoked) {
				t.Fatal(err)
			}
			if _, err := first.Replace(); !errors.Is(err, contract.ErrRevoked) {
				t.Fatal(err)
			}
			if dispatched.Load() != 0 {
				t.Fatal("old leases dispatched on replaced observation")
			}
		})
	}
}
func TestCouchDBQueryUnknownAndMixedReceiptsAndExplicitRecovery(t *testing.T) {
	var writes atomic.Int32
	server := sessionServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"native-etag"`)
		if r.Method == "PUT" {
			writes.Add(1)
			w.WriteHeader(409)
			fmt.Fprint(w, `{"error":"conflict","exact":9007199254740993}`)
			return
		}
		if r.Method == "POST" {
			writes.Add(1)
			w.WriteHeader(201)
			fmt.Fprint(w, `[{"id":"a","rev":"1-ok"},{"id":"b","error":"conflict"}]`)
			return
		}
		fmt.Fprint(w, `{"n":9007199254740993,"decimal":1.2300e-20}`)
	})
	s := testSession(t, connections.CouchDBConfig{Endpoint: server.URL}, "")
	l, _ := s.ReserveQuery(1)
	result, err := l.Execute(context.Background(), "owned", "PUT /owned/doc\n{}")
	if !errors.Is(err, contract.ErrSQLSessionLost) || result.Steps[0].State != "uncertain" || !strings.Contains(result.Sets[0].Rows[0][1].Text, "9007199254740993") || result.Sets[0].Rows[0][2].Text != `"native-etag"` {
		t.Fatal(result, err)
	}
	if _, err = l.Execute(context.Background(), "owned", "PUT /owned/doc\n{}"); !errors.Is(err, contract.ErrSQLSessionLost) || writes.Load() != 1 {
		t.Fatal("replayed", err)
	}
	fresh, err := l.Replace()
	if err != nil || writes.Load() != 1 {
		t.Fatal("replace replayed", err)
	}
	if err = fresh.Preconnect(context.Background()); err != nil || writes.Load() != 1 {
		t.Fatal("preconnect replayed", err)
	}
	result, err = fresh.Execute(context.Background(), "owned", "POST /owned/_bulk_docs\n{\"docs\":[]}")
	if err != nil || result.Sets[0].Rows[0][0].Text != "201" || !strings.Contains(result.Sets[0].Rows[0][1].Text, "conflict") || result.AffectedRows != 0 {
		t.Fatal("mixed receipts inferred SQL writes", result, err)
	}
	result, err = fresh.ExecuteStream(context.Background(), "owned", "GET /owned/doc", func(_ int, cols []contract.Column, cells []contract.Cell) error {
		cols[0].Name = "mutated"
		cells[1].Text = "mutated"
		return nil
	})
	if err != nil || result.Sets[0].Columns[0].Name != "HTTP status" || !strings.Contains(result.Sets[0].Rows[0][1].Text, "1.2300e-20") {
		t.Fatal(result, err)
	}
	before := writes.Load()
	if _, err = fresh.Execute(context.Background(), "../other", "PUT /owned/doc\n{}"); err == nil || writes.Load() != before {
		t.Fatal("invalid selected namespace dispatched")
	}
}

func TestCouchDBMetadataRevokesLatePublicationAndCloseSettlesOriginalRead(t *testing.T) {
	for _, mode := range []string{"replace", "close"} {
		t.Run(mode, func(t *testing.T) {
			var changed atomic.Bool
			entered, acked := make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/":
					raw := mockIdentity
					if changed.Load() {
						raw = strings.ReplaceAll(raw, "0123456789abcdef0123456789abcdef", "abcdef0123456789abcdef0123456789")
					}
					fmt.Fprint(w, raw)
				case "/_session":
					fmt.Fprint(w, mockAnonymous)
				case "/_all_dbs":
					if mode == "replace" {
						changed.Store(true)
						fmt.Fprint(w, `["owned"]`)
						return
					}
					close(entered)
					<-r.Context().Done()
					close(acked)
				}
			}))
			defer server.Close()
			s := testSession(t, connections.CouchDBConfig{Endpoint: server.URL}, "")
			if mode == "replace" {
				result, err := s.Namespaces(context.Background())
				if result != nil || !errors.Is(err, contract.ErrRevoked) || s.Alive() {
					t.Fatal("late replaced metadata published", result, err)
				}
				return
			}
			done := make(chan error, 1)
			go func() {
				result, err := s.Namespaces(context.Background())
				if result != nil {
					t.Error("closed metadata published")
				}
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("metadata never entered")
			}
			_ = s.Close()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("metadata not settled")
			}
			select {
			case <-acked:
			case <-time.After(5 * time.Second):
				t.Fatal("read worker not acknowledged")
			}
		})
	}
}
func TestCouchDBQueryReservationHasNoIOAndReleasesBoundedSlots(t *testing.T) {
	var calls atomic.Int32
	server := sessionServer(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, `{}`) })
	s := testSession(t, connections.CouchDBConfig{Endpoint: server.URL}, "")
	if _, err := s.ReserveQuery(0); !errors.Is(err, contract.ErrSQLSessionLimit) {
		t.Fatal(err)
	}
	leases := make([]contract.QueryLease, 0, 32)
	for i := uint64(1); i <= 32; i++ {
		l, err := s.ReserveQuery(i)
		if err != nil {
			t.Fatal(err)
		}
		leases = append(leases, l)
	}
	if _, err := s.ReserveQuery(33); !errors.Is(err, contract.ErrSQLSessionLimit) {
		t.Fatal(err)
	}
	leases[0].Release()
	fresh, err := s.ReserveQuery(33)
	if err != nil {
		t.Fatal(err)
	}
	if err = fresh.Preconnect(context.Background()); err != nil || calls.Load() != 0 {
		t.Fatal("reservation or preconnect dispatched user requests", err)
	}
	_ = s.Close()
	if _, err = s.ReserveQuery(34); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal(err)
	}
}

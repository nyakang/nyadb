package couchdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func nativeSession(t *testing.T, name string) *Session {
	t.Helper()
	cfg, secret := nativeFixture(t, name)
	return testSession(t, cfg, secret)
}
func TestCouchDBNativeSessionMetadataPermissionsAndQualifiedDefinitions(t *testing.T) {
	admin := nativeOpen(t, "NYADB_COUCHDB_ADMIN_CONFIG")
	s := nativeSession(t, "NYADB_COUCHDB_ADMIN_CONFIG")
	names := []string{nativeID(), nativeID()}
	for _, name := range names {
		nativeRun(t, admin, Request{Method: "PUT", Path: "/" + name})
		t.Cleanup(func() { nativeRun(t, admin, Request{Method: "DELETE", Path: "/" + name}) })
		nativeRun(t, admin, Request{Method: "PUT", Path: "/" + name + "/_security", Body: []byte(`{"admins":{"names":[],"roles":[]},"members":{"names":[],"roles":[]}}`)})
		nativeRun(t, admin, Request{Method: "PUT", Path: "/" + name + "/_design/shared", Body: []byte(`{"_id":"_design/shared","views":{"exact":{"map":"function(doc) { if (doc.n) emit(doc._id, doc.n); }","reduce":"_sum"}},"exact":9007199254740993}`)})
	}
	ns, err := s.Namespaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, n := range ns {
		if n.Ref.ProfileID != s.ProfileID() || n.Ref.Schema != "" || n.Ref.Catalog != n.Label {
			t.Fatal("native qualified namespace changed", n)
		}
		found[n.Label] = true
	}
	var refs []contract.ObjectRef
	for _, name := range names {
		if !found[name] {
			t.Fatal("owned database absent")
		}
		objects, err := s.Browse(context.Background(), s.Namespace(name))
		if err != nil || len(objects) != 2 {
			t.Fatal(objects, err)
		}
		refs = append(refs, objects[1].Ref)
		m, err := s.TableMetadata(context.Background(), objects[1].Ref)
		raw := nativeRun(t, admin, Request{Method: "GET", Path: "/" + name + "/_design/shared"}).Body
		if err != nil || m.Editable || len(m.Columns) != 0 || m.Definition != string(raw) || len(m.Definitions) != 2 || !strings.Contains(m.Definition, "9007199254740993") {
			t.Fatal("native design definition changed", m, err)
		}
	}
	if refs[0] == refs[1] {
		t.Fatal("same-name design identity aliased")
	}
	regular := nativeSession(t, "NYADB_COUCHDB_PADDED_CONFIG")
	denied, err := regular.Namespaces(context.Background())
	var httpError *HTTPError
	if !errors.As(err, &httpError) || httpError.Status != 401 || denied != nil {
		t.Fatal("default admin_only_all_dbs failure hidden", denied, err)
	}
	t.Log("native default admin_only_all_dbs: regular principal GET /_all_dbs = 401; explicitly granted database browse succeeds")
	if list, err := regular.Browse(context.Background(), regular.Namespace(names[0])); err != nil || len(list) != 2 {
		t.Fatal("granted database browse failed", list, err)
	}
}
func TestCouchDBNativeSessionRESTReceiptsAndRecoveryWithoutReplay(t *testing.T) {
	s := nativeSession(t, "NYADB_COUCHDB_ADMIN_CONFIG")
	l, err := s.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	id := nativeID()
	source := fmt.Sprintf("PUT /nyadb_fixture/%s\n{\"_id\":%q,\"n\":9007199254740993}", id, id)
	result, err := l.Execute(context.Background(), "nyadb_fixture", source)
	if err != nil || result.Sets[0].Rows[0][0].Text != "201" {
		t.Fatal(result, err)
	}
	result, err = l.Execute(context.Background(), "nyadb_fixture", source)
	if !errors.Is(err, contract.ErrSQLSessionLost) || result.Steps[0].State != "uncertain" || !strings.Contains(result.Sets[0].Rows[0][1].Text, "conflict") {
		t.Fatal(result, err)
	}
	fresh, err := l.Replace()
	if err != nil {
		t.Fatal(err)
	}
	if err = fresh.Preconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err = fresh.Execute(context.Background(), "nyadb_fixture", "GET /nyadb_fixture/"+id)
	if err != nil || !strings.Contains(result.Sets[0].Rows[0][1].Text, "9007199254740993") || result.Sets[0].Rows[0][2].Text == "" {
		t.Fatal("native exact response or ETag lost", result, err)
	}
	var doc map[string]json.RawMessage
	if decodeJSON([]byte(result.Sets[0].Rows[0][1].Text), &doc) != nil || strings.HasPrefix(string(doc["_rev"]), `"2-`) {
		t.Fatal("recovery replayed original write")
	}
	bulk := fmt.Sprintf("POST /nyadb_fixture/_bulk_docs\n{\"docs\":[{\"_id\":%q,\"n\":0},{\"_id\":%q,\"n\":9007199254740993}]}", id, nativeID())
	result, err = fresh.Execute(context.Background(), "nyadb_fixture", bulk)
	if err != nil || result.Sets[0].Rows[0][0].Text != "201" || !strings.Contains(result.Sets[0].Rows[0][1].Text, "conflict") || !strings.Contains(result.Sets[0].Rows[0][1].Text, "rev") || result.AffectedRows != 0 {
		t.Fatal("native partial bulk lost or inferred SQL count", result, err)
	}
}
func TestCouchDBNativeSessionLostReceiptCancellationAndOriginalClose(t *testing.T) {
	for _, mode := range []string{"lost", "release", "close"} {
		t.Run(mode, func(t *testing.T) {
			admin := nativeOpen(t, "NYADB_COUCHDB_ADMIN_CONFIG")
			var writes atomic.Int32
			entered, acked := make(chan struct{}), make(chan struct{})
			relay := nativeRelay(t, admin, func(w http.ResponseWriter, r *http.Request, response Response) bool {
				if r.Method != "PUT" {
					return false
				}
				writes.Add(1)
				if response.Status != 201 {
					t.Error("original write did not commit")
				}
				close(entered)
				if mode == "lost" {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
					} else {
						_ = conn.Close()
					}
					close(acked)
					return true
				}
				select {
				case <-r.Context().Done():
				case <-time.After(10 * time.Second):
					t.Error("original cancellation unacknowledged")
				}
				close(acked)
				return true
			})
			s := testSession(t, relay.config, relay.secret)
			l, _ := s.ReserveQuery(1)
			independent := nativeSession(t, "NYADB_COUCHDB_ADMIN_CONFIG")
			other, _ := independent.ReserveQuery(1)
			id := nativeID()
			done := make(chan error, 1)
			go func() {
				result, err := l.Execute(context.Background(), "nyadb_fixture", fmt.Sprintf("PUT /nyadb_fixture/%s\n{\"_id\":%q,\"value\":9}", id, id))
				if len(result.Steps) != 1 || result.Steps[0].State != "uncertain" {
					t.Error("native uncertain outcome lost", result)
				}
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("native write never entered")
			}
			if mode != "lost" {
				var wg sync.WaitGroup
				for i := 0; i < 2; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						if mode == "close" {
							_ = s.Close()
						} else {
							l.Release()
						}
					}()
				}
				wg.Wait()
			}
			select {
			case err := <-done:
				if !errors.Is(err, contract.ErrSQLSessionLost) {
					t.Fatal(err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("original executor not settled")
			}
			select {
			case <-acked:
			case <-time.After(10 * time.Second):
				t.Fatal("relay not settled")
			}
			result, err := other.Execute(context.Background(), "nyadb_fixture", "GET /nyadb_fixture/"+id)
			if err != nil || !strings.Contains(result.Sets[0].Rows[0][1].Text, `"value":9`) {
				t.Fatal("independent original session damaged", result, err)
			}
			if writes.Load() != 1 {
				t.Fatal("original write replayed")
			}
			if mode != "close" {
				fresh, err := l.Replace()
				if err != nil {
					t.Fatal(err)
				}
				if err = fresh.Preconnect(context.Background()); err != nil || writes.Load() != 1 {
					t.Fatal("recovery replayed", err)
				}
			}
		})
	}
}
func TestCouchDBNativeSessionRejectsChangedNodeAndPrincipalBeforeDispatch(t *testing.T) {
	for _, mode := range []string{"node", "principal"} {
		t.Run(mode, func(t *testing.T) {
			admin := nativeOpen(t, "NYADB_COUCHDB_ADMIN_CONFIG")
			var changed atomic.Bool
			var writes atomic.Int32
			relay := nativeRelay(t, admin, func(w http.ResponseWriter, r *http.Request, response Response) bool {
				if r.Method == "PUT" {
					writes.Add(1)
				}
				if !changed.Load() {
					return false
				}
				body := string(response.Body)
				if mode == "node" && r.URL.Path == "/proxy/couchdb/" {
					body = strings.Replace(body, admin.Identity().UUID, "abcdef0123456789abcdef0123456789", 1)
				} else if mode == "principal" && r.URL.Path == "/proxy/couchdb/_session" {
					body = strings.Replace(body, `"native-admin"`, `"different-principal"`, 1)
				} else {
					return false
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(response.Status)
				fmt.Fprint(w, body)
				return true
			})
			s := testSession(t, relay.config, relay.secret)
			first, _ := s.ReserveQuery(1)
			second, _ := s.ReserveQuery(2)
			if err := first.Preconnect(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := second.Preconnect(context.Background()); err != nil {
				t.Fatal(err)
			}
			changed.Store(true)
			if _, err := first.Execute(context.Background(), "nyadb_fixture", "PUT /nyadb_fixture/never_dispatched\n{}"); !errors.Is(err, contract.ErrSQLSessionLost) {
				t.Fatal(err)
			}
			if _, err := second.Execute(context.Background(), "nyadb_fixture", "GET /nyadb_fixture"); !errors.Is(err, contract.ErrRevoked) {
				t.Fatal(err)
			}
			if _, err := first.Replace(); !errors.Is(err, contract.ErrRevoked) {
				t.Fatal(err)
			}
			if writes.Load() != 0 {
				t.Fatal("old session dispatched after changed native observation")
			}
			if _, err := nativeSession(t, "NYADB_COUCHDB_ADMIN_CONFIG").Namespaces(context.Background()); err != nil {
				t.Fatal("replacement damaged original endpoint", err)
			}
		})
	}
}

package qdrant

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func nativeSession(t *testing.T) (*Session, connections.QdrantConfig, string) {
	t.Helper()
	if os.Getenv("NYADB_QDRANT_SESSION_IT") != "1" {
		t.Skip("requires owned Qdrant session fixture")
	}
	cfg, secret, _ := nativeFixture(t)
	return testSession(t, cfg, secret), cfg, secret
}

func TestQdrantNativeSessionMetadataAndDefinitions(t *testing.T) {
	s, _, _ := nativeSession(t)
	c := nativeOpen(t)
	name := nativeCollection(t, c, `{"vectors":{"dense":{"size":2,"distance":"Dot"}},"sparse_vectors":{"sparse":{}}}`)
	alias := name + "_alias"
	nativeRun(t, c, Request{Method: "POST", Path: "/collections/aliases", Body: []byte(`{"actions":[{"create_alias":{"collection_name":"` + name + `","alias_name":"` + alias + `"}}]}`)})
	names, err := s.Namespaces(context.Background())
	if err != nil || len(names) == 0 {
		t.Fatal("native namespaces", err)
	}
	found := false
	for _, n := range names {
		if n.Ref.ProfileID != s.ProfileID() || n.Ref.Schema != "" || n.Label != n.Ref.Catalog || n.Ref.Catalog == alias {
			t.Fatal("native alias or label became qualified physical identity")
		}
		found = found || n.Ref.Catalog == name
	}
	if !found {
		t.Fatal("original native collection omitted")
	}
	objects, err := s.Browse(context.Background(), s.Namespace(name))
	if err != nil || len(objects) != 3 || objects[0].Ref != s.Namespace(name).Object("collection", name) {
		t.Fatal("native qualified objects", err)
	}
	for _, object := range objects {
		structure, err := s.TableMetadata(context.Background(), object.Ref)
		if err != nil || structure.Object != object.Ref || len(structure.Definitions) != 1 || structure.Definitions[0].SQL != structure.Definition {
			t.Fatal("complete native definition", err)
		}
		if object.Ref.Kind == "aliases" {
			if !strings.Contains(structure.Definition, alias) || !validCollectionAliases([]byte(structure.Definition), name) {
				t.Fatal("original alias relationship lost")
			}
		} else if !strings.Contains(structure.Definition, `"sparse_vectors"`) || !strings.Contains(structure.Definition, `"dense"`) {
			t.Fatal("named/sparse configuration omitted")
		}
	}
	if _, err := s.Browse(context.Background(), s.Namespace(alias)); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("alias accepted as original physical collection", err)
	}
	wrong := s.Namespace(name).Object("collection", name)
	wrong.ProfileID++
	if _, err := s.TableMetadata(context.Background(), wrong); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("foreign profile metadata admitted", err)
	}
}

func TestQdrantNativeSessionRequestOutcomesAndExactResponse(t *testing.T) {
	s, cfg, _ := nativeSession(t)
	c := nativeOpen(t)
	name := nativeCollection(t, c, `{"vectors":{"size":2,"distance":"Dot"}}`)
	l, _ := s.ReserveQuery(1)
	if err := l.Preconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	body := `{"points":[{"id":9007199254740993,"vector":[1,0],"payload":{"integer":9007199254740993,"decimal":1.2300e-20,"text":"会话🐱"}}]}`
	for _, wait := range []bool{false, true} {
		result, err := l.Execute(context.Background(), name, "PUT /collections/"+name+"/points?wait="+strconv.FormatBool(wait)+"\n"+body)
		want := contract.StatementEnqueued
		if wait {
			want = "completed"
		}
		if err != nil || len(result.Steps) != 1 || result.Steps[0].State != want || result.AffectedRows != 0 || result.Sets[0].Rows[0][0].Text != "200" || result.Sets[0].Rows[0][1].Kind != contract.JSON || result.Truncated {
			t.Fatal("HTTP success confused admission/completion", result, err)
		}
	}
	result, err := l.Execute(context.Background(), name, "POST /collections/"+name+`/points/query
{"query":[1,0],"with_payload":true,"with_vector":true,"limit":1}`)
	if err != nil || !strings.Contains(result.Sets[0].Rows[0][1].Text, "9007199254740993") || !strings.Contains(result.Sets[0].Rows[0][1].Text, "会话🐱") || result.Steps[0].State != "completed" {
		t.Fatal("native response lost exact point/payload", err)
	}
	result, err = l.Execute(context.Background(), "", "GET /collections/owned_absent_"+name)
	if err == nil || errors.Is(err, contract.ErrSQLSessionLost) || result.Steps[0].State != "failed" || result.Sets[0].Rows[0][0].Text != "404" || result.Truncated {
		t.Fatal("native completed read failure changed lease", err)
	}
	if _, err := l.Execute(context.Background(), "", "GET /"); err != nil {
		t.Fatal("read rejection retired original lease", err)
	}
	if cfg.Auth == "api_key" {
		_, _, readonly := nativeFixture(t)
		ro := testSession(t, cfg, readonly)
		read, _ := ro.ReserveQuery(1)
		result, err := read.Execute(context.Background(), name, "PUT /collections/"+name+"/points?wait=true\n"+body)
		if !errors.Is(err, contract.ErrSQLSessionLost) || result.Steps[0].State != "uncertain" || result.Sets[0].Rows[0][0].Text != "403" || result.Truncated || result.Sets[0].Rows[0][1].Truncated {
			t.Fatal("original forbidden write evidence/recovery", result, err)
		}
		other, _ := ro.ReserveQuery(2)
		if _, err := other.Execute(context.Background(), name, "GET /collections/"+name); err != nil {
			t.Fatal("write rejection revoked independent allowed read", err)
		}
	}
}

func TestQdrantNativeSessionLostResponseExplicitLeaseRecovery(t *testing.T) {
	_, _, secret := nativeSession(t)
	c := nativeOpen(t)
	name := nativeCollection(t, c, `{"vectors":{"size":2,"distance":"Dot"}}`)
	entered := make(chan struct{})
	cfg, calls := nativeFaultProxy(t, c, func(r *http.Request) bool { return r.Method == "PUT" && r.URL.Path == "/collections/"+name+"/points" }, func(w http.ResponseWriter, r *http.Request, response Response) {
		close(entered)
		connection, buffer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer connection.Close()
		fmt.Fprint(buffer, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 99999\r\n\r\n")
		buffer.Write(response.Body[:len(response.Body)/2])
		buffer.Flush()
	})
	s := testSession(t, cfg, secret)
	l, _ := s.ReserveQuery(1)
	result, err := l.Execute(context.Background(), name, "PUT /collections/"+name+`/points?wait=true
{"points":[{"id":9007199254740993,"vector":[1,0],"payload":{"original":true}}]}`)
	waitSessionSignal(t, entered)
	if !errors.Is(err, contract.ErrSQLSessionLost) || len(result.Steps) != 1 || result.Steps[0].State != "uncertain" || !result.Truncated || !result.Sets[0].Rows[0][1].Truncated || result.Sets[0].Rows[0][1].Text == "" {
		t.Fatal("original partial receipt lost", result, err)
	}
	if _, err := l.Execute(context.Background(), "", "GET /"); !errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatal("lost lease silently reconnected", err)
	}
	replacement, err := l.Replace()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := replacement.Execute(context.Background(), "", "GET /"); err != nil {
		t.Fatal("explicit lease recovery", err)
	}
	count, _, err := c.Count(context.Background(), name, nil)
	if err != nil || count != 1 || calls.Load() != 1 {
		t.Fatal("original write replayed or interpreted as rollback", err)
	}
}

func TestQdrantNativeSessionCanceledOriginalLeaseAndSessionClose(t *testing.T) {
	_, _, secret := nativeSession(t)
	for _, write := range []bool{false, true} {
		for _, action := range []string{"cancel", "release", "close"} {
			t.Run(strconv.FormatBool(write)+"/"+action, func(t *testing.T) {
				c := nativeOpen(t)
				name := nativeCollection(t, c, `{"vectors":{"size":2,"distance":"Dot"}}`)
				entered, acked := make(chan struct{}), make(chan struct{})
				cfg, calls := nativeFaultProxy(t, c, func(r *http.Request) bool {
					return write && r.Method == "PUT" && r.URL.Path == "/collections/"+name+"/points" || !write && r.Method == "GET" && r.URL.Path == "/collections/"+name
				}, func(w http.ResponseWriter, r *http.Request, response Response) {
					close(entered)
					<-r.Context().Done()
					close(acked)
				})
				s, fresh := testSession(t, cfg, secret), testSession(t, cfg, secret)
				first, _ := s.ReserveQuery(1)
				other, _ := s.ReserveQuery(2)
				independent, _ := fresh.ReserveQuery(1)
				if err := first.Preconnect(context.Background()); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				settled := make(chan error, 1)
				go func() {
					source := "GET /collections/" + name
					if write {
						source = "PUT /collections/" + name + "/points?wait=true\n" + `{"points":[{"id":1,"vector":[1,0]}]}`
					}
					result, err := first.Execute(ctx, name, source)
					state := "interrupted"
					if write {
						state = "uncertain"
					}
					if len(result.Steps) != 1 || result.Steps[0].State != state {
						settled <- errors.New("original canceled native outcome changed")
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
							if action == "close" {
								_ = s.Close()
							} else {
								first.Release()
							}
						}()
					}
					wg.Wait()
				}
				waitSessionSignal(t, acked)
				select {
				case err := <-settled:
					if !errors.Is(err, context.Canceled) || !errors.Is(err, contract.ErrSQLSessionLost) {
						t.Fatal("original executor cancellation", err)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("owning native executor did not settle")
				}
				if action != "close" {
					if _, err := other.Execute(context.Background(), "", "GET /"); err != nil {
						t.Fatal("independent original lease retired", err)
					}
				}
				if _, err := independent.Execute(context.Background(), "", "GET /"); err != nil {
					t.Fatal("independent native session retired", err)
				}
				if action == "cancel" {
					l, err := first.Replace()
					if err != nil {
						t.Fatal(err)
					}
					if _, err := l.Execute(context.Background(), "", "GET /"); err != nil {
						t.Fatal(err)
					}
				}
				if write {
					count, _, err := c.Count(context.Background(), name, nil)
					if err != nil || count != 1 {
						t.Fatal("native canceled write treated as rollback", err)
					}
				}
				if calls.Load() != 1 {
					t.Fatal("original native request replayed")
				}
			})
		}
	}
}

func TestQdrantNativeSessionBinaryObservationDriftRejectsDispatch(t *testing.T) {
	_, _, secret := nativeSession(t)
	c := nativeOpen(t)
	name := nativeCollection(t, c, `{"vectors":{"size":2,"distance":"Dot"}}`)
	var changed atomic.Bool
	var writes atomic.Int32
	cfg, _ := nativeFaultProxy(t, c, func(r *http.Request) bool {
		if r.Method == "PUT" {
			writes.Add(1)
		}
		return r.URL.Path == "/"
	}, func(w http.ResponseWriter, r *http.Request, response Response) {
		raw := string(response.Body)
		if changed.Load() {
			raw = strings.Replace(raw, `"1.20.0"`, `"1.20.1"`, 1)
		}
		w.WriteHeader(response.Status)
		fmt.Fprint(w, raw)
	})
	s := testSession(t, cfg, secret)
	l, _ := s.ReserveQuery(1)
	if err := l.Preconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	changed.Store(true)
	if _, err := l.Execute(context.Background(), name, "PUT /collections/"+name+"/points?wait=true\n"+`{"points":[{"id":1,"vector":[1,0]}]}`); !errors.Is(err, contract.ErrSQLSessionLost) || s.Alive() || writes.Load() != 0 {
		t.Fatal("observed binary drift allowed original dispatch", err)
	}
	if _, err := l.Replace(); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("revoked original session automatically recovered", err)
	}
	fresh := testSession(t, cfg, secret)
	if fresh.Identity().Version != "1.20.1" || c.Identity().Version != "1.20.0" {
		t.Fatal("relay observation confused with actual binary/incarnation")
	}
}

func TestQdrantNativeSessionNonJSONAndSinkFailureDoNotReplay(t *testing.T) {
	s, _, _ := nativeSession(t)
	c := nativeOpen(t)
	name := nativeCollection(t, c, `{"vectors":{"size":2,"distance":"Dot"}}`)
	l, _ := s.ReserveQuery(1)
	result, err := l.Execute(context.Background(), "", "GET /metrics")
	if err != nil || result.Sets[0].Rows[0][1].Kind != contract.Text || !strings.Contains(result.Sets[0].Rows[0][1].Text, "app_info") {
		t.Fatal("native non-JSON metrics response", err)
	}
	result, err = l.ExecuteStream(context.Background(), name, "PUT /collections/"+name+`/points?wait=true
{"points":[{"id":1,"vector":[1,0]}]}`, func(int, []contract.Column, []contract.Cell) error { return errors.New("presentation failure") })
	if err == nil || result.Steps[0].State != "completed" || result.AffectedRows != 0 || result.Truncated || result.Sets[0].Rows[0][1].Kind != contract.JSON {
		t.Fatal("sink failure rewrote completed native receipt", result, err)
	}
	if _, err := l.Execute(context.Background(), "", "GET /"); err != nil {
		t.Fatal("presentation failure lost settled native lease", err)
	}
	count, _, err := c.Count(context.Background(), name, nil)
	if err != nil || count != 1 {
		t.Fatal("presentation failure replayed native mutation", err)
	}
	result, err = l.Execute(context.Background(), name, "POST /collections/"+name+"/snapshots?wait=true")
	if err != nil || result.Steps[0].State != "completed" {
		t.Fatal("original snapshot creation receipt", err)
	}
	raw, err := NativeResult([]byte(result.Sets[0].Rows[0][1].Text))
	var snapshot struct{ Name string }
	if err != nil || decodeJSON(raw, &snapshot) != nil {
		t.Fatal("native snapshot description", err)
	}
	if _, err := collectionPath(snapshot.Name); err != nil {
		t.Fatal("native snapshot name")
	}
	path := "/collections/" + name + "/snapshots/" + url.PathEscape(snapshot.Name)
	original := nativeRun(t, c, Request{Method: "GET", Path: path})
	result, err = l.ExecuteStream(context.Background(), name, "GET "+path, func(_ int, columns []contract.Column, cells []contract.Cell) error {
		if cells[1].Kind != contract.Binary || !bytes.Equal(cells[1].Bytes, original.Body) || len(cells[1].Bytes) == 0 {
			return errors.New("native archive bytes lost")
		}
		cells[1].Bytes[0] ^= 0xff
		return nil
	})
	if err != nil || result.Truncated || result.Sets[0].Rows[0][1].Kind != contract.Binary || !bytes.Equal(result.Sets[0].Rows[0][1].Bytes, original.Body) {
		t.Fatal("original binary snapshot download changed", err)
	}
	asyncName := nativeCollection(t, c, `{"vectors":{"size":2,"distance":"Dot"}}`)
	result, err = l.Execute(context.Background(), asyncName, "POST /collections/"+asyncName+"/snapshots?wait=false")
	if err != nil || result.Steps[0].State != contract.StatementEnqueued || result.AffectedRows != 0 {
		t.Fatal("native asynchronous snapshot claimed completion", result, err)
	}
	if _, err := l.Execute(context.Background(), "", "GET /"); err != nil {
		t.Fatal("accepted native request retired original lease", err)
	}
	// Observe the server independently; the accepted request is never replayed.
	deadline := time.Now().Add(10 * time.Second)
	for {
		response := nativeRun(t, c, Request{Method: "GET", Path: "/collections/" + asyncName + "/snapshots"})
		raw, err := NativeResult(response.Body)
		var snapshots []struct{ Name string }
		if err != nil || decodeJSON(raw, &snapshots) != nil || snapshots == nil {
			t.Fatal("native snapshot observation", err)
		}
		if len(snapshots) != 0 {
			path := "/collections/" + asyncName + "/snapshots/" + url.PathEscape(snapshots[0].Name)
			response := nativeRun(t, c, Request{Method: "GET", Path: path})
			if len(response.Body) == 0 {
				t.Fatal("observed native background archive is empty")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native background snapshot did not become observable")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

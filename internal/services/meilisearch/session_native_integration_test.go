package meilisearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func nativeSession(t *testing.T) *Session {
	t.Helper()
	if os.Getenv("NYADB_MEILISEARCH_SESSION_IT") != "1" {
		t.Skip("requires owned native session fixture")
	}
	cfg, secret := nativeFixture(t)
	return testSession(t, cfg, secret)
}
func TestMeilisearchNativeSessionQualifiedPaginationAndDefinitions(t *testing.T) {
	s := nativeSession(t)
	c := nativeOpen(t)
	prefix := "owned_page_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	receipts := make([]Receipt, 257)
	for i := range receipts {
		uid := fmt.Sprintf("%s_%03d", prefix, i)
		receipts[i] = nativeSubmit(t, c, Request{Method: "POST", Path: "/indexes", Body: []byte(`{"uid":"` + uid + `","primaryKey":"id"}`)})
	}
	// Wait on every original receipt; admitting the last creation is not proof
	// that preceding tasks completed, or that the catalog is a snapshot.
	for _, r := range receipts {
		nativeWait(t, c, r)
	}
	ns, err := s.Namespaces(context.Background())
	if err != nil || len(ns) < 257 {
		t.Fatal("native complete index pagination", len(ns), err)
	}
	seen := map[string]bool{}
	for _, n := range ns {
		if n.Ref != s.Namespace(n.Label) || seen[n.Label] {
			t.Fatal("native namespace alias")
		}
		seen[n.Label] = true
	}
	for i := range receipts {
		if !seen[fmt.Sprintf("%s_%03d", prefix, i)] {
			t.Fatal("native index omitted beyond first page")
		}
	}
	var refs []contract.ObjectRef
	for _, i := range []int{0, 256} {
		uid := fmt.Sprintf("%s_%03d", prefix, i)
		nativeWait(t, c, nativeSubmit(t, c, Request{Method: "PATCH", Path: "/indexes/" + uid + "/settings", Body: []byte(`{"searchableAttributes":["text"],"filterableAttributes":["integer"],"sortableAttributes":["integer"],"stopWords":["原生"],"synonyms":{"猫":["cat"]}}`)}))
		list, err := s.Browse(context.Background(), s.Namespace(uid))
		if err != nil || len(list) != 4 || list[0].CreatedAt == "" || list[0].ModifiedAt == "" {
			t.Fatal("native qualified objects", err)
		}
		info, err := parseIndex([]byte(list[0].Definition), uid)
		if err != nil || info.PrimaryKey == nil || *info.PrimaryKey != "id" {
			t.Fatal("native complete primary key definition", err)
		}
		m, err := s.TableMetadata(context.Background(), list[2].Ref)
		if err != nil || m.Editable || len(m.Definitions) != 1 || m.Definitions[0].SQL != m.Definition {
			t.Fatal("native full readonly settings", err)
		}
		fields, err := nativeObject([]byte(m.Definition))
		if err != nil || !bytes.Contains(fields["synonyms"], []byte("猫")) || !bytes.Contains(fields["filterableAttributes"], []byte("integer")) || len(fields) < 10 {
			t.Fatal("native settings truncated or changed")
		}
		stats, err := s.TableMetadata(context.Background(), list[3].Ref)
		if err != nil || !strings.Contains(stats.Definition, `"numberOfDocuments":0`) {
			t.Fatal("native independent stats", err)
		}
		refs = append(refs, list[2].Ref)
	}
	if refs[0] == refs[1] {
		t.Fatal("native same-name settings aliased")
	}
	t.Logf("native %d indexes enumerated across bounded pages, with original qualified identity, full primary key/settings/stats", len(ns))
}
func TestMeilisearchNativeSessionRESTAdmissionFailureAndSynchronous(t *testing.T) {
	s := nativeSession(t)
	c := nativeOpen(t)
	uid := nativeIndex(t, c)
	l, err := s.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	if err = l.Preconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`[{"id":"one","integer":9007199254740993,"text":"原生🐱"}]`, `[{"other":"missing-id"}]`} {
		result, err := l.Execute(context.Background(), uid, "POST /indexes/"+uid+"/documents\n"+body)
		if err != nil || len(result.Steps) != 1 || result.Steps[0].State != contract.StatementEnqueued || result.AffectedRows != 0 || result.Sets[0].AffectedRows != 0 || result.Sets[0].Rows[0][0].Text != "202" {
			t.Fatal("native 202 falsely reported completion", err)
		}
		receipt, err := parseReceipt([]byte(result.Sets[0].Rows[0][1].Text))
		if err != nil || receipt.IndexUID == nil || *receipt.IndexUID != uid {
			t.Fatal("native original lease receipt lost", err)
		}
		task, err := c.WaitTask(context.Background(), receipt, 30*time.Second)
		if strings.Contains(body, "missing-id") {
			var failure *TaskFailure
			if !errors.As(err, &failure) || task.Status != "failed" || !bytes.Contains(task.Raw, []byte("missing_document_id")) {
				t.Fatal("native admitted failure hidden", err)
			}
		} else if err != nil || task.Status != "succeeded" {
			t.Fatal("native original task", err)
		}
		if !bytes.Equal(task.Receipt.Raw, receipt.Raw) {
			t.Fatal("native original receipt changed")
		}
	}
	result, err := l.Execute(context.Background(), uid, "GET /indexes/"+uid+"/documents/one")
	if err != nil || result.Steps[0].State != "completed" || !strings.Contains(result.Sets[0].Rows[0][1].Text, "9007199254740993") {
		t.Fatal("native synchronous document evidence", err)
	}
	cfg, _ := nativeFixture(t)
	if cfg.Auth == "bearer" {
		result, err = l.Execute(context.Background(), "", `POST /keys`+"\n"+`{"name":"owned-session-key","actions":["search"],"indexes":["`+uid+`"],"expiresAt":null}`)
		if err != nil || result.Steps[0].State != "completed" || result.Sets[0].Rows[0][0].Text != "201" || result.AffectedRows != 0 {
			t.Fatal("native synchronous key outcome", err)
		}
		fields, err := nativeObject([]byte(result.Sets[0].Rows[0][1].Text))
		keyUID, e := nativeString(fields["uid"])
		if err != nil || e != nil {
			t.Fatal("native key UID unavailable")
		}
		nativeRun(t, c, Request{Method: "DELETE", Path: "/keys/" + keyUID})
	}
	t.Log("native REST lease preserved 202 enqueued receipts, later failed task, exact integer and synchronous replies without affected-row claims")
}
func TestMeilisearchNativeSessionLostCancelReleaseCloseAndRecovery(t *testing.T) {
	_ = nativeSession(t)
	for _, action := range []string{"lost", "cancel", "release", "close"} {
		t.Run(action, func(t *testing.T) {
			upstream := nativeOpen(t)
			uid := nativeIndex(t, upstream)
			entered, acked := make(chan struct{}), make(chan struct{})
			accepted := make(chan Receipt, 1)
			var submissions atomic.Int32
			relay := nativeRelay(t, upstream, func(w http.ResponseWriter, r *http.Request, response Response) bool {
				if r.Method != "POST" {
					return false
				}
				submissions.Add(1)
				receipt, err := parseReceipt(response.Body)
				if err != nil || response.Status != 202 {
					t.Error("native relay did not capture admission")
				}
				accepted <- receipt
				close(entered)
				if action == "lost" {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
					} else {
						_ = conn.Close()
					}
				} else {
					<-r.Context().Done()
				}
				close(acked)
				return true
			})
			_, secret := nativeFixture(t)
			s := testSession(t, relay.config, secret)
			first, _ := s.ReserveQuery(1)
			other, _ := s.ReserveQuery(2)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			type outcome struct {
				result contract.SQLResult
				err    error
			}
			settled := make(chan outcome, 1)
			go func() {
				r, e := first.Execute(ctx, uid, "POST /indexes/"+uid+"/documents\n"+`[{"id":"original","integer":9007199254740993}]`)
				settled <- outcome{r, e}
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("native original request not admitted")
			}
			if action == "cancel" {
				cancel()
			} else if action != "lost" {
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
			select {
			case o := <-settled:
				if !errors.Is(o.err, contract.ErrSQLSessionLost) || o.result.Steps[0].State != "uncertain" {
					t.Fatal("native original unknown outcome not retained", o.err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("native executor not settled")
			}
			select {
			case <-acked:
			case <-time.After(5 * time.Second):
				t.Fatal("native relay executor not acknowledged")
			}
			if _, err := first.Execute(context.Background(), uid, "POST /indexes/"+uid+"/documents\n[]"); err == nil || submissions.Load() != 1 {
				t.Fatal("native original write replayed")
			}
			if action != "close" {
				if _, err := other.Execute(context.Background(), uid, "GET /indexes/"+uid); err != nil {
					t.Fatal("native unrelated lease canceled", err)
				}
			}
			replacement, err := first.Replace()
			if action == "close" {
				if !errors.Is(err, contract.ErrRevoked) {
					t.Fatal("replacement rebound closed original session", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if _, err = replacement.Execute(context.Background(), uid, "GET /indexes/"+uid); err != nil {
					t.Fatal("native explicit replacement", err)
				}
			}
			fresh := nativeOpen(t)
			nativeWait(t, fresh, <-accepted)
			doc := nativeRun(t, fresh, Request{Method: "GET", Path: "/indexes/" + uid + "/documents/original"})
			if !bytes.Contains(doc.Body, []byte("9007199254740993")) {
				t.Fatal("native admitted original document lost")
			}
			history, err := nativeObject(nativeRun(t, fresh, Request{Method: "GET", Path: "/tasks?indexUids=" + uid + "&types=documentAdditionOrUpdate"}).Body)
			total, e := nativeUint(history["total"], 64)
			if err != nil || e != nil || total != 1 || submissions.Load() != 1 {
				t.Fatal("native task history did not prove no replay")
			}
			independent := nativeSession(t)
			freshLease, _ := independent.ReserveQuery(1)
			if _, err := freshLease.Execute(context.Background(), uid, "GET /indexes/"+uid); err != nil {
				t.Fatal("native new original-profile session affected", err)
			}
		})
	}
}
func TestMeilisearchNativeSessionMetadataCloseAcknowledged(t *testing.T) {
	_ = nativeSession(t)
	upstream := nativeOpen(t)
	uid := nativeIndex(t, upstream)
	entered, acked := make(chan struct{}), make(chan struct{})
	relay := nativeRelay(t, upstream, func(w http.ResponseWriter, r *http.Request, response Response) bool {
		if !strings.HasSuffix(r.URL.Path, "/settings") {
			return false
		}
		close(entered)
		<-r.Context().Done()
		close(acked)
		return true
	})
	_, secret := nativeFixture(t)
	s := testSession(t, relay.config, secret)
	settled := make(chan error, 1)
	go func() {
		_, err := s.TableMetadata(context.Background(), s.Namespace(uid).Object("settings", "settings"))
		settled <- err
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("native settings read not entered")
	}
	_ = s.Close()
	select {
	case err := <-settled:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("native metadata executor not settled")
	}
	select {
	case <-acked:
	case <-time.After(5 * time.Second):
		t.Fatal("native metadata relay not acknowledged")
	}
	if _, err := nativeSession(t).TableMetadata(context.Background(), s.Namespace(uid).Object("settings", "settings")); err != nil {
		t.Fatal("native independent metadata affected", err)
	}
}
func TestMeilisearchNativeSessionScopedPermissionsAndRevocation(t *testing.T) {
	_ = nativeSession(t)
	master := nativeOpen(t)
	cfg, _ := nativeFixture(t)
	if cfg.Auth != "bearer" {
		t.Fatal("requires authenticated native scoped session")
	}
	uid := nativeIndex(t, master)
	other := nativeIndex(t, master)
	nativeWait(t, master, nativeSubmit(t, master, Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: []byte(`[{"id":"one"}]`)}))
	body, _ := json.Marshal(map[string]any{"name": "owned-session-readonly", "actions": []string{"indexes.get", "documents.get", "search", "tasks.get"}, "indexes": []string{uid}, "expiresAt": nil})
	f, err := nativeObject(nativeRun(t, master, Request{Method: "POST", Path: "/keys", Body: body}).Body)
	key, e1 := nativeString(f["key"])
	keyUID, e2 := nativeString(f["uid"])
	if err != nil || e1 != nil || e2 != nil {
		t.Fatal("native scoped key unavailable")
	}
	cfg.IndexUID = uid
	s := testSession(t, cfg, key)
	if s.Identity() != (Identity{}) {
		t.Fatal("scoped session invented binary observation")
	}
	ns, err := s.Namespaces(context.Background())
	if err != nil || len(ns) != 1 || ns[0].Ref != s.Namespace(uid) {
		t.Fatal("native scoped enumeration escaped", err)
	}
	list, err := s.Browse(context.Background(), ns[0].Ref)
	if err != nil || len(list) != 4 {
		t.Fatal("native scoped allowed index hidden", err)
	}
	for _, i := range []int{2, 3} {
		_, err = s.TableMetadata(context.Background(), list[i].Ref)
		var status *HTTPError
		if !errors.As(err, &status) || status.Status != 403 || !s.Alive() {
			t.Fatal("native denied settings/stats hidden or revoked allowed reads", err)
		}
	}
	l, _ := s.ReserveQuery(1)
	if _, err = l.Execute(context.Background(), uid, "GET /indexes/"+other); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("native configured scope escaped", err)
	}
	result, err := l.Execute(context.Background(), uid, "GET /indexes/"+uid+"/documents/one")
	if err != nil || !strings.Contains(result.Sets[0].Rows[0][1].Text, `"id":"one"`) {
		t.Fatal("native allowed documents revoked", err)
	}
	nativeRun(t, master, Request{Method: "DELETE", Path: "/keys/" + keyUID})
	_, err = l.Execute(context.Background(), uid, "GET /indexes/"+uid+"/documents/one")
	var status *HTTPError
	if !errors.As(err, &status) || status.Status != 403 {
		t.Fatal("native key revocation fell back to master", err)
	}
	t.Log("native single-index session retained unavailable binary version, explicit denied settings/stats, allowed document read and revoked-key failure without fallback")
}

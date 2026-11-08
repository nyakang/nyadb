package meilisearch

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func nativeNamespacePlan(t *testing.T, l contract.TaskDocumentLease, action string, draft contract.NamespaceDraft) contract.TaskMutationPlan {
	t.Helper()
	n := l.(contract.NamespaceManagementLease)
	before := []contract.NamespaceRecord{}
	for _, ref := range namespaceMutationRefs(&contract.NamespaceMutation{Draft: draft}) {
		r, e := n.ReadNamespace(context.Background(), ref)
		if e != nil {
			t.Fatal("native namespace observation", e)
		}
		before = append(before, r)
	}
	p, e := n.PrepareNamespaceMutation(action, draft, before)
	if e != nil {
		t.Fatal("native namespace preview", e)
	}
	return p
}
func nativeNamespaceChange(t *testing.T, l contract.TaskDocumentLease, action string, draft contract.NamespaceDraft) contract.TaskMutationResult {
	t.Helper()
	p := nativeNamespacePlan(t, l, action, draft)
	m := l.(contract.TaskManagementLease)
	r, e := m.MutateTasks(context.Background(), p)
	if e != nil || r.State != "enqueued" || r.NamespaceVerified || r.Receipt.Raw == "" {
		t.Fatal("native namespace admission", e, r)
	}
	if _, e = m.MutateTasks(context.Background(), p); !errors.Is(e, contract.ErrTaskMutationUsed) {
		t.Fatal("namespace replay", e)
	}
	r, e = m.ObserveTaskMutation(context.Background(), r.Receipt, 30*time.Second)
	if e != nil || r.State != "succeeded" || !r.NamespaceVerified {
		t.Fatal("native namespace completion/readback", e, r)
	}
	return r
}
func TestMeilisearchNativeNamespaceSixActionsContentVerificationAndRecovery(t *testing.T) {
	cfg, secret := nativeFixture(t)
	c := nativeOpen(t)
	s := testSession(t, cfg, secret)
	l, _ := nativeManagedTasks(t, s, "")
	uid := "owned_namespace_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	destination := uid + "_renamed"
	other := nativeIndex(t, c)
	nativeWait(t, c, nativeSubmit(t, c, Request{Method: "POST", Path: "/indexes/" + other + "/documents", Body: []byte(`[{"id":"other","text":"另一个🐱","n":9007199254740995}]`)}))
	nativeWait(t, c, nativeSubmit(t, c, Request{Method: "PATCH", Path: "/indexes/" + other + "/settings", Body: []byte(`{"stopWords":["other_namespace_owned"]}`)}))
	draft := contract.NamespaceDraft{Ref: s.Namespace(uid)}
	p := nativeNamespacePlan(t, l, "namespace_create", draft)
	admitted, e := l.(contract.TaskManagementLease).MutateTasks(context.Background(), p)
	if e != nil || admitted.State != "enqueued" {
		t.Fatal(e)
	}
	original, e := parseReceipt([]byte(admitted.Receipt.Raw))
	if e != nil {
		t.Fatal(e)
	}
	_ = s.Close()
	fresh := testSession(t, cfg, secret)
	recovery, e := fresh.RecoverTaskDocument(l.(*taskDocument).object, l)
	if e != nil || recovery.Namespace == nil || recovery.Receipt.Raw != admitted.Receipt.Raw {
		t.Fatal("original namespace recovery", e)
	}
	l = recovery.Lease
	defer l.Close()
	settled, e := l.(contract.TaskManagementLease).ObserveTaskMutation(context.Background(), recovery.Receipt, 30*time.Second)
	if e != nil || !settled.NamespaceVerified || settled.Namespaces[0].HasPrimaryKey {
		t.Fatal("create without primary key", e)
	}
	draft.PrimaryKey, draft.PrimaryKeySet = "id", true
	nativeNamespaceChange(t, l, "namespace_primary_key", draft)
	data := []byte(`[{"id":"original","text":"原始🐱","nested":{"n":9007199254740993}}]`)
	nativeWait(t, c, nativeSubmit(t, c, Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: data}))
	nativeWait(t, c, nativeSubmit(t, c, Request{Method: "PATCH", Path: "/indexes/" + uid + "/settings", Body: []byte(`{"stopWords":["original_namespace_owned"]}`)}))
	draft.PrimaryKey, draft.PrimaryKeySet = "", false
	draft.Destination = fresh.Namespace(destination)
	nativeNamespaceChange(t, l, "namespace_rename", draft)
	actual := nativeRun(t, c, Request{Method: "GET", Path: "/indexes/" + destination + "/documents/original"})
	if !strings.Contains(string(actual.Body), "9007199254740993") || !strings.Contains(string(actual.Body), "原始🐱") {
		t.Fatal("rename changed exact document")
	}
	draft.Ref, draft.Destination = fresh.Namespace(destination), fresh.Namespace(other)
	nativeNamespaceChange(t, l, "namespace_swap", draft)
	actual = nativeRun(t, c, Request{Method: "GET", Path: "/indexes/" + other + "/documents/original"})
	if !strings.Contains(string(actual.Body), "9007199254740993") {
		t.Fatal("swap did not move original contents")
	}
	actual = nativeRun(t, c, Request{Method: "GET", Path: "/indexes/" + destination + "/documents/other"})
	if !strings.Contains(string(actual.Body), "9007199254740995") {
		t.Fatal("swap did not move second contents")
	}
	for _, entry := range []struct{ uid, word string }{{other, "original_namespace_owned"}, {destination, "other_namespace_owned"}} {
		actual = nativeRun(t, c, Request{Method: "GET", Path: "/indexes/" + entry.uid + "/settings"})
		if !strings.Contains(string(actual.Body), entry.word) {
			t.Fatal("swap did not move native settings")
		}
	}
	// Native rename leaves its own receipt index unchanged; swap rewrites
	// historical tasks. Such old observations must stop rather than rebind.
	if _, e = c.ReadTask(context.Background(), original); !errors.Is(e, ErrTaskChanged) {
		t.Fatal("historical identity drift accepted", e)
	}
	c = nativeOpen(t)
	draft.Destination = contract.NamespaceRef{}
	nativeNamespaceChange(t, l, "namespace_clear", draft)
	nativeNamespaceChange(t, l, "namespace_delete", draft)
	actual = nativeRun(t, c, Request{Method: "GET", Path: "/indexes/" + other + "/documents/original"})
	if !strings.Contains(string(actual.Body), "9007199254740993") {
		t.Fatal("clear/drop changed sibling contents")
	}
	t.Log("six native HTTP202 index operations, full original observations, original session recovery, exact Unicode/large integer contents after rename and atomic swap, historical task identity drift rejected, clear and delete preserve sibling")
}
func TestMeilisearchNativeNamespaceLostCancelCloseNoReplay(t *testing.T) {
	for _, fault := range []string{"lost", "canceled", "closed"} {
		t.Run(fault, func(t *testing.T) {
			upstream := nativeOpen(t)
			cfg, secret := nativeFixture(t)
			uid := nativeIndex(t, upstream)
			entered, release := make(chan struct{}), make(chan struct{})
			var receipt Receipt
			var mu sync.Mutex
			var writes atomic.Int32
			c, proxy := mockOpen(t, func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				response, e := upstream.Do(context.Background(), Request{Method: r.Method, Path: strings.TrimPrefix(r.URL.RequestURI(), "/proxy/meili"), Body: body})
				if e != nil {
					w.WriteHeader(502)
					return
				}
				if r.Method == "DELETE" {
					writes.Add(1)
					parsed, e := parseReceipt(response.Body)
					if e != nil {
						t.Error(e)
					}
					mu.Lock()
					receipt = parsed
					mu.Unlock()
					close(entered)
					if fault != "lost" {
						<-release
					}
					conn, _, _ := w.(http.Hijacker).Hijack()
					conn.Close()
					return
				}
				w.WriteHeader(response.Status)
				w.Write(response.Body)
			})
			c.Close()
			proxied := cfg
			proxied.Endpoint = proxy.URL + "/proxy/meili"
			proxied.CAFile = ""
			proxied.ServerName = ""
			proxied.ClientCertFile = ""
			proxied.ClientKeyFile = ""
			s := testSession(t, proxied, secret)
			l, m := nativeManagedTasks(t, s, "")
			p := nativeNamespacePlan(t, l, "namespace_clear", contract.NamespaceDraft{Ref: s.Namespace(uid)})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type reply struct {
				r contract.TaskMutationResult
				e error
			}
			out := make(chan reply, 1)
			go func() { r, e := m.MutateTasks(ctx, p); out <- reply{r, e} }()
			<-entered
			if fault == "canceled" {
				cancel()
				close(release)
			}
			if fault == "closed" {
				done := make(chan struct{})
				go func() { s.Close(); close(done) }()
				close(release)
				<-done
			}
			result := <-out
			if result.e == nil || result.r.State != "uncertain" || result.r.Namespace == nil || writes.Load() != 1 {
				t.Fatal("lost namespace admission", result)
			}
			fresh := s
			if fault == "closed" {
				fresh = testSession(t, proxied, secret)
			}
			r, e := fresh.RecoverTaskDocument(s.Namespace("").Object("tasks", "tasks"), l)
			if e != nil || !r.Uncertain || r.Namespace == nil {
				t.Fatal("uncertain namespace recovery", e)
			}
			defer r.Lease.Close()
			if _, e = r.Lease.(contract.ServerBackupLease).PrepareServerBackup("dump"); !errors.Is(e, contract.ErrTaskMutationUsed) {
				t.Fatal("uncertain namespace permitted replay", e)
			}
			if _, e = fresh.ReserveDataDocument(fresh.Namespace(uid).Object("documents", "documents")); !errors.Is(e, contract.ErrNamespaceBusy) {
				t.Fatal("unknown clear reopened data", e)
			}
			mu.Lock()
			original := receipt
			mu.Unlock()
			nativeWait(t, upstream, original)
		})
	}
	t.Log("original native clear admitted exactly once before lost/canceled/session-close reply; acknowledged uncertainty transfers namespace barrier without replay")
}
func TestMeilisearchNativeNamespaceScopedPermissions(t *testing.T) {
	cfg, secret := nativeFixture(t)
	if cfg.Auth != "bearer" {
		t.Skip("requires authenticated fixture")
	}
	admin := nativeOpen(t)
	permissions := map[string]string{"namespace_create": "indexes.create", "namespace_primary_key": "indexes.update", "namespace_rename": "indexes.update", "namespace_swap": "indexes.swap", "namespace_clear": "documents.delete", "namespace_delete": "indexes.delete"}
	for _, action := range []string{"namespace_create", "namespace_primary_key", "namespace_rename", "namespace_swap", "namespace_clear", "namespace_delete"} {
		for _, allowed := range []bool{false, true} {
			uid := nativeIndex(t, admin)
			if action == "namespace_create" {
				uid += "_missing"
			}
			destination := uid + "_destination"
			if action == "namespace_swap" {
				destination = nativeIndex(t, admin)
			}
			actions := []string{"version", "indexes.get", "stats.get", "tasks.get"}
			if allowed {
				actions = append(actions, permissions[action])
			}
			body, _ := json.Marshal(map[string]any{"actions": actions, "indexes": []string{"*"}, "expiresAt": nil})
			response := nativeRun(t, admin, Request{Method: "POST", Path: "/keys", Body: body})
			var key struct{ Key string }
			if json.Unmarshal(response.Body, &key) != nil || key.Key == "" {
				t.Fatal("namespace delegated key")
			}
			s := testSession(t, cfg, key.Key)
			l, _ := nativeManagedTasks(t, s, "")
			draft := contract.NamespaceDraft{Ref: s.Namespace(uid)}
			if action == "namespace_primary_key" {
				draft.PrimaryKeySet, draft.PrimaryKey = true, "sku"
			}
			if action == "namespace_rename" || action == "namespace_swap" {
				draft.Destination = s.Namespace(destination)
			}
			p := nativeNamespacePlan(t, l, action, draft)
			r, e := l.(contract.TaskManagementLease).MutateTasks(context.Background(), p)
			if !allowed {
				if e == nil || r.State != "rejected" || r.Receipt.Raw != "" {
					t.Fatal("native namespace permission rejection", action, e, r)
				}
				continue
			}
			if e != nil || r.State != "enqueued" {
				t.Fatal("delegated namespace admission", action, e, r)
			}
			r, e = l.(contract.TaskManagementLease).ObserveTaskMutation(context.Background(), r.Receipt, 30*time.Second)
			if e != nil || !r.NamespaceVerified {
				t.Fatal("delegated namespace observation", action, e, r)
			}
		}
	}
	// Stable UID operations are also available on a truly index-scoped profile.
	uid := nativeIndex(t, admin)
	scopedConfig := cfg
	scopedConfig.IndexUID = uid
	s := testSession(t, scopedConfig, secret)
	l, _ := nativeManagedTasks(t, s, uid)
	nativeNamespaceChange(t, l, "namespace_primary_key", contract.NamespaceDraft{Ref: s.Namespace(uid), PrimaryKeySet: true, PrimaryKey: "sku"})
	nativeNamespaceChange(t, l, "namespace_clear", contract.NamespaceDraft{Ref: s.Namespace(uid)})
	for _, a := range []string{"namespace_create", "namespace_rename", "namespace_swap", "namespace_delete"} {
		if l.(contract.NamespaceManagementLease).SupportsNamespaceManagement(a) {
			t.Fatal("global namespace action on configured index scope")
		}
	}
	t.Log("all six native delegated permissions denied/allowed without master fallback; configured sole-index profile supports primary-key and clear only")
}

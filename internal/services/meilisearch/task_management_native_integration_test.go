package meilisearch

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func nativeManagedTasks(t *testing.T, s *Session, index string) (contract.TaskDocumentLease, contract.TaskManagementLease) {
	t.Helper()
	l, err := s.ReserveTaskDocument(s.Namespace(index).Object("tasks", "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, l.(contract.TaskManagementLease)
}
func nativeManagedRecord(t *testing.T, l contract.TaskDocumentLease, id uint32) contract.TaskRecord {
	t.Helper()
	p, err := l.List(context.Background(), contract.TaskQuery{Limit: 1, UIDs: []string{strconv.FormatUint(uint64(id), 10)}})
	if err != nil || len(p.Results) != 1 {
		t.Fatal("original selected task missing", err)
	}
	return p.Results[0]
}
func TestMeilisearchNativeTaskDocumentManagementAdmissionCountsAndRecovery(t *testing.T) {
	cfg, secret := nativeFixture(t)
	s := testSession(t, cfg, secret)
	c := nativeOpen(t)
	uid := nativeIndex(t, c)
	other := nativeIndex(t, c)
	kept := nativeSubmit(t, c, Request{Method: "POST", Path: "/indexes/" + other + "/documents", Body: []byte(`[{"id":"kept"}]`)})
	nativeWait(t, c, kept)
	l, m := nativeManagedTasks(t, s, uid)
	// Connect the original document before occupying the native indexer.
	if _, err := l.List(context.Background(), contract.TaskQuery{Limit: 1}); err != nil {
		t.Fatal(err)
	}
	docs := make([]map[string]any, 40000)
	for i := range docs {
		docs[i] = map[string]any{"id": "block_" + strconv.Itoa(i), "text": strings.Repeat("owned native search words ", 4)}
	}
	body, _ := json.Marshal(docs)
	blocker := nativeSubmit(t, c, Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: body})
	target := nativeSubmit(t, c, Request{Method: "PATCH", Path: "/indexes/" + uid + "/settings", Body: []byte(`{"stopWords":["queued"]}`)})
	original := nativeManagedRecord(t, l, target.UID)
	plan, err := m.PrepareTaskMutation("cancel", []contract.TaskRecord{original})
	if err != nil {
		t.Fatal("queued cancellation preview", err)
	}
	queued, err := m.MutateTasks(context.Background(), plan)
	if err != nil || queued.State != "enqueued" || queued.CountsKnown || queued.Receipt.Type != "taskCancelation" {
		t.Fatal("native cancel admission", err, queued)
	}
	if !strings.Contains(plan.Request, "indexUids="+uid) || !strings.Contains(plan.Request, "uids="+original.UID) {
		t.Fatal("preview lost UID/index restriction")
	}
	if _, err = m.MutateTasks(context.Background(), plan); !errors.Is(err, contract.ErrTaskMutationUsed) {
		t.Fatal("management preview replayed", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	fresh := testSession(t, cfg, secret)
	recovered, err := fresh.RecoverTaskDocument(s.Namespace(uid).Object("tasks", "tasks"), l)
	if err != nil || recovered.Receipt.Raw != queued.Receipt.Raw {
		t.Fatal("native original admission recovery", err)
	}
	defer recovered.Lease.Close()
	next := recovered.Lease.(contract.TaskManagementLease)
	observed, err := next.ObserveTaskMutation(context.Background(), recovered.Receipt, 30*time.Second)
	if err != nil || observed.State != "succeeded" || !observed.CountsKnown || observed.Matched != 1 || observed.Changed != 1 {
		t.Fatal("native cancel completed counts", err, observed)
	}
	canceled := nativeManagedRecord(t, recovered.Lease, target.UID)
	if canceled.Status != "canceled" || !strings.Contains(canceled.Raw, `"canceledBy":`+queued.Receipt.UID) {
		t.Fatal("original task not canceled by original management UID")
	}
	nativeWait(t, c, blocker)
	completed := nativeManagedRecord(t, recovered.Lease, blocker.UID)
	deletion, err := next.PrepareTaskMutation("delete", []contract.TaskRecord{canceled, completed})
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := next.MutateTasks(context.Background(), deletion)
	if err != nil || admitted.State != "enqueued" {
		t.Fatal(err)
	}
	done, err := next.ObserveTaskMutation(context.Background(), admitted.Receipt, 30*time.Second)
	if err != nil || done.State != "succeeded" || !done.CountsKnown || done.Matched != 2 || done.Changed != 2 {
		t.Fatal("native selected deletion counts", err, done)
	}
	if _, err = recovered.Lease.Read(context.Background(), canceled); !errors.Is(err, ErrTaskUnavailable) {
		t.Fatal("deleted record recreated", err)
	}
	if task, _, err := c.LookupTask(context.Background(), kept.UID); err != nil || task.Status != "succeeded" {
		t.Fatal("foreign index task deleted", err)
	}
	p, _, err := c.ListDocuments(context.Background(), uid, DocumentsRequest{Limit: 1})
	if err != nil || p.Total != 40000 {
		t.Fatal("task deletion changed original documents", err)
	}
	t.Log("original selected UID-only task cancellation/deletion previews, HTTP200 admission, exact completed counts, original session replacement recovery, retained documents and foreign tasks; no replay")
}
func TestMeilisearchNativeTaskDocumentManagementLostCancelCloseNoReplay(t *testing.T) {
	for _, fault := range []string{"lost", "canceled", "closed"} {
		t.Run(fault, func(t *testing.T) {
			upstream := nativeOpen(t)
			uid := nativeIndex(t, upstream)
			target := nativeSubmit(t, upstream, Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: []byte(`[{"id":"retained"}]`)})
			nativeWait(t, upstream, target)
			admitted := make(chan Receipt, 1)
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			var writes atomic.Int32
			relay := nativeRelay(t, upstream, func(w http.ResponseWriter, r *http.Request, response Response) bool {
				if r.Method != "DELETE" {
					return false
				}
				writes.Add(1)
				receipt, err := parseReceipt(response.Body)
				if err != nil || response.Status != 200 {
					t.Error("original native deletion admission missing")
					return false
				}
				admitted <- receipt
				if fault == "lost" {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err == nil {
						_ = conn.Close()
					}
					return true
				}
				<-r.Context().Done()
				<-release
				return true
			})
			cfg, secret := nativeFixture(t)
			cfg.Endpoint, cfg.CAFile, cfg.ServerName = relay.config.Endpoint, relay.config.CAFile, relay.config.ServerName
			cfg.ClientCertFile, cfg.ClientKeyFile = "", ""
			s := testSession(t, cfg, secret)
			l, m := nativeManagedTasks(t, s, uid)
			record := nativeManagedRecord(t, l, target.UID)
			plan, err := m.PrepareTaskMutation("delete", []contract.TaskRecord{record})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type reply struct {
				result contract.TaskMutationResult
				err    error
			}
			out := make(chan reply, 1)
			go func() { result, err := m.MutateTasks(ctx, plan); out <- reply{result, err} }()
			var receipt Receipt
			select {
			case receipt = <-admitted:
			case <-time.After(10 * time.Second):
				t.Fatal("original write not admitted")
			}
			if fault == "canceled" {
				cancel()
			}
			if fault == "closed" {
				_ = s.Close()
			}
			select {
			case response := <-out:
				if response.result.State != "uncertain" || response.result.Request == "" || !errors.Is(response.err, contract.ErrTaskLeaseLost) {
					t.Fatal("unknown native outcome not retained", response.result, response.err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("original executor not acknowledged")
			}
			unblock()
			fresh := testSession(t, cfg, secret)
			recovery, err := fresh.RecoverTaskDocument(s.Namespace(uid).Object("tasks", "tasks"), l)
			if err != nil || !recovery.Uncertain {
				t.Fatal("unknown outcome became retry", err)
			}
			defer recovery.Lease.Close()
			if _, err = recovery.Lease.(contract.TaskManagementLease).PrepareTaskMutation("delete", []contract.TaskRecord{record}); !errors.Is(err, contract.ErrTaskMutationUsed) || writes.Load() != 1 {
				t.Fatal("unknown native submission replayed", err)
			}
			nativeWait(t, upstream, receipt)
			p, _, err := upstream.ListDocuments(context.Background(), uid, DocumentsRequest{Limit: 1})
			if err != nil || p.Total != 1 {
				t.Fatal("task record deletion changed documents", err)
			}
		})
	}
	t.Log("lost/canceled/session-close original server admission is uncertain after executor acknowledgement; recovery retains uncertainty and blocks replay")
}
func TestMeilisearchNativeTaskDocumentManagementScopedPermissions(t *testing.T) {
	cfg, _ := nativeFixture(t)
	if cfg.Auth != "bearer" {
		t.Fatal("requires selected authenticated fixture")
	}
	master := nativeOpen(t)
	uid := nativeIndex(t, master)
	other := nativeIndex(t, master)
	for _, mode := range []string{"read-only", "index-task-delete", "global-task-read"} {
		t.Run(mode, func(t *testing.T) {
			write := mode != "read-only"
			target := nativeSubmit(t, master, Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: []byte(`[{"id":"retained"}]`)})
			nativeWait(t, master, target)
			actions := []string{"indexes.get", "tasks.get"}
			if write {
				actions = append(actions, "tasks.delete", "tasks.cancel")
			}
			indexes := []string{uid}
			if mode == "global-task-read" {
				indexes = []string{"*"}
			}
			body, _ := json.Marshal(map[string]any{"name": "owned-task-management-scope", "actions": actions, "indexes": indexes, "expiresAt": nil})
			fields, err := nativeObject(nativeRun(t, master, Request{Method: "POST", Path: "/keys", Body: body}).Body)
			key, e1 := nativeString(fields["key"])
			keyUID, e2 := nativeString(fields["uid"])
			if err != nil || e1 != nil || e2 != nil {
				t.Fatal("native key missing")
			}
			defer nativeRun(t, master, Request{Method: "DELETE", Path: "/keys/" + keyUID})
			cfg.IndexUID = uid
			s := testSession(t, cfg, key)
			l, m := nativeManagedTasks(t, s, uid)
			if s.SupportsTaskDocument(s.Namespace(other).Object("tasks", "tasks")) || s.SupportsTaskDocument(s.Namespace("").Object("tasks", "tasks")) {
				t.Fatal("profile scope escaped")
			}
			record := nativeManagedRecord(t, l, target.UID)
			plan, err := m.PrepareTaskMutation("delete", []contract.TaskRecord{record})
			if err != nil {
				t.Fatal(err)
			}
			result, err := m.MutateTasks(context.Background(), plan)
			if !write {
				var status *HTTPError
				if result.State != "rejected" || !errors.As(err, &status) || status.Status != 403 || result.TaskRaw == "" {
					t.Fatal("original permission refusal missing", err, result)
				}
				if task, _, err := master.LookupTask(context.Background(), target.UID); err != nil || task.Status != "succeeded" {
					t.Fatal("read-only key used fallback", err)
				}
				return
			}
			if err != nil || result.State != "enqueued" {
				t.Fatal("native scoped management admission", err)
			}
			observed, err := m.ObserveTaskMutation(context.Background(), result.Receipt, 30*time.Second)
			if mode == "index-task-delete" {
				var status *HTTPError
				if !errors.Is(err, contract.ErrTaskMutationUnavailable) || !errors.As(err, &status) || status.Status != 404 || observed.State != "enqueued" || observed.CountsKnown || observed.Receipt.Raw != result.Receipt.Raw || !strings.Contains(observed.TaskRaw, "task_not_found") {
					t.Fatal("hidden global management task mislabeled or lost original receipt", err, observed)
				}
				// Independent fixture administration confirms native completion.
				// The production scoped lease must retain its own HTTP404 and
				// cannot use these fixture credentials or invent those counts.
				original, e := parseReceipt([]byte(result.Receipt.Raw))
				if e != nil {
					t.Fatal(e)
				}
				details := nativeTaskDetails(t, nativeWait(t, master, original))
				changed, e := nativeUint(details["deletedTasks"], 64)
				if e != nil || changed != 1 {
					t.Fatal("native scoped deletion not completed")
				}
				recovered, e := s.RecoverTaskDocument(s.Namespace(uid).Object("tasks", "tasks"), l)
				if e != nil || recovered.Receipt.Raw != result.Receipt.Raw {
					t.Fatal("hidden management receipt discarded on recovery", e)
				}
				defer recovered.Lease.Close()
				return
			}
			if err != nil || observed.State != "succeeded" || !observed.CountsKnown || observed.Changed != 1 {
				t.Fatal("explicit profile scope with globally authorized task observation", err, observed)
			}
		})
	}
	t.Log("native read-only HTTP403, index-scoped write admission with hidden global management-task HTTP404 retained, and globally authorized original completed counts without master fallback")
}

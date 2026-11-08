package meilisearch

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestMeilisearchNativeServerBackupAdmissionArtifactsAndRecovery(t *testing.T) {
	cfg, secret := nativeFixture(t)
	s := testSession(t, cfg, secret)
	l, m := nativeManagedTasks(t, s, "")
	b := l.(contract.ServerBackupLease)
	artifacts := map[string]contract.TaskMutationResult{}
	for _, action := range []string{"dump", "snapshot"} {
		if !b.SupportsServerBackup(action) {
			t.Fatal("global server backup unsupported")
		}
		plan, err := b.PrepareServerBackup(action)
		if err != nil {
			t.Fatal(err)
		}
		result, err := m.MutateTasks(context.Background(), plan)
		if err != nil || result.State != "enqueued" || result.Artifact.Kind != "" || result.CountsKnown || len(result.Targets) != 0 || result.Receipt.Type != serverBackupKind(serverBackupRequest(action)) {
			t.Fatal("native backup admission", err, result)
		}
		if _, err = m.MutateTasks(context.Background(), plan); !errors.Is(err, contract.ErrTaskMutationUsed) {
			t.Fatal("backup replayed", err)
		}
		if action == "dump" {
			_ = s.Close()
			fresh := testSession(t, cfg, secret)
			recovery, err := fresh.RecoverTaskDocument(s.Namespace("").Object("tasks", "tasks"), l)
			if err != nil || recovery.Receipt.Raw != result.Receipt.Raw {
				t.Fatal("original backup receipt lost", err)
			}
			l = recovery.Lease
			defer l.Close()
			m = l.(contract.TaskManagementLease)
			b = l.(contract.ServerBackupLease)
			result.Receipt = recovery.Receipt
		}
		observed, err := m.ObserveTaskMutation(context.Background(), result.Receipt, 30*time.Second)
		if err != nil || observed.State != "succeeded" || observed.Artifact.Kind != action || observed.CountsKnown || observed.TaskRaw == "" {
			t.Fatal("native backup result", err, observed)
		}
		if action == "dump" && observed.Artifact.UID == "" || action == "snapshot" && observed.Artifact.UID != "" {
			t.Fatal("native artifact fabricated", observed)
		}
		artifacts[action] = observed
	}
	if path := os.Getenv("NYADB_MEILISEARCH_BACKUP_MANIFEST"); path != "" {
		raw, err := json.Marshal(artifacts)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Log("native global POST /dumps and /snapshots HTTP202 admission, original receipt recovery, completed dumpUid and snapshot without path; no replay or local download claim")
}
func TestMeilisearchNativeServerBackupScopedPermissions(t *testing.T) {
	cfg, secret := nativeFixture(t)
	if cfg.Auth != "bearer" {
		t.Skip("requires authenticated fixture")
	}
	admin := nativeOpen(t)
	uid := nativeIndex(t, admin)
	for _, action := range []string{"dump", "snapshot"} {
		s := testSession(t, cfg, secret)
		scoped, _ := nativeManagedTasks(t, s, uid)
		if scoped.(contract.ServerBackupLease).SupportsServerBackup(action) {
			t.Fatal("index scope can back up global server")
		}
		for _, allowed := range []bool{false, true} {
			actions := []string{"version", "indexes.get", "tasks.get"}
			if allowed {
				actions = append(actions, serverBackupRequest(action).Path[1:]+".create")
			}
			body, _ := json.Marshal(map[string]any{"actions": actions, "indexes": []string{"*"}, "expiresAt": nil})
			response := nativeRun(t, admin, Request{Method: "POST", Path: "/keys", Body: body})
			var key struct{ Key string }
			err := json.Unmarshal(response.Body, &key)
			if err != nil || key.Key == "" {
				t.Fatal("native backup key", err)
			}
			session := testSession(t, cfg, key.Key)
			_, m := nativeManagedTasks(t, session, "")
			b := m.(contract.ServerBackupLease)
			plan, err := b.PrepareServerBackup(action)
			if err != nil {
				t.Fatal(err)
			}
			result, err := m.MutateTasks(context.Background(), plan)
			if !allowed {
				if err == nil || result.State != "rejected" || result.Receipt.Raw != "" || !strings.Contains(result.TaskRaw, "invalid_api_key") {
					t.Fatal("permission refusal lost", err, result)
				}
				continue
			}
			if err != nil {
				t.Fatal("granted native backup permission", err, result)
			}
			result, err = m.ObserveTaskMutation(context.Background(), result.Receipt, 30*time.Second)
			if err != nil || result.Artifact.Kind != action {
				t.Fatal("granted backup observation", err, result)
			}
		}
	}
}
func TestMeilisearchNativeServerBackupLostCancelCloseNoReplay(t *testing.T) {
	for _, fault := range []string{"lost", "canceled", "closed"} {
		t.Run(fault, func(t *testing.T) {
			upstream := nativeOpen(t)
			cfg, secret := nativeFixture(t)
			var writes atomic.Int32
			var receipt Receipt
			var mu sync.Mutex
			entered, release := make(chan struct{}), make(chan struct{})
			c, proxy := mockOpen(t, func(w http.ResponseWriter, r *http.Request) {
				response, err := upstream.Do(context.Background(), Request{Method: r.Method, Path: strings.TrimPrefix(r.URL.RequestURI(), "/proxy/meili")})
				if err != nil {
					t.Error("upstream backup proxy", err)
					w.WriteHeader(502)
					return
				}
				if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/dumps") {
					writes.Add(1)
					parsed, err := parseReceipt(response.Body)
					if err != nil {
						t.Error(err)
					}
					mu.Lock()
					receipt = parsed
					mu.Unlock()
					close(entered)
					if fault != "lost" {
						<-release
					}
					conn, _, _ := w.(http.Hijacker).Hijack()
					_ = conn.Close()
					return
				}
				w.WriteHeader(response.Status)
				_, _ = w.Write(response.Body)
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
			plan, err := l.(contract.ServerBackupLease).PrepareServerBackup("dump")
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
			go func() { r, e := m.MutateTasks(ctx, plan); out <- reply{r, e} }()
			<-entered
			if fault == "canceled" {
				cancel()
				close(release)
			}
			if fault == "closed" {
				done := make(chan struct{})
				go func() { _ = l.Close(); close(done) }()
				close(release)
				<-done
			}
			result := <-out
			if result.err == nil || result.result.State != "uncertain" || result.result.Request != "POST /dumps" || writes.Load() != 1 {
				t.Fatal("lost admitted backup not uncertain", result)
			}
			if _, err = m.MutateTasks(context.Background(), plan); err == nil || writes.Load() != 1 {
				t.Fatal("backup replayed", err)
			}
			mu.Lock()
			original := receipt
			mu.Unlock()
			nativeWait(t, upstream, original)
		})
	}
}

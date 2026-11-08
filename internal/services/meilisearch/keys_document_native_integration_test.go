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

func nativeKeys(t *testing.T, s *Session) *keysDocument {
	t.Helper()
	lease, err := s.ReserveKeysDocument(s.Namespace("").Object("keys", "keys"))
	if err != nil {
		t.Fatal("original key reservation", err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	return lease.(*keysDocument)
}
func nativeKeyDraft(name string) contract.KeyDraft {
	return contract.KeyDraft{Name: name, Description: "exact 原生🐱", Actions: []string{"search"}, Indexes: []string{"owned*"}}
}
func nativeKeyCreate(t *testing.T, d *keysDocument, draft contract.KeyDraft) contract.KeyResult {
	t.Helper()
	plan, err := d.Prepare("create", contract.KeyRecord{}, draft)
	if err != nil {
		t.Fatal("pure key creation preview", err)
	}
	result, err := d.Apply(context.Background(), plan)
	if err != nil || result.State != "completed" || result.Status != 201 || result.Record.UID == "" {
		t.Fatal("original synchronous key creation", err, result.State, result.Status)
	}
	return result
}
func keyNoSecret(t *testing.T, value any, secrets ...string) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal("public key evidence unavailable")
	}
	for _, secret := range secrets {
		if secret != "" && strings.Contains(string(raw), secret) {
			t.Fatal("public key evidence leaked a credential")
		}
	}
}
func nativeKeyStatus(t *testing.T, err error, status int) *contract.KeyRequestError {
	t.Helper()
	var diagnostic *contract.KeyRequestError
	var native *HTTPError
	if !errors.As(err, &diagnostic) || !errors.As(err, &native) || native.Status != status || diagnostic.Raw == "" {
		t.Fatal("complete original key refusal missing", err)
	}
	return diagnostic
}

func TestMeilisearchNativeKeysAnonymousMissingMaster(t *testing.T) {
	cfg, secret := nativeFixture(t)
	if cfg.Auth != "none" || secret != "" {
		t.Fatal("requires owned native anonymous fixture")
	}
	s := testSession(t, cfg, secret)
	d := nativeKeys(t, s)
	_, err := d.List(context.Background(), 0, 20)
	if !strings.Contains(nativeKeyStatus(t, err, 401).Raw, "missing_master_key") {
		t.Fatal("anonymous server fabricated an API key catalog")
	}
	plan, err := d.Prepare("create", contract.KeyRecord{}, nativeKeyDraft("anonymous refusal"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := d.Apply(context.Background(), plan)
	if result.State != "rejected" || result.Status != 401 || !strings.Contains(nativeKeyStatus(t, err, 401).Raw, "missing_master_key") {
		t.Fatal("anonymous synchronous refusal became success or unknown", result.State)
	}
	recovered, err := s.RecoverKeysDocument(d.object, d)
	if err != nil || recovered.Uncertain || recovered.Last.State != "rejected" || recovered.Last.Response != result.Response {
		t.Fatal("definite anonymous refusal lost during recovery", err)
	}
	defer recovered.Lease.Close()
	t.Log("native anonymous GET keys and POST creation both return HTTP401 missing_master_key; pure preview makes no permission claim and recovery retains a definite synchronous refusal")
}

func TestMeilisearchNativeKeysPagingRedactionAndSynchronousCRUD(t *testing.T) {
	cfg, master := nativeFixture(t)
	s := testSession(t, cfg, master)
	d := nativeKeys(t, s)
	created := make(map[string]contract.KeyRecord)
	secrets := []string{master}
	for i := 0; i < 23; i++ {
		draft := nativeKeyDraft("owned paging " + strconv.Itoa(i))
		if i == 0 {
			draft.UID, draft.Actions, draft.ExpiresAt = testKeyUID, []string{"documents.get", "search"}, "2030-01-01T00:00:00Z"
		}
		result := nativeKeyCreate(t, d, draft)
		secret, err := d.Reveal(result.Record)
		if err != nil || len(secret) != 64 {
			t.Fatal("explicit original created secret unavailable", err)
		}
		secrets = append(secrets, secret)
		keyNoSecret(t, result, secrets...)
		created[result.UID] = result.Record
	}
	seen := map[string]bool{}
	var original contract.KeyRecord
	for offset := uint64(0); ; offset += 7 {
		page, err := d.List(context.Background(), offset, 7)
		if err != nil || page.Offset != offset || page.Limit != 7 || page.Total < 23 || len(page.Results) == 0 {
			t.Fatal("original native key pagination", err)
		}
		keyNoSecret(t, page, secrets...)
		for _, r := range page.Results {
			if seen[r.UID] {
				t.Fatal("native offset repeated a key")
			}
			seen[r.UID] = true
			if r.UID == testKeyUID {
				original, err = d.Read(context.Background(), r)
				if err != nil || original.Raw != r.Raw || original.Fingerprint != r.Fingerprint {
					t.Fatal("complete original key read changed evidence", err)
				}
			}
		}
		if offset+uint64(len(page.Results)) >= page.Total {
			break
		}
	}
	for uid := range created {
		if !seen[uid] {
			t.Fatal("native page omitted owned key")
		}
	}
	// A record leaves the registry on explicit page replacement. Reread its
	// native page before changing it; no arbitrary UID or secret lookup occurs.
	for offset := uint64(0); ; offset += 7 {
		page, err := d.List(context.Background(), offset, 7)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, r := range page.Results {
			if r.UID == testKeyUID {
				original, found = r, true
			}
		}
		if found {
			break
		}
		if offset+uint64(len(page.Results)) >= page.Total {
			t.Fatal("owned original key missing")
		}
	}
	secret, err := d.Reveal(original)
	if err != nil {
		t.Fatal(err)
	}
	foreign := nativeKeys(t, s)
	if _, err = foreign.Reveal(original); !errors.Is(err, contract.ErrKeyConflict) {
		t.Fatal("original secret rebound to another executor")
	}
	plan, err := d.Prepare("update", original, contract.KeyDraft{Name: "updated 原生🐱", Description: "new metadata", Actions: []string{"*"}, Indexes: []string{"*"}})
	if err != nil || strings.Contains(plan.Body, "actions") || strings.Contains(plan.Body, "indexes") || strings.Contains(plan.Body, "expiresAt") {
		t.Fatal("native immutable policy included in metadata update", err)
	}
	updated, err := d.Apply(context.Background(), plan)
	if err != nil || updated.State != "completed" || updated.Status != 200 || updated.Record.UID != original.UID || !sameKeySet(updated.Record.Actions, original.Actions) || updated.Record.ExpiresAt != original.ExpiresAt {
		t.Fatal("native synchronous metadata update", err, updated.State)
	}
	keyNoSecret(t, updated, secrets...)
	if got, err := d.Reveal(updated.Record); err != nil || got != secret {
		t.Fatal("metadata update changed native key bytes", err)
	}
	if _, err = d.Apply(context.Background(), plan); !errors.Is(err, contract.ErrKeyPlanUsed) {
		t.Fatal("original synchronous update replayed")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	fresh := testSession(t, cfg, master)
	recovery, err := fresh.RecoverKeysDocument(d.object, d)
	if err != nil || recovery.Uncertain || recovery.Last.State != "completed" || recovery.Last.Record.Token == updated.Record.Token {
		t.Fatal("original synchronous result recovery", err)
	}
	defer recovery.Lease.Close()
	if got, err := recovery.Lease.Reveal(recovery.Last.Record); err != nil || got != secret {
		t.Fatal("explicit recovered secret changed", err)
	}
	deletion, err := recovery.Lease.Prepare("delete", recovery.Last.Record, contract.KeyDraft{})
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := recovery.Lease.Apply(context.Background(), deletion)
	if err != nil || deleted.State != "completed" || deleted.Status != 204 || deleted.Response != "" {
		t.Fatal("native synchronous empty deletion response", err)
	}
	if _, err = recovery.Lease.Reveal(recovery.Last.Record); !errors.Is(err, contract.ErrKeyConflict) {
		t.Fatal("deleted secret retained in registry")
	}
	t.Log("23 owned keys over native OFFSET pages; masked complete native JSON, optional UID and expiration, pure explicit secret reveal, immutable policy, HTTP201/200/204 synchronous receipts and original-session evidence recovery")
}

func TestMeilisearchNativeKeysNativePolicyRefusalsAndDelegatedPermissions(t *testing.T) {
	cfg, master := nativeFixture(t)
	admin := nativeOpen(t)
	s := testSession(t, cfg, master)
	d := nativeKeys(t, s)
	created := nativeKeyCreate(t, d, nativeKeyDraft("owned policy"))
	for _, body := range []string{`{"actions":["documents.get"]}`, `{"indexes":["*"]}`, `{"expiresAt":null}`} {
		response, err := admin.Do(context.Background(), Request{Method: "PATCH", Path: "/keys/" + created.UID, Body: []byte(body)})
		var status *HTTPError
		if !errors.As(err, &status) || status.Status != 400 || !strings.Contains(string(response.Body), "immutable_api_key_") {
			t.Fatal("native immutable policy refusal missing", err)
		}
		// This fixture client deliberately treats generic REST mutation errors
		// as unknown. Reopen explicitly; the typed key executor uses the exact
		// native refusal to distinguish a definite rejection.
		admin = nativeOpen(t)
	}
	for _, policy := range []string{"duplicate", "action", "expiration"} {
		lease := nativeKeys(t, s)
		draft := nativeKeyDraft("owned refused " + policy)
		expected := 400
		if policy == "duplicate" {
			draft.UID, expected = created.UID, 409
		} else if policy == "action" {
			draft.Actions = []string{"not.a.native.action"}
		} else {
			draft.ExpiresAt = "2000-01-01T00:00:00Z"
		}
		plan, err := lease.Prepare("create", contract.KeyRecord{}, draft)
		if err != nil {
			t.Fatal("native policy preview rejected before native validation", err)
		}
		result, err := lease.Apply(context.Background(), plan)
		nativeKeyStatus(t, err, expected)
		if result.State != "rejected" || result.Status != expected || lease.uncertain {
			t.Fatal("native definite policy refusal became unknown", result.State)
		}
	}
	for _, access := range []string{"read", "write", "revoked"} {
		draft := nativeKeyDraft("owned delegated " + access)
		draft.Indexes = []string{"*"}
		draft.Actions = []string{"indexes.get", "version", "keys.get"}
		if access == "write" {
			draft.Actions = append(draft.Actions, "keys.create", "keys.update", "keys.delete")
		}
		result := nativeKeyCreate(t, d, draft)
		secret, err := d.Reveal(result.Record)
		if err != nil {
			t.Fatal(err)
		}
		delegate := testSession(t, cfg, secret)
		lease := nativeKeys(t, delegate)
		page, err := lease.List(context.Background(), 0, 20)
		if err != nil || page.Total < 1 {
			t.Fatal("native delegated keys.get denied", err)
		}
		keyNoSecret(t, page, secret, master)
		plan, err := lease.Prepare("create", contract.KeyRecord{}, nativeKeyDraft("owned delegated child"))
		if err != nil {
			t.Fatal(err)
		}
		if access == "revoked" {
			nativeRun(t, admin, Request{Method: "DELETE", Path: "/keys/" + result.UID})
		}
		child, err := lease.Apply(context.Background(), plan)
		if access == "revoked" {
			var status *HTTPError
			if !errors.As(err, &status) || status.Status != 403 || child.State != "not_submitted" || child.Status != 0 {
				t.Fatal("revoked original key passed observation or submitted mutation", err, child.State)
			}
			continue
		}
		if access == "read" {
			nativeKeyStatus(t, err, 403)
			if child.State != "rejected" || child.Status != 403 {
				t.Fatal("original delegate refused mutation used fallback", child.State)
			}
			continue
		}
		if err != nil || child.State != "completed" || child.Status != 201 {
			t.Fatal("native delegated keys.create denied", err)
		}
		for _, action := range []string{"update", "delete"} {
			p, err := lease.Prepare(action, child.Record, contract.KeyDraft{Name: "delegated update", Description: "metadata"})
			if err != nil {
				t.Fatal(err)
			}
			child, err = lease.Apply(context.Background(), p)
			if err != nil || child.State != "completed" {
				t.Fatal("native delegated metadata/delete denied", err)
			}
		}
	}
	t.Log("native immutable actions/indexes/expiration HTTP400, duplicate HTTP409 and expired policy refusal; original delegated keys.get/create/update/delete work without master fallback, read-only POST is rejected with HTTP403 and revoked delegate fails original observation with HTTP403 before submission")
}

func TestMeilisearchNativeKeysLostCancelCloseAndRecoveryNoReplay(t *testing.T) {
	for _, action := range []string{"create", "update", "delete"} {
		for _, fault := range []string{"lost", "canceled", "closed"} {
			t.Run(action+"/"+fault, func(t *testing.T) {
				cfg, master := nativeFixture(t)
				upstream := nativeOpen(t)
				seed := nativeKeys(t, testSession(t, cfg, master))
				var original contract.KeyRecord
				if action != "create" {
					original = nativeKeyCreate(t, seed, nativeKeyDraft("owned lost original")).Record
				}
				accepted := make(chan string, 1)
				release := make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				defer unblock()
				var writes atomic.Int32
				relay := nativeRelay(t, upstream, func(w http.ResponseWriter, r *http.Request, response Response) bool {
					if r.Method == "GET" {
						return false
					}
					writes.Add(1)
					uid := original.UID
					if action != "delete" {
						fields, err := nativeObject(response.Body)
						uid, _ = nativeString(fields["uid"])
						if err != nil || uid == "" || response.Status != map[string]int{"create": 201, "update": 200}[action] {
							t.Error("real synchronous key change not demonstrated")
						}
					} else if response.Status != 204 || len(response.Body) != 0 {
						t.Error("real synchronous key deletion not demonstrated")
					}
					accepted <- uid
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
				cfg.Endpoint, cfg.CAFile, cfg.ServerName = relay.config.Endpoint, relay.config.CAFile, relay.config.ServerName
				cfg.ClientCertFile, cfg.ClientKeyFile = "", ""
				s := testSession(t, cfg, master)
				d := nativeKeys(t, s)
				if action != "create" {
					for offset := uint64(0); ; offset += 100 {
						page, err := d.List(context.Background(), offset, 100)
						if err != nil {
							t.Fatal(err)
						}
						found := false
						for _, r := range page.Results {
							if r.UID == original.UID {
								original, found = r, true
							}
						}
						if found {
							break
						}
						if offset+uint64(len(page.Results)) >= page.Total {
							t.Fatal("original fault target missing")
						}
					}
				}
				plan, err := d.Prepare(action, original, nativeKeyDraft("owned changed once"))
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				type reply struct {
					result contract.KeyResult
					err    error
				}
				out := make(chan reply, 1)
				go func() { result, err := d.Apply(ctx, plan); out <- reply{result, err} }()
				var uid string
				select {
				case uid = <-accepted:
				case <-time.After(10 * time.Second):
					t.Fatal("real key mutation did not execute")
				}
				if fault == "canceled" {
					cancel()
				}
				if fault == "closed" {
					_ = s.Close()
				}
				var settled reply
				select {
				case settled = <-out:
					if settled.result.State != "uncertain" || settled.result.Request == "" || !errors.Is(settled.err, contract.ErrKeyLeaseLost) || writes.Load() != 1 {
						t.Fatal("lost synchronous write not retained after acknowledgement", settled.err, settled.result.State)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("original key executor did not acknowledge cancellation")
				}
				unblock()
				fresh := testSession(t, cfg, master)
				recovery, err := fresh.RecoverKeysDocument(d.object, d)
				if err != nil || !recovery.Uncertain || recovery.Last.Request != settled.result.Request || recovery.Last.State != "uncertain" || writes.Load() != 1 {
					t.Fatal("unknown synchronous write lost during explicit recovery", err)
				}
				defer recovery.Lease.Close()
				if _, err = recovery.Lease.Prepare("create", contract.KeyRecord{}, nativeKeyDraft("must not replay")); !errors.Is(err, contract.ErrKeyPlanUsed) || writes.Load() != 1 {
					t.Fatal("unknown synchronous key write became retry")
				}
				observer := nativeOpen(t)
				response, err := observer.Do(context.Background(), Request{Method: "GET", Path: "/keys/" + uid})
				if action == "delete" {
					var status *HTTPError
					if !errors.As(err, &status) || status.Status != 404 {
						t.Fatal("independent observer did not confirm deletion")
					}
				} else {
					fields, e := nativeObject(response.Body)
					name, _ := nativeString(fields["name"])
					if err != nil || e != nil || name != "owned changed once" {
						t.Fatal("independent observer did not confirm single key change", err)
					}
					keyNoSecret(t, settled.result, master)
				}
			})
		}
	}
	t.Log("real synchronous create/update/delete each execute once before lost/canceled/session-close response; original executor acknowledgement, retained concrete unknown request and recovery prohibit replay, independent original-native observer confirms the server change")
}

func TestMeilisearchNativeKeysReadLossCancelCloseAndReplacement(t *testing.T) {
	for _, fault := range []string{"lost", "canceled", "closed"} {
		t.Run(fault, func(t *testing.T) {
			upstream := nativeOpen(t)
			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			var reads atomic.Int32
			relay := nativeRelay(t, upstream, func(w http.ResponseWriter, r *http.Request, response Response) bool {
				if !strings.HasSuffix(r.URL.Path, "/keys") {
					return false
				}
				reads.Add(1)
				entered <- struct{}{}
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
			cfg, master := nativeFixture(t)
			cfg.Endpoint, cfg.CAFile, cfg.ServerName = relay.config.Endpoint, relay.config.CAFile, relay.config.ServerName
			cfg.ClientCertFile, cfg.ClientKeyFile = "", ""
			s := testSession(t, cfg, master)
			d := nativeKeys(t, s)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			out := make(chan error, 1)
			go func() { _, err := d.List(ctx, 0, 20); out <- err }()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("original key read not entered")
			}
			if fault == "canceled" {
				cancel()
			}
			if fault == "closed" {
				_ = s.Close()
			}
			select {
			case err := <-out:
				if !errors.Is(err, contract.ErrKeyLeaseLost) {
					t.Fatal("original read was not retired", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("original key read executor did not acknowledge")
			}
			unblock()
			if _, err := d.List(context.Background(), 0, 20); !errors.Is(err, contract.ErrKeyLeaseLost) || reads.Load() != 1 {
				t.Fatal("lost original key read replayed", err)
			}
			fresh := testSession(t, cfg, master)
			recovered, err := fresh.RecoverKeysDocument(d.object, d)
			if err != nil || recovered.Uncertain || reads.Load() != 1 {
				t.Fatal("read recovery performed I/O or claimed unknown mutation", err)
			}
			_ = recovered.Lease.Close()
		})
	}
	cfg, master := nativeFixture(t)
	s := testSession(t, cfg, master)
	d := nativeKeys(t, s)
	created := nativeKeyCreate(t, d, nativeKeyDraft("owned replaced"))
	plan, err := d.Prepare("update", created.Record, nativeKeyDraft("must not overwrite"))
	if err != nil {
		t.Fatal(err)
	}
	admin := nativeOpen(t)
	nativeRun(t, admin, Request{Method: "DELETE", Path: "/keys/" + created.UID})
	// Native UID-derived secrets can be identical after a UID is recreated.
	// CreatedAt and the full observation are compared too, without claiming CAS.
	time.Sleep(time.Millisecond)
	draft := nativeKeyDraft("replacement")
	draft.UID = created.UID
	body, err := keyBody("create", draft)
	if err != nil {
		t.Fatal(err)
	}
	nativeRun(t, admin, Request{Method: "POST", Path: "/keys", Body: []byte(body)})
	result, err := d.Apply(context.Background(), plan)
	if !errors.Is(err, contract.ErrKeyConflict) || result.State != "not_submitted" {
		t.Fatal("recreated original UID was overwritten", err)
	}
	t.Log("original lost/canceled/closed key reads settle once and recovery has no I/O; recreated native UID with colliding derived secret is rejected by original creation/full-record evidence, with no atomic CAS claim")
}

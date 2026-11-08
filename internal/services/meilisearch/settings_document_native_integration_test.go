package meilisearch

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func nativeSettingsEdit(t *testing.T, snapshot contract.SettingsSnapshot, fields map[string]any) string {
	t.Helper()
	var value map[string]json.RawMessage
	if decodeJSON([]byte(snapshot.Raw), &value) != nil {
		t.Fatal("native settings object unavailable")
	}
	for name, v := range fields {
		raw, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		value[name] = raw
	}
	raw, e := json.Marshal(value)
	if e != nil {
		t.Fatal(e)
	}
	return string(raw)
}
func nativeSettingsTypo(t *testing.T, source contract.SettingsSnapshot, enabled bool) map[string]any {
	t.Helper()
	var fields map[string]json.RawMessage
	_ = decodeJSON([]byte(source.Raw), &fields)
	var typo map[string]any
	if decodeJSON(fields["typoTolerance"], &typo) != nil {
		t.Fatal("native typo settings unavailable")
	}
	typo["enabled"] = enabled
	return typo
}
func TestMeilisearchNativeSettingsDeltaAdmissionObserveResetAndRecovery(t *testing.T) {
	cfg, secret := nativeFixture(t)
	s := testSession(t, cfg, secret)
	c := nativeOpen(t)
	uid := nativeIndex(t, c)
	nativeWait(t, c, nativeSubmit(t, c, Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: []byte(`[{"id":"one","title":"原生猫","other":"ignored","exact":9007199254740993}]`)}))
	nativeWait(t, c, nativeSubmit(t, c, Request{Method: "PATCH", Path: "/indexes/" + uid + "/settings", Body: []byte(`{"synonyms":{"猫":["kitten"],"狗":["dog"]},"faceting":{"sortFacetValuesBy":{"*":"alpha","title":"count"}}}`)}))
	o := s.Namespace(uid).Object("settings", "settings")
	lease, err := s.ReserveSettingsDocument(o)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	source, err := lease.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	desired := nativeSettingsEdit(t, source, map[string]any{"searchableAttributes": []string{"title"}, "filterableAttributes": []string{"id"}, "sortableAttributes": []string{"title"}, "synonyms": map[string]any{"猫": []string{"cat"}, "狗": []string{"dog"}}, "faceting": map[string]any{"maxValuesPerFacet": 100, "sortFacetValuesBy": map[string]any{"*": "alpha", "title": "alpha"}}, "typoTolerance": nativeSettingsTypo(t, source, false)})
	plan, err := lease.Prepare(source, desired, false)
	if err != nil || strings.Contains(plan.Patch, "displayedAttributes") || strings.Contains(plan.Patch, "embedders") {
		t.Fatal("native settings delta included untouched fields", err, plan.Patch)
	}
	result, err := lease.Apply(context.Background(), plan)
	if err != nil || result.State != "enqueued" || result.Receipt.Type != "settingsUpdate" {
		t.Fatal("native settings admission", err)
	}
	if _, err = lease.Apply(context.Background(), plan); !errors.Is(err, contract.ErrSettingsPlanUsed) {
		t.Fatal("native settings preview replayed", err)
	}
	recovered, err := s.RecoverSettingsDocument(o, lease)
	if err != nil || recovered.Receipt.Raw != result.Receipt.Raw {
		t.Fatal("native original settings receipt recovery", err)
	}
	defer recovered.Lease.Close()
	observed, err := recovered.Lease.Observe(context.Background(), recovered.Receipt, 30*time.Second)
	if err != nil || observed.State != "succeeded" || !strings.Contains(observed.TaskRaw, `"finishedAt"`) {
		t.Fatal("native settings task completion", err)
	}
	var actual map[string]json.RawMessage
	if decodeJSON([]byte(observed.Snapshot.Raw), &actual) != nil || string(actual["searchableAttributes"]) != `["title"]` || string(actual["filterableAttributes"]) != `["id"]` {
		t.Fatal("native current settings not observed")
	}
	if !strings.Contains(string(actual["synonyms"]), `"狗":["dog"]`) || !strings.Contains(string(actual["faceting"]), `"*":"alpha"`) {
		t.Fatal("native dictionary replacement discarded unchanged synonyms or facet orders")
	}
	docs := nativeRun(t, c, Request{Method: "GET", Path: "/indexes/" + uid + "/documents?limit=10"})
	if !strings.Contains(string(docs.Body), "9007199254740993") {
		t.Fatal("settings mutation altered original document precision")
	}
	reset, err := recovered.Lease.Prepare(observed.Snapshot, "", true)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := recovered.Lease.Apply(context.Background(), reset)
	if err != nil || queued.State != "enqueued" {
		t.Fatal("native settings reset admission", err)
	}
	done, err := recovered.Lease.Observe(context.Background(), queued.Receipt, 30*time.Second)
	if err != nil || done.State != "succeeded" {
		t.Fatal("native settings reset completion", err)
	}
	actual = nil
	_ = decodeJSON([]byte(done.Snapshot.Raw), &actual)
	if string(actual["searchableAttributes"]) != `["*"]` || string(actual["filterableAttributes"]) != `[]` {
		t.Fatal("native settings reset did not restore defaults")
	}
	docs = nativeRun(t, c, Request{Method: "GET", Path: "/indexes/" + uid + "/documents?limit=10"})
	if !strings.Contains(string(docs.Body), "9007199254740993") {
		t.Fatal("settings reset deleted original documents")
	}
	t.Log("native recursive settings delta, unchanged fields preserved, HTTP202 original receipt, one-use previews, explicit recovered observation, default reset and unchanged document bytes")
}
func TestMeilisearchNativeSettingsConflictFailureAndIndexReplacement(t *testing.T) {
	cfg, secret := nativeFixture(t)
	s := testSession(t, cfg, secret)
	c := nativeOpen(t)
	uid := nativeIndex(t, c)
	lease, err := s.ReserveSettingsDocument(s.Namespace(uid).Object("settings", "settings"))
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	source, err := lease.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := lease.Prepare(source, nativeSettingsEdit(t, source, map[string]any{"searchableAttributes": []string{"title"}}), false)
	if err != nil {
		t.Fatal(err)
	}
	nativeWait(t, c, nativeSubmit(t, c, Request{Method: "PATCH", Path: "/indexes/" + uid + "/settings", Body: []byte(`{"searchableAttributes":["concurrent"]}`)}))
	denied, err := lease.Apply(context.Background(), plan)
	if !errors.Is(err, contract.ErrSettingsConflict) || denied.State != "not_submitted" {
		t.Fatal("native settings conflict overwrote concurrent settings", err)
	}
	current, err := lease.Read(context.Background())
	if err != nil || !strings.Contains(current.Raw, "concurrent") {
		t.Fatal("native concurrent settings lost", err)
	}
	nativeWait(t, c, nativeSubmit(t, c, Request{Method: "POST", Path: "/indexes/" + uid + "/documents", Body: []byte(`[{"id":"owned","title":"原生"}]`)}))
	gatewayRaw, e := os.ReadFile(os.Getenv("NYADB_MEILISEARCH_CONFIG"))
	if e != nil {
		t.Fatal("private native fixture unavailable")
	}
	var fixture struct {
		Gateway string `json:"fixture_gateway"`
	}
	if json.Unmarshal(gatewayRaw, &fixture) != nil || net.ParseIP(fixture.Gateway) == nil {
		t.Fatal("owned native fixture bridge gateway unavailable")
	}
	listener, e := net.Listen("tcp", net.JoinHostPort(fixture.Gateway, "0"))
	if e != nil {
		t.Fatal("owned local model fixture unavailable", e)
	}
	var modelCalls atomic.Int32
	model := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"owned-settings-fixture-rejected"}`))
	}))
	_ = model.Listener.Close()
	model.Listener = listener
	model.Start()
	defer model.Close()
	invalid := nativeSettingsEdit(t, current, map[string]any{"embedders": map[string]any{"owned": map[string]any{"source": "rest", "dimensions": 3, "url": model.URL, "request": map[string]any{"input": "{{text}}"}, "response": map[string]any{"embedding": "{{embedding}}"}}}})
	failing, err := lease.Prepare(current, invalid, false)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := lease.Apply(context.Background(), failing)
	if err != nil || queued.State != "enqueued" {
		t.Fatal("native invalid settings did not preserve asynchronous admission", err)
	}
	failed, err := lease.Observe(context.Background(), queued.Receipt, 30*time.Second)
	if err != nil || failed.State != "failed" || !strings.Contains(failed.TaskRaw, `"error"`) || failed.Snapshot.Raw == "" || modelCalls.Load() == 0 {
		t.Fatal("native failed settings task missing complete evidence", err)
	}

	syncPlan, err := lease.Prepare(failed.Snapshot, nativeSettingsEdit(t, failed.Snapshot, map[string]any{"rankingRules": []string{"invalidOwnedRule"}}), false)
	if err != nil {
		t.Fatal(err)
	}
	syncResult, err := lease.Apply(context.Background(), syncPlan)
	var diagnostic *contract.SettingsRequestError
	var status *HTTPError
	if !errors.As(err, &diagnostic) || !errors.As(err, &status) || status.Status != 400 || diagnostic.Raw == "" || syncResult.State != "rejected" {
		t.Fatal("native HTTP400 settings rejection lost complete diagnostic", err)
	}
	recoveredBefore, err := s.RecoverSettingsDocument(source.Object, lease)
	if err != nil {
		t.Fatal(err)
	}
	lease = recoveredBefore.Lease
	defer lease.Close()
	nativeWait(t, c, nativeSubmit(t, c, Request{Method: "DELETE", Path: "/indexes/" + uid}))
	body, _ := json.Marshal(map[string]any{"uid": uid, "primaryKey": "id"})
	nativeWait(t, c, nativeSubmit(t, c, Request{Method: "POST", Path: "/indexes", Body: body}))
	if _, err = lease.Read(context.Background()); !errors.Is(err, contract.ErrSettingsLeaseLost) || !errors.Is(err, contract.ErrSettingsConflict) {
		t.Fatal("original settings lease rebound recreated index", err)
	}
	recovery, err := s.RecoverSettingsDocument(source.Object, lease)
	if err != nil {
		t.Fatal(err)
	}
	defer recovery.Lease.Close()
	if _, err = recovery.Lease.Read(context.Background()); !errors.Is(err, contract.ErrSettingsLeaseLost) {
		t.Fatal("settings recovery erased original index incarnation", err)
	}
	t.Log("native settings conflict submits nothing; owned local model rejection task remains failed with complete native error; invalid ranking HTTP400 remains separate and reread settings; recreated index and recovery never rebind the original document")
}
func TestMeilisearchNativeSettingsLostCancelCloseNoReplay(t *testing.T) {
	for _, fault := range []string{"lost", "canceled", "closed"} {
		t.Run(fault, func(t *testing.T) {
			upstream := nativeOpen(t)
			uid := nativeIndex(t, upstream)
			var writes atomic.Int32
			admitted := make(chan Receipt, 1)
			release := make(chan struct{})
			relay := nativeRelay(t, upstream, func(w http.ResponseWriter, r *http.Request, response Response) bool {
				if r.Method != "PATCH" || !strings.HasSuffix(r.URL.Path, "/settings") {
					return false
				}
				writes.Add(1)
				receipt, e := parseReceipt(response.Body)
				if e != nil {
					t.Error("native fault relay missing original settings admission")
				}
				admitted <- receipt
				if fault != "lost" {
					<-r.Context().Done()
					<-release
				}
				if h, ok := w.(http.Hijacker); ok {
					conn, _, _ := h.Hijack()
					_ = conn.Close()
				}
				return true
			})
			s := testSession(t, relay.config, relay.secret)
			o := s.Namespace(uid).Object("settings", "settings")
			lease, err := s.ReserveSettingsDocument(o)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			source, err := lease.Read(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			plan, err := lease.Prepare(source, nativeSettingsEdit(t, source, map[string]any{"searchableAttributes": []string{"title"}}), false)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type answer struct {
				r contract.SettingsResult
				e error
			}
			out := make(chan answer, 1)
			go func() { r, e := lease.Apply(ctx, plan); out <- answer{r, e} }()
			receipt := <-admitted
			closed := make(chan struct{})
			if fault == "canceled" {
				cancel()
			}
			if fault == "closed" {
				go func() { _ = s.Close(); close(closed) }()
			}
			if fault != "lost" {
				close(release)
			}
			settled := <-out
			if settled.r.State != "uncertain" || !errors.Is(settled.e, contract.ErrSettingsLeaseLost) {
				t.Fatal("native original interrupted settings outcome", settled.e)
			}
			if fault == "closed" {
				<-closed
			}
			if _, err = lease.Apply(context.Background(), plan); err == nil || writes.Load() != 1 {
				t.Fatal("uncertain native settings preview replayed", err)
			}
			nativeWait(t, upstream, receipt)
			current := testSession(t, relay.config, relay.secret)
			recovery, err := current.RecoverSettingsDocument(o, lease)
			if err != nil || !recovery.Uncertain {
				t.Fatal("native uncertain settings recovery lost original outcome", err)
			}
			defer recovery.Lease.Close()
			observed, err := recovery.Lease.Read(context.Background())
			if err != nil || !strings.Contains(observed.Raw, "title") {
				t.Fatal("native admitted settings missing after interrupted original response", err)
			}
			if _, err = recovery.Lease.Prepare(observed, plan.Draft, false); !errors.Is(err, contract.ErrSettingsPlanUsed) || writes.Load() != 1 {
				t.Fatal("native recovery enabled unknown settings replay", err)
			}
		})
	}
}
func TestMeilisearchNativeSettingsScopedPermissions(t *testing.T) {
	master := nativeOpen(t)
	cfg, _ := nativeFixture(t)
	if cfg.Auth != "bearer" {
		t.Fatal("requires selected authenticated fixture")
	}
	uid := nativeIndex(t, master)
	other := nativeIndex(t, master)
	for _, write := range []bool{false, true} {
		t.Run(map[bool]string{false: "read-only", true: "settings-update"}[write], func(t *testing.T) {
			actions := []string{"indexes.get", "settings.get", "tasks.get"}
			if write {
				actions = append(actions, "settings.update")
			}
			body, _ := json.Marshal(map[string]any{"name": "owned-settings-scope", "indexes": []string{uid}, "actions": actions, "expiresAt": nil})
			fields, err := nativeObject(nativeRun(t, master, Request{Method: "POST", Path: "/keys", Body: body}).Body)
			key, e1 := nativeString(fields["key"])
			keyUID, e2 := nativeString(fields["uid"])
			if err != nil || e1 != nil || e2 != nil {
				t.Fatal("native settings scoped key unavailable")
			}
			cfg.IndexUID = uid
			s := testSession(t, cfg, key)
			o := s.Namespace(uid).Object("settings", "settings")
			if s.SupportsSettingsDocument(s.Namespace(other).Object("settings", "settings")) {
				t.Fatal("foreign index settings available")
			}
			lease, err := s.ReserveSettingsDocument(o)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			source, err := lease.Read(context.Background())
			if err != nil {
				t.Fatal("native scoped settings read", err)
			}
			desired := nativeSettingsEdit(t, source, map[string]any{"searchableAttributes": []string{"ownedScoped"}})
			plan, err := lease.Prepare(source, desired, false)
			if err != nil {
				t.Fatal(err)
			}
			result, err := lease.Apply(context.Background(), plan)
			if !write {
				var status *HTTPError
				var diagnostic *contract.SettingsRequestError
				if !errors.As(err, &status) || status.Status != 403 || !errors.As(err, &diagnostic) || diagnostic.Raw == "" || result.State != "rejected" {
					t.Fatal("native settings denied write lost original HTTP403", err)
				}
			} else {
				if err != nil || result.State != "enqueued" {
					t.Fatal("native scoped settings admission", err)
				}
				done, err := lease.Observe(context.Background(), result.Receipt, 30*time.Second)
				if err != nil || done.State != "succeeded" || done.Snapshot.Raw == "" {
					t.Fatal("native scoped original settings observation", err)
				}
			}
			nativeRun(t, master, Request{Method: "DELETE", Path: "/keys/" + keyUID})
			if write {
				_, err = lease.Read(context.Background())
				var status *HTTPError
				if !errors.As(err, &status) || status.Status != 403 {
					t.Fatal("revoked original settings key used fallback credentials", err)
				}
			}
		})
	}
}

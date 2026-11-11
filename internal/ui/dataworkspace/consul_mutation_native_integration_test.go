package dataworkspace

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/consul"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/locales"
)

func consulMutationCopy(t *testing.T, s *State, d *Document, preview bool) {
	t.Helper()
	want, label := d.nativeReceipt.JSON, "复制完整回执"
	if preview {
		want, label = d.mutation.snapshot.NativeJSON, "复制完整预览"
	} else {
		d.nativeReceipt.Open = true
		defer func() { d.nativeReceipt.Open = false }()
	}
	tt := ui.NewTester(func(c *ui.Context) { s.View(c, d); s.Modals(c) }, 1280, 800)
	tt.Frame()
	if err := tt.Click(locales.Text(label)); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != want {
		t.Fatal("copy omitted complete original preview/receipt")
	}
}

func consulMutationExport(t *testing.T, s *State, d *Document) {
	t.Helper()
	want := d.nativeReceipt.JSON
	path := filepath.Join(t.TempDir(), "original-receipt.json")
	s.exportNativeWriteReceipt(ui.Services{}, d, func(mygo.SaveDialogOptions) (string, error) { return path, nil })
	waitConsulGrid(t, s, func() bool { return d.nativeReceipt.Results == nil })
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != want || d.nativeReceipt.JSON != want {
		t.Fatal("export changed complete original receipt", err)
	}
}

func waitConsulMutation(t *testing.T, s *State, d *Document) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for d.Busy() || d.loading {
		s.Consume(ui.Services{})
		if d.mutation.open && d.mutation.results == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("original Consul mutation worker did not settle", d.edit.Err)
		}
		time.Sleep(time.Millisecond)
	}
}

func consulMutationState(t *testing.T, p connections.Profile, secret string) (*dbruntime.Manager, *State, *Document, *Session, contract.KVRequestLease) {
	t.Helper()
	m := dbruntime.New()
	dbruntime.RegisterBuiltins(m, nil)
	t.Cleanup(func() { m.Close() })
	h, err := m.ConnectSession(context.Background(), p, secret)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := m.KVRequests(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := provider.ReserveKV(99)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { writer.Close() })
	active := &Session{ProfileID: p.ID, Generation: h.Generation(), Connected: true}
	s := New(Dependencies{Session: func() Session { return *active }, SessionFor: func(id int64) Session {
		return Session{ProfileID: id, Generation: active.Generation, Connected: active.Connected}
	}, RuntimeFor: func(id int64) Runtime { return m.Data(id) }, Inspector: InspectorDependencies{Session: func() Session { return *active }, Profile: func(id int64) (connections.Profile, bool) { return p, id == p.ID }}})
	t.Cleanup(s.CloseAll)
	d := s.OpenObject(1, m.Namespace(p.ID, "kv").Object("keyspace", "keys"), h.Capabilities())
	d.limit = 100
	d.edit.Batch = true
	return m, s, d, active, writer
}

func consulMutationLoad(t *testing.T, s *State, d *Document, refresh bool) {
	t.Helper()
	s.Load(ui.Services{}, d, refresh)
	waitConsulMutation(t, s, d)
	if d.err != "" || d.page == nil {
		t.Fatal("original public mutation page", d.err)
	}
}

func consulMutationEdit(t *testing.T, s *State, d *Document, row int, field string, value contract.DataValue) {
	t.Helper()
	source := slices.Index(d.page.Columns, field)
	if source < 0 || !mysqlDataCanEdit(d, row, source) {
		t.Fatal("original editable field unavailable", field)
	}
	s.finishMySQLCellEdit(d, false)
	s.beginMySQLCellEdit(ui.Services{}, d, row, source, true)
	waitConsulMutation(t, s, d)
	if d.edit.Editor == nil {
		t.Fatal("complete original editor unavailable", field, d.edit.Err)
	}
	d.edit.Editor.Kind = typedKindLabel(value.Kind)
	d.edit.Editor.Text = value.Text
	if value.Kind == contract.Binary {
		d.edit.Editor.Text = "0x" + hex.EncodeToString(value.Bytes)
	}
	s.finishMySQLCellEdit(d, true)
	if d.edit.Editor != nil {
		t.Fatal("original native field edit rejected", field, d.edit.Err)
	}
}

func consulMutationPreview(t *testing.T, s *State, d *Document) {
	t.Helper()
	s.saveMySQLData(ui.Services{}, d)
	waitConsulMutation(t, s, d)
	if !d.mutation.open || d.mutation.plan == nil || d.edit.Err != "" {
		t.Fatal("original immutable Consul grid confirmation missing", d.edit.Err)
	}
	if !strings.Contains(d.mutation.snapshot.NativeJSON, `"Operations"`) || !strings.Contains(d.mutation.snapshot.NativeJSON, `"BeforeImages"`) {
		t.Fatal("confirmation lost complete original native parameters")
	}
}

func consulMutationRead(t *testing.T, writer contract.KVRequestLease, key string) contract.KVResult {
	t.Helper()
	native, err := consul.KeyReadRequest(key)
	if err != nil {
		t.Fatal(err)
	}
	r, err := consul.DocumentRequest(native)
	if err != nil {
		t.Fatal(err)
	}
	p, err := writer.Prepare(r)
	if err != nil {
		t.Fatal(err)
	}
	out, err := writer.Execute(context.Background(), p)
	if err != nil && !(out.HTTP != nil && out.HTTP.Status == 409 && out.Completed) {
		t.Fatal("original complete native verification", err)
	}
	return out
}

func consulMutationExecute(t *testing.T, writer contract.KVRequestLease, native consul.Request) contract.KVResult {
	t.Helper()
	r, err := consul.DocumentRequest(native)
	if err != nil {
		t.Fatal(err)
	}
	p, err := writer.Prepare(r)
	if err != nil {
		t.Fatal(err)
	}
	out, err := writer.Execute(context.Background(), p)
	if err != nil || !out.Completed || out.HTTP == nil || !out.HTTP.Succeeded {
		t.Fatal("original native mutation fixture executor", err)
	}
	return out
}

func TestConsulNativePublicGridMutationOriginalConfirmationBytesFlagsRenameInsertDeleteAndParking(t *testing.T) {
	p, secret := consulGridProfile(t)
	p.ConsulOptions.Prefix = "native/public-mutations//" + t.Name() + "/中文/"
	m, s, d, active, writer := consulMutationState(t, p, secret)
	key := p.ConsulOptions.Prefix + "original"
	value := bytes.Repeat([]byte{0, 255}, 256<<10)
	consulGridPut(t, writer, key, value)
	consulMutationLoad(t, s, d, false)
	if !d.editable() || !s.runtimeFor(d).NativeReceipts || s.runtimeFor(d).PreviewEdits == nil {
		t.Fatal("public original edit capability unavailable")
	}
	for i, c := range d.page.ColumnMetadata {
		if mysqlDataCanEdit(d, 0, i) == c.Generated {
			t.Fatal("generated native field edit authority", c.Name)
		}
	}
	sibling := s.OpenObject(2, d.object, d.capabilities)
	sibling.limit = 100
	consulMutationLoad(t, s, sibling, false)
	cached := bytes.Clone(sibling.page.Rows[0][1].Value.Bytes)
	consulMutationEdit(t, s, d, 0, "value", contract.DataValue{Kind: contract.Binary, Bytes: []byte{255, 0, 7}})
	consulMutationEdit(t, s, d, 0, "flags", contract.DataValue{Kind: contract.Integer, Text: "9007199254740993"})
	captureConsulGrid(t, s, d, "mutation-draft")
	consulMutationPreview(t, s, d)
	consulMutationCopy(t, s, d, true)
	if len(d.mutation.snapshot.NativeJSON) <= 64<<10 {
		t.Fatal("512 KiB original before-image was not preserved beyond display preview")
	}
	captureConsulGrid(t, s, d, "mutation-confirmation")
	snapshot := d.mutation.plan.Snapshot()
	snapshot.Edits[0].Values["value"].Bytes[0] = 42
	if !reflect.DeepEqual(d.mutation.plan.Snapshot(), d.mutation.snapshot) {
		t.Fatal("inspection copy changed original confirmation")
	}
	s.cancelDataMutation(d)
	if !d.HasDraft() || d.mutation.open {
		t.Fatal("confirmation cancel discarded drafts")
	}
	consulMutationPreview(t, s, d)
	s.confirmDataMutation(ui.Services{}, d)
	active.ProfileID = p.ID + 1
	select {
	case completion := <-d.edit.Results:
		channel := make(chan mysqlEditResult, 1)
		channel <- completion
		d.edit.Results = channel
		if completion.Err != nil || completion.Saved == nil || !completion.Saved.CommitConfirmed {
			t.Fatal("original Consul UI write failed", completion.Err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("original parked write did not finish")
	}
	s.Consume(ui.Services{})
	if !d.HasDraft() || !d.edit.Busy || d.nativeReceipt.JSON != "" {
		t.Fatal("parked write completion was consumed by a different profile")
	}
	active.ProfileID = p.ID
	waitConsulMutation(t, s, d)
	if d.HasDraft() || d.edit.Uncertain != nil || d.nativeReceipt.JSON == "" || !bytes.Equal(d.page.Rows[0][1].Value.Bytes, []byte{255, 0, 7}) || !bytes.Equal(sibling.page.Rows[0][1].Value.Bytes, cached) {
		t.Fatal("original confirmed refresh replaced drafts or sibling observation", d.edit.Err)
	}
	captureConsulGrid(t, s, d, "mutation-saved")
	consulMutationCopy(t, s, d, false)
	consulMutationExport(t, s, d)
	rename := p.ConsulOptions.Prefix + "renamed//"
	consulMutationEdit(t, s, d, 0, "key", contract.DataValue{Kind: contract.Text, Text: rename})
	consulMutationPreview(t, s, d)
	s.confirmDataMutation(ui.Services{}, d)
	waitConsulMutation(t, s, d)
	if d.HasDraft() || d.page.Rows[0][0].Value.Text != rename {
		t.Fatal("native renamed identity did not replace original", d.edit.Err)
	}
	captureConsulGrid(t, s, d, "mutation-renamed")
	s.addMySQLDataRecord(ui.Services{}, d)
	row := len(d.page.Rows) - 1
	consulMutationEdit(t, s, d, row, "key", contract.DataValue{Kind: contract.Text, Text: p.ConsulOptions.Prefix + "empty"})
	consulMutationEdit(t, s, d, row, "value", contract.DataValue{Kind: contract.Binary})
	consulMutationPreview(t, s, d)
	s.confirmDataMutation(ui.Services{}, d)
	waitConsulMutation(t, s, d)
	if d.HasDraft() || len(d.page.Rows) != 2 {
		t.Fatal("explicit native empty insert did not settle", d.edit.Err)
	}
	captureConsulGrid(t, s, d, "mutation-inserted")
	d.selection.Clear()
	d.selection.Add(0)
	d.selection.Add(1)
	s.deleteMySQLDataRecords(ui.Services{}, d, 0)
	consulMutationPreview(t, s, d)
	captureConsulGrid(t, s, d, "mutation-delete-confirmation")
	s.confirmDataMutation(ui.Services{}, d)
	waitConsulMutation(t, s, d)
	if d.HasDraft() || len(d.page.Rows) != 0 {
		t.Fatal("atomic selected-key delete did not settle", d.edit.Err)
	}
	captureConsulGrid(t, s, d, "mutation-deleted")
	old := s.runtimeFor(sibling)
	m.Disconnect(p.ID)
	fresh, err := m.ConnectSession(context.Background(), p, secret)
	if err != nil {
		t.Fatal(err)
	}
	active.Generation = fresh.Generation()
	if _, err := old.RowReader(context.Background(), p.ID, "kv", "keys", *sibling.page.RowRefs[0]); err == nil {
		t.Fatal("original generation row survived replacement")
	}
	s.Close(sibling)
	if !m.IsConnected(p.ID) {
		t.Fatal("original document close disconnected replacement")
	}
}

func TestConsulNativePublicGridMutationConflictKeepsOriginalDraftsAndLockedRowsReadOnly(t *testing.T) {
	p, secret := consulGridProfile(t)
	p.ConsulOptions.Prefix = "native/public-mutation-conflict/" + t.Name() + "/"
	_, s, d, _, writer := consulMutationState(t, p, secret)
	key := p.ConsulOptions.Prefix + "key"
	consulGridPut(t, writer, key, []byte("original"))
	consulMutationLoad(t, s, d, false)
	consulMutationEdit(t, s, d, 0, "value", contract.DataValue{Kind: contract.Binary, Bytes: []byte("draft")})
	consulMutationPreview(t, s, d)
	consulGridPut(t, writer, key, []byte("external"))
	s.confirmDataMutation(ui.Services{}, d)
	waitConsulMutation(t, s, d)
	if !d.HasDraft() || d.edit.Uncertain != nil || d.edit.Err == "" || d.nativeReceipt.JSON == "" {
		t.Fatal("confirmed original conflict discarded drafts or invented unknown", d.edit.Err)
	}
	captureConsulGrid(t, s, d, "mutation-conflict")
	r := consulMutationRead(t, writer, key)
	if r.HTTP == nil || len(r.HTTP.Entries) != 1 || !bytes.Equal(r.HTTP.Entries[0].Value, []byte("external")) {
		t.Fatal("conflicted UI write altered foreign observation")
	}
	if !s.GuardLeave(d, func(ui.Services) { t.Fatal("unresolved original draft left without decision") }) {
		t.Fatal("conflicted draft lost leave guard")
	}
	create := consulMutationExecute(t, writer, consul.Request{Method: "PUT", Path: "/v1/session/create", Body: []byte(`{"Name":"owned grid lock","TTL":"10s","LockDelay":"0s","Behavior":"release","NodeChecks":[]}`)})
	id := create.HTTP.SessionID
	value := []byte("external")
	lock, err := consul.TxnRequest([]consul.TxnOperation{{KV: &consul.TxnKV{Verb: "lock", Key: key, Value: &value, Session: &id}}})
	if err != nil {
		t.Fatal(err)
	}
	consulMutationExecute(t, writer, lock)
	locked := s.OpenObject(3, d.object, d.capabilities)
	locked.limit = 100
	locked.edit.Batch = true
	consulMutationLoad(t, s, locked, false)
	if !locked.page.ReadOnlyRows[0] {
		t.Fatal("original Session-locked row did not publish read-only state")
	}
	for i := range locked.page.Columns {
		if mysqlDataCanEdit(locked, 0, i) {
			t.Fatal("original locked native field remained editable", locked.page.Columns[i])
		}
	}
	s.deleteMySQLDataRecords(ui.Services{}, locked, 0)
	if locked.HasDraft() || locked.edit.Err == "" {
		t.Fatal("original locked row deletion was staged")
	}
	captureConsulGrid(t, s, locked, "mutation-session-locked")
	release, err := consul.TxnRequest([]consul.TxnOperation{{KV: &consul.TxnKV{Verb: "unlock", Key: key, Value: &value, Session: &id}}})
	if err != nil {
		t.Fatal(err)
	}
	consulMutationExecute(t, writer, release)
	consulMutationLoad(t, s, locked, true)
	if locked.page.ReadOnlyRows[0] {
		t.Fatal("explicit original unlock did not settle after refresh")
	}
	consulMutationEdit(t, s, locked, 0, "flags", contract.DataValue{Kind: contract.Integer, Text: "9007199254740993"})
	consulMutationPreview(t, s, locked)
	s.confirmDataMutation(ui.Services{}, locked)
	waitConsulMutation(t, s, locked)
	if locked.HasDraft() || locked.edit.Uncertain != nil {
		t.Fatal("explicit unlocked original write failed", locked.edit.Err)
	}
}

// Faults are armed only after the immutable original UI preview exists. The
// upstream receives and commits the transaction before this relay loses its ACK.
func consulMutationRelay(t *testing.T, p connections.Profile, secret, mode string, armed *atomic.Bool) (connections.Profile, *atomic.Int32, <-chan struct{}) {
	t.Helper()
	client, original := consulGridOriginalHTTP(t, p)
	calls := &atomic.Int32{}
	processed := make(chan struct{})
	var notified atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, incoming *http.Request) {
		raw, err := io.ReadAll(incoming.Body)
		if err != nil {
			return
		}
		request, err := http.NewRequestWithContext(incoming.Context(), incoming.Method, original+incoming.URL.RequestURI(), bytes.NewReader(raw))
		if err != nil {
			t.Error(err)
			return
		}
		request.Header = incoming.Header.Clone()
		response, err := client.Do(request)
		if err != nil {
			return
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Error(err)
			return
		}
		for key, values := range response.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		write := false
		if incoming.URL.Path == "/v1/txn" {
			var ops []consul.TxnOperation
			if json.Unmarshal(raw, &ops) == nil {
				for _, op := range ops {
					if op.KV != nil && (op.KV.Verb == "cas" || op.KV.Verb == "delete-cas") {
						write = true
					}
				}
			}
		}
		if armed.Load() && write {
			calls.Add(1)
		}
		if armed.Load() && write && response.StatusCode == 200 && notified.CompareAndSwap(false, true) {
			close(processed)
			switch mode {
			case "lost":
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					conn.Close()
				}
				return
			case "partial":
				w.Header().Set("Content-Length", strconv.Itoa(len(body)+50))
				w.WriteHeader(200)
				w.Write(body[:min(20, len(body))])
				return
			case "cancel", "close", "replace":
				<-incoming.Context().Done()
				return
			case "missing":
				body = []byte(`{"Results":null,"Errors":null}`)
			case "http500", "http408":
				w.Header().Del("Content-Length")
				status := 500
				if mode == "http408" {
					status = 408
				}
				w.WriteHeader(status)
				w.Write(body)
				return
			case "bad-index":
				w.Header().Set("X-Consul-Index", "unavailable-original-index")
			case "bad-leader":
				w.Header().Set("X-Consul-Knownleader", "unavailable-original-leader")
			}
		}
		w.Header().Del("Content-Length")
		w.WriteHeader(response.StatusCode)
		w.Write(body)
	}))
	t.Cleanup(server.Close)
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	p.Host, p.Port, p.TLSMode = u.Hostname(), uint16(port), "disable"
	p.ConsulOptions.CAFile, p.ConsulOptions.ClientCertFile, p.ConsulOptions.ClientKeyFile = "", "", ""
	return p, calls, processed
}

func TestConsulNativePublicGridMutationProcessedReplyFaultsParkingGenerationLeaveAndNoReplay(t *testing.T) {
	base, secret := consulGridProfile(t)
	for _, mode := range []string{"lost", "partial", "missing", "http500", "http408", "cancel", "close", "replace", "parked"} {
		t.Run(mode, func(t *testing.T) {
			p := base
			p.ConsulOptions.Prefix = "native/public-grid-write-fault/" + strings.ReplaceAll(t.Name(), "/", "-") + "/"
			_, _, _, _, observer := consulMutationState(t, p, secret)
			key := p.ConsulOptions.Prefix + "key"
			consulGridPut(t, observer, key, []byte("original"))
			armed := &atomic.Bool{}
			fault := mode
			if mode == "parked" {
				fault = "lost"
			}
			proxy, calls, processed := consulMutationRelay(t, p, secret, fault, armed)
			m, s, d, active, _ := consulMutationState(t, proxy, secret)
			consulMutationLoad(t, s, d, false)
			consulMutationEdit(t, s, d, 0, "value", contract.DataValue{Kind: contract.Binary, Bytes: []byte{0, 255, 7}})
			consulMutationPreview(t, s, d)
			armed.Store(true)
			s.confirmDataMutation(ui.Services{}, d)
			results := d.edit.Results
			if mode == "parked" {
				active.ProfileID = p.ID + 1
			}
			select {
			case <-processed:
			case <-time.After(20 * time.Second):
				t.Fatal("original upstream UI transaction did not finish")
			}
			switch mode {
			case "cancel":
				d.edit.Cancel()
			case "close":
				if !s.GuardLeave(d, func(ui.Services) { t.Fatal("pending original write left without acknowledgement") }) {
					t.Fatal("pending original write lost leave guard")
				}
				s.Close(d)
			case "replace":
				m.Disconnect(p.ID)
				fresh, err := m.ConnectSession(context.Background(), proxy, secret)
				if err != nil {
					t.Fatal(err)
				}
				active.Generation = fresh.Generation()
			}
			var completion mysqlEditResult
			select {
			case completion = <-results:
			case <-time.After(20 * time.Second):
				t.Fatal("original UI write executor did not acknowledge cancellation")
			}
			if completion.Saved == nil || completion.Saved.CommitConfirmed || !(completion.Saved.CommitUncertain || completion.Saved.Incomplete) || completion.Saved.NativeJSON == "" {
				t.Fatal("lost original UI receipt promoted success", completion.Err)
			}
			if mode != "close" {
				channel := make(chan mysqlEditResult, 1)
				channel <- completion
				d.edit.Results = channel
				if mode == "parked" {
					s.Consume(ui.Services{})
					if !d.edit.Busy || d.nativeReceipt.JSON != "" || !d.HasDraft() {
						t.Fatal("foreign profile consumed original unknown write")
					}
					active.ProfileID = p.ID
				}
				waitConsulMutation(t, s, d)
				if !d.HasDraft() || d.edit.Uncertain == nil || d.nativeReceipt.JSON == "" {
					t.Fatal("unknown original UI write lost drafts or receipt", d.edit.Err)
				}
				original := d.nativeReceipt.JSON
				consulMutationCopy(t, s, d, false)
				consulMutationExport(t, s, d)
				captureConsulGrid(t, s, d, "mutation-unknown-"+mode)
				if mode != "replace" {
					if _, err := s.runtimeFor(d).Count(context.Background(), p.ID, "kv", "keys"); err != nil {
						t.Fatal("independent auxiliary read blocked", err)
					}
				}
				if d.nativeReceipt.JSON != original {
					t.Fatal("later read replaced original unknown native receipt")
				}
				s.saveMySQLData(ui.Services{}, d)
				if d.mutation.open || !d.HasDraft() || !s.GuardLeave(d, func(ui.Services) { t.Fatal("unknown original write left without decision") }) {
					t.Fatal("unknown original write did not retain replay/leave barrier")
				}
				captureConsulGrid(t, s, d, "mutation-after-read-"+mode)
			}
			actual := consulMutationRead(t, observer, key)
			if actual.HTTP == nil || len(actual.HTTP.Entries) != 1 || !bytes.Equal(actual.HTTP.Entries[0].Value, []byte{0, 255, 7}) {
				t.Fatal("UI fault was not injected after the genuine native write")
			}
			if calls.Load() != 1 {
				t.Fatal("original UI transaction replayed", calls.Load())
			}
			s.Close(d)
			if d.runtimeLease != nil || d.nativeReceipt.JSON != "" {
				t.Fatal("terminal close retained original caches or lease")
			}
		})
	}
}

func TestConsulNativePublicGridMutationOptionalMetadataWarningsKeepVerifiedOriginalCommit(t *testing.T) {
	base, secret := consulGridProfile(t)
	for _, mode := range []string{"bad-index", "bad-leader"} {
		t.Run(mode, func(t *testing.T) {
			p := base
			p.ConsulOptions.Prefix = "native/public-grid-metadata-warning/" + strings.ReplaceAll(t.Name(), "/", "-") + "/"
			_, _, _, _, observer := consulMutationState(t, p, secret)
			key := p.ConsulOptions.Prefix + "key"
			consulGridPut(t, observer, key, []byte("original"))
			armed := &atomic.Bool{}
			proxy, calls, processed := consulMutationRelay(t, p, secret, mode, armed)
			_, s, d, _, _ := consulMutationState(t, proxy, secret)
			consulMutationLoad(t, s, d, false)
			consulMutationEdit(t, s, d, 0, "value", contract.DataValue{Kind: contract.Binary, Bytes: []byte{0, 255}})
			consulMutationPreview(t, s, d)
			armed.Store(true)
			s.confirmDataMutation(ui.Services{}, d)
			select {
			case <-processed:
			case <-time.After(20 * time.Second):
				t.Fatal("original metadata warning write did not finish")
			}
			waitConsulMutation(t, s, d)
			if d.HasDraft() || d.edit.Uncertain != nil || d.nativeReceipt.JSON == "" || !bytes.Equal(d.page.Rows[0][1].Value.Bytes, []byte{0, 255}) {
				t.Fatal("optional metadata warning invalidated complete original commit", d.edit.Err)
			}
			var receipt contract.KVResult
			if json.Unmarshal([]byte(d.nativeReceipt.JSON), &receipt) != nil || !receipt.Completed || receipt.WriteOutcomeUnknown || receipt.HTTP == nil || !receipt.HTTP.Acknowledged || !receipt.HTTP.Succeeded || receipt.ProjectionError == "" {
				t.Fatal("optional warning replaced original transport ACK")
			}
			captureConsulGrid(t, s, d, "mutation-warning-"+mode)
			consulMutationEdit(t, s, d, 0, "flags", contract.DataValue{Kind: contract.Integer, Text: "9007199254740993"})
			consulMutationPreview(t, s, d)
			s.confirmDataMutation(ui.Services{}, d)
			waitConsulMutation(t, s, d)
			if d.HasDraft() || calls.Load() != 2 {
				t.Fatal("complete original commit invented a later write barrier", d.edit.Err)
			}
		})
	}
}

func TestConsulNativePublicGridMutationBoundsOverlapAndDestinationConflictsNeverPartiallyWrite(t *testing.T) {
	p, secret := consulGridProfile(t)
	p.ConsulOptions.Prefix = "native/public-grid-mutation-bounds/" + t.Name() + "/"
	_, _, _, _, observer := consulMutationState(t, p, secret)
	original, destination := p.ConsulOptions.Prefix+"a", p.ConsulOptions.Prefix+"b"
	consulGridPut(t, observer, original, []byte("original"))
	consulGridPut(t, observer, destination, []byte("destination"))
	armed := &atomic.Bool{}
	proxy, calls, _ := consulMutationRelay(t, p, secret, "observe", armed)
	_, s, d, _, _ := consulMutationState(t, proxy, secret)
	consulMutationLoad(t, s, d, false)
	armed.Store(true)
	// A prefix violation is rejected while preserving the UI draft, before CAS.
	consulMutationEdit(t, s, d, 0, "key", contract.DataValue{Kind: contract.Text, Text: "foreign/key"})
	s.saveMySQLData(ui.Services{}, d)
	waitConsulMutation(t, s, d)
	if d.mutation.plan != nil || d.edit.Err == "" || !d.HasDraft() || calls.Load() != 0 {
		t.Fatal("foreign destination wrote or lost draft")
	}
	s.discardMySQLData(d)
	// Native destination-absence conflicts reject both rename and sibling edit.
	consulMutationEdit(t, s, d, 0, "key", contract.DataValue{Kind: contract.Text, Text: destination})
	consulMutationEdit(t, s, d, 1, "value", contract.DataValue{Kind: contract.Binary, Bytes: []byte("attempted")})
	s.saveMySQLData(ui.Services{}, d)
	waitConsulMutation(t, s, d)
	if d.mutation.plan != nil || d.edit.Err == "" || calls.Load() != 0 || !d.HasDraft() {
		t.Fatal("overlapping batch authorized native write")
	}
	s.discardMySQLData(d)
	consulMutationEdit(t, s, d, 0, "key", contract.DataValue{Kind: contract.Text, Text: destination})
	s.saveMySQLData(ui.Services{}, d)
	waitConsulMutation(t, s, d)
	if d.mutation.plan != nil || d.edit.Err == "" || calls.Load() != 0 {
		t.Fatal("existing destination preview was authorized")
	}
	s.discardMySQLData(d)
	// Repeated insert identities and a >64-operation batch remain local errors.
	late := p.ConsulOptions.Prefix + "late-destination"
	consulMutationEdit(t, s, d, 0, "key", contract.DataValue{Kind: contract.Text, Text: late})
	consulMutationEdit(t, s, d, 1, "value", contract.DataValue{Kind: contract.Binary, Bytes: []byte("attempted")})
	consulMutationPreview(t, s, d)
	consulGridPut(t, observer, late, []byte("concurrent"))
	s.confirmDataMutation(ui.Services{}, d)
	waitConsulMutation(t, s, d)
	if !d.HasDraft() || d.edit.Uncertain != nil || d.edit.Err == "" || d.nativeReceipt.JSON == "" || calls.Load() != 1 {
		t.Fatal("late destination conflict did not retain original atomic failure", d.edit.Err)
	}
	captureConsulGrid(t, s, d, "mutation-destination-conflict")
	s.discardMySQLData(d)
	for i := 0; i < 2; i++ {
		s.addMySQLDataRecord(ui.Services{}, d)
		row := len(d.page.Rows) - 1
		consulMutationEdit(t, s, d, row, "key", contract.DataValue{Kind: contract.Text, Text: p.ConsulOptions.Prefix + "duplicate"})
		consulMutationEdit(t, s, d, row, "value", contract.DataValue{Kind: contract.Binary})
	}
	s.saveMySQLData(ui.Services{}, d)
	waitConsulMutation(t, s, d)
	if d.mutation.plan != nil || d.edit.Err == "" || calls.Load() != 1 {
		t.Fatal("duplicate UI insert authorized write")
	}
	s.discardMySQLData(d)
	for i := 0; i < 22; i++ {
		s.addMySQLDataRecord(ui.Services{}, d)
		row := len(d.page.Rows) - 1
		consulMutationEdit(t, s, d, row, "key", contract.DataValue{Kind: contract.Text, Text: p.ConsulOptions.Prefix + strconv.Itoa(i)})
		consulMutationEdit(t, s, d, row, "value", contract.DataValue{Kind: contract.Binary})
	}
	s.saveMySQLData(ui.Services{}, d)
	waitConsulMutation(t, s, d)
	if d.mutation.plan != nil || d.edit.Err == "" || calls.Load() != 1 {
		t.Fatal("oversized UI batch split or wrote")
	}
	captureConsulGrid(t, s, d, "mutation-bounds")
	for key, want := range map[string]string{original: "original", destination: "destination"} {
		actual := consulMutationRead(t, observer, key)
		if actual.HTTP == nil || len(actual.HTTP.Entries) != 1 || string(actual.HTTP.Entries[0].Value) != want {
			t.Fatal("invalid UI batch partially changed original service")
		}
	}
}

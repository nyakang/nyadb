package kvworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/consul"
)

func consulHeldKeysAcquire(t *testing.T, writer contract.KVRequestLease, id, key string) {
	t.Helper()
	flags := uint64(math.MaxUint64)
	r, err := consul.KeyPutRequest(key, []byte{0, 255, 128}, &flags, nil)
	if err != nil || r.Method != "PUT" {
		t.Fatal("original held-key fixture requires canonical HTTP key", err)
	}
	r.Query.Set("acquire", id)
	consulRenewalDo(t, writer, r)
	t.Cleanup(func() {
		r, err := consul.KeyDeleteRequest(key, nil, false)
		if err == nil {
			consulRenewalDo(t, writer, r)
		}
	})
}

func nativeSessionHeldKeysCapture(t *testing.T, s *State, d *Document, stage string) {
	t.Helper()
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			tt := workspaceTester(s, d, size[0], size[1])
			tt.Frame()
			labels := []string{"复制完整操作记录"}
			if d.httpSessionKeys {
				labels = []string{"返回原 HTTP 回执"}
				if d.httpSessionKeysBrowser != nil && d.httpSessionKeysBrowser.detail {
					labels = append(labels, "返回持有键列表", "持有键原始详情")
				} else {
					labels = append(labels, "筛选已返回的持有键", "查看所选原始详情")
				}
			} else if d.httpSessions {
				labels = []string{"读取所选 Session 的持有键（全根目录）", "返回原 HTTP 回执"}
			}
			assertHTTPSessionRenewalControls(t, tt, size, labels...)
			captureSessionHeldKeys(t, tt, language, size, stage)
		}
	}
}

func consulSelectHeldKeysSession(t *testing.T, s *State, d *Document, id string) {
	t.Helper()
	consulWorkspaceToolInput(t, s, d, contract.KVToolHTTPSessionList, contract.KVRequestToolInput{})
	consulWorkspaceRunTool(t, s, d, false)
	tt := workspaceTester(s, d, 720, 520)
	workspaceClick(t, tt, "Session 列表")
	workspaceClick(t, tt, "筛选已返回的 Session")
	tt.Type(id)
	tt.Frame()
	workspaceClick(t, tt, id)
}

func TestConsulNativePublicSessionHeldKeysOriginalWholeRootPagesDetailsAndRecovery(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	writer := consulRenewalWriter(t, p, secret)
	id := consulSessionToolCreate(t, writer, t.Name())
	for i := range 53 {
		consulHeldKeysAcquire(t, writer, id, fmt.Sprintf("outside-held/%s/key-%03d", t.Name(), i))
	}
	s, d, m := consulWorkspaceState(t, p, secret)
	if !d.HTTPSessionKeysAvailable() {
		t.Fatal("public original runtime lease lost held-key facet")
	}
	consulSelectHeldKeysSession(t, s, d, id)
	request := d.Request()
	nativeSessionHeldKeysCapture(t, s, d, "native-selected-session")
	s.ObserveSelectedHTTPSessionKeys(d)
	consume(t, s, d)
	if d.last == nil || d.last.HTTPSessionKeys == nil || !d.last.HTTPSessionKeys.Complete || len(d.last.HTTPSessionKeys.Matches) != 53 || d.last.HTTPSessionKeys.Request.ID != id || !d.httpSessionKeys || d.Request() != request || d.last.Scope.Generation != m.Generation(p.ID) {
		t.Fatal("public original full-root observation changed UUID, scope or text", d.Error())
	}
	h := d.last.Result.HTTP
	if h.Request.Path != "/v1/kv/" || len(h.Request.Query) != 1 || h.Protocol != "HTTP/1.1" && h.Protocol != "HTTP/2.0" || h.Request.Query["recurse"] == nil {
		t.Fatal("public observation narrowed configured prefix or invented a UUID query")
	}
	for _, index := range d.last.HTTPSessionKeys.Matches {
		row := h.Entries[index]
		if row.Session != id || row.Flags != math.MaxUint64 || row.ModifyIndex == 0 || !bytes.Equal(row.Value, []byte{0, 255, 128}) {
			t.Fatal("original native held-key identity/bytes/unsigned fields changed")
		}
	}
	nativeSessionHeldKeysCapture(t, s, d, "native-held-keys-list")
	tt := workspaceTester(s, d, 720, 520)
	workspaceClick(t, tt, "下一页")
	key := fmt.Sprintf("outside-held/%s/key-052", t.Name())
	workspaceClick(t, tt, key)
	workspaceClick(t, tt, "复制所选持有键名称")
	if tt.Clipboard() != key {
		t.Fatal("original native last-page key copy changed")
	}
	nativeSessionHeldKeysCapture(t, s, d, "native-held-last-page")
	workspaceClick(t, tt, "查看所选原始详情")
	nativeSessionHeldKeysCapture(t, s, d, "native-held-key-detail")
	workspaceClick(t, tt, "返回原 HTTP 回执")
	nativeSessionHeldKeysCapture(t, s, d, "native-original-held-receipt")
	record, err := s.OperationRecord(d)
	if err != nil {
		t.Fatal(err)
	}
	var exported struct {
		HTTP struct{ Body []byte }
		Keys *HTTPSessionKeysReceipt `json:"session_held_keys"`
	}
	if json.Unmarshal(record, &exported) != nil || exported.Keys == nil || exported.Keys.Request.ID != id || !bytes.Equal(exported.HTTP.Body, h.Body) {
		t.Fatal("public export changed original entire root evidence or target")
	}
	old := d.scope.Generation
	if err := m.Disconnect(p.ID); err != nil {
		t.Fatal(err)
	}
	s.DisconnectProfile(p.ID)
	if err := m.Connect(context.Background(), p, secret); err != nil {
		t.Fatal(err)
	}
	s.Recover(d)
	consume(t, s, d)
	after, err := s.OperationRecord(d)
	if err != nil || d.needsRecovery || d.scope.Generation <= old || d.last.Scope.Generation != old || !bytes.Equal(record, after) || d.Request() != request {
		t.Fatal("public recovery replayed or relabeled original whole-root evidence", err)
	}
}

func TestConsulNativePublicSessionHeldKeysOriginalUnknownDestroyAndACLBoundary(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	writer := consulRenewalWriter(t, p, secret)
	id := consulSessionToolCreate(t, writer, t.Name()+"-unknown")
	survivor := consulSessionToolCreate(t, writer, t.Name()+"-survivor")
	consulHeldKeysAcquire(t, writer, survivor, "allowed/held/"+t.Name())
	consulHeldKeysAcquire(t, writer, survivor, "outside-held/"+t.Name())
	proxy, calls, processed := consulSessionToolFault(t, p, "lost")
	s, d, _ := consulWorkspaceState(t, proxy, secret)
	input := contract.KVRequestToolInput{HTTPSession: contract.KVHTTPSessionToolInput{ID: id}}
	consulWorkspaceToolInput(t, s, d, contract.KVToolHTTPSessionDestroy, input)
	s.Run(d)
	s.Confirm(d, true)
	await(t, processed)
	consume(t, s, d)
	if d.unknown == nil || !d.unknown.Result.WriteOutcomeUnknown || calls.Load() != 1 {
		t.Fatal("original processed destroy was acknowledged or replayed")
	}
	before, err := encodeHTTPOperationReceipt(*d.unknown)
	if err != nil {
		t.Fatal(err)
	}
	s.Recover(d)
	consume(t, s, d)
	if d.needsRecovery || calls.Load() != 1 {
		t.Fatal("public recovery replayed original unknown destroy")
	}
	consulSelectHeldKeysSession(t, s, d, survivor)
	s.ObserveSelectedHTTPSessionKeys(d)
	consume(t, s, d)
	if d.last.HTTPSessionKeys == nil || !d.last.HTTPSessionKeys.Complete || len(d.last.HTTPSessionKeys.Matches) != 2 || !d.hasUnknownWrites() {
		t.Fatal("later complete root observation promoted earlier unknown destroy")
	}
	nativeSessionHeldKeysCapture(t, s, d, "native-held-after-unknown")
	tt := workspaceTester(s, d, 720, 520)
	workspaceClick(t, tt, "返回原 HTTP 回执")
	d.selectedUnknown = true
	tt.Frame()
	nativeSessionHeldKeysCapture(t, s, d, "native-original-unknown-destroy")
	after, err := encodeHTTPOperationReceipt(*d.unknown)
	if err != nil || !bytes.Equal(before, after) || calls.Load() != 1 {
		t.Fatal("browsing changed original unknown write evidence", err)
	}
	d.selectedUnknown = false
	consulWorkspaceToolInput(t, s, d, contract.KVToolHTTPSessionDestroy, input)
	s.Run(d)
	if d.Busy() || d.confirmation != nil || calls.Load() != 1 {
		t.Fatal("held-key observation authorized replay of unknown destroy")
	}
	if p.ConsulOptions.Auth != "none" {
		restricted := os.Getenv("NYADB_CONSUL_WRITE_TOKEN")
		if restricted == "" {
			t.Fatal("original session/allowed-key ACL fixture token missing")
		}
		aclProfile := p
		aclProfile.ConsulOptions.Prefix = "allowed/"
		s, d, _ = consulWorkspaceState(t, aclProfile, restricted)
		consulSelectHeldKeysSession(t, s, d, survivor)
		s.ObserveSelectedHTTPSessionKeys(d)
		consume(t, s, d)
		if d.last.HTTPSessionKeys == nil || !d.last.HTTPSessionKeys.ProjectionComplete || d.last.HTTPSessionKeys.Complete || len(d.last.HTTPSessionKeys.Matches) != 1 || d.last.Result.HTTP.Metadata.ACLFiltered == nil || !*d.last.Result.HTTP.Metadata.ACLFiltered {
			t.Fatal("ACL-filtered visible held keys became complete impact evidence", d.Error())
		}
	}
	nativeSessionHeldKeysCapture(t, s, d, "native-held-acl-or-original-boundary")
}

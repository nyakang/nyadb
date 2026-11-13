package kvworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/nyakang/nyadb/internal/services/consul"
)

func nativeSessionDestroyCapture(t *testing.T, s *State, d *Document, stage string) {
	t.Helper()
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			tt := workspaceTester(s, d, size[0], size[1])
			tt.Frame()
			labels := []string{"Session 销毁原始记录", "选择原始步骤回执", "KV 原生回执"}
			if d.confirmation != nil {
				labels = []string{"确认销毁原 Session", "复制完整 Session 销毁预览", "我确认销毁此原 Session", "确认销毁 Session"}
			}
			assertHTTPSessionRenewalControls(t, tt, size, labels...)
			captureSessionDestroy(t, tt, language, size, stage)
		}
	}
}

func TestConsulNativePublicSessionDestroyOriginalReleaseDeleteConfirmationAndCompleteRecords(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	writer := consulRenewalWriter(t, p, secret)
	for _, behavior := range []string{"release", "delete"} {
		t.Run(behavior, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{"Name": t.Name(), "TTL": "10m", "LockDelay": "0s", "NodeChecks": []string{}, "Behavior": behavior})
			created := consulRenewalDo(t, writer, consul.Request{Method: "PUT", Path: "/v1/session/create", Body: body})
			id := created.HTTP.SessionID
			t.Cleanup(func() { consulRenewalDo(t, writer, consul.Request{Method: "PUT", Path: "/v1/session/destroy/" + id}) })
			for i := range 53 {
				consulHeldKeysAcquire(t, writer, id, fmt.Sprintf("outside-destroy/%s/key-%03d", t.Name(), i))
			}
			s, d, m := consulWorkspaceState(t, p, secret)
			if !d.HTTPSessionDestroyAvailable() {
				t.Fatal("same original lease lost public destroy facet")
			}
			consulSelectHeldKeysSession(t, s, d, id)
			original := d.Request()
			tt := workspaceTester(s, d, 720, 520)
			workspaceClick(t, tt, "预览销毁所选 Session")
			consume(t, s, d)
			if d.confirmation == nil || d.confirmation.destroy.behavior != behavior || len(d.confirmation.destroy.preview.Keys.Matches) != 53 || d.Request() != original {
				t.Fatal("public original immutable preview missing", d.Error())
			}
			preview := d.confirmation.destroy.preview.Clone()
			nativeSessionDestroyCapture(t, s, d, "native-"+behavior+"-confirm")
			tt = workspaceTester(s, d, 720, 520)
			tt.Frame()
			workspaceClick(t, tt, "下一页")
			nativeSessionDestroyCapture(t, s, d, "native-"+behavior+"-last-page")
			s.Confirm(d, true)
			consume(t, s, d)
			r := d.last.Result
			if r.HTTP == nil || !r.Completed || r.WriteOutcomeUnknown || !bytes.Equal(bytes.TrimSpace(r.HTTP.Body), []byte("true")) || r.HTTPSessionDestroy == nil || !r.HTTPSessionDestroy.Revalidated || r.HTTPSessionDestroy.Preview.Scope.Generation != m.Generation(p.ID) || len(r.HTTPSessionDestroy.Recheck.Keys.Matches) != 53 {
				t.Fatal("public destroy lost native outcome/original scope", d.Error())
			}
			before, err := s.OperationRecord(d)
			if err != nil {
				t.Fatal(err)
			}
			for _, keyIndex := range preview.Keys.Matches {
				key := preview.Keys.Root.HTTP.Entries[keyIndex].Key
				read, _ := consul.KeyReadRequest(key)
				observed := consulRenewalDo(t, writer, read)
				if behavior == "delete" {
					if len(observed.HTTP.Entries) != 0 {
						t.Fatal("native delete kept held key")
					}
				} else if len(observed.HTTP.Entries) != 1 || !bytes.Equal(observed.HTTP.Entries[0].Value, []byte{0, 255, 128}) || observed.HTTP.Entries[0].Session == id {
					t.Fatal("native release changed original bytes or retained lock")
				}
			}
			nativeSessionDestroyCapture(t, s, d, "native-"+behavior+"-receipt")
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
			if err != nil || d.needsRecovery || d.scope.Generation <= old || d.last.Scope.Generation != old || !bytes.Equal(before, after) || d.Request() != original {
				t.Fatal("public recovery replayed or relabeled original destruction", err)
			}
		})
	}
}

func TestConsulNativePublicSessionDestroyChangedKeysUnknownCancellationAndRecovery(t *testing.T) {
	p, secret := consulWorkspaceProfile(t)
	writer := consulRenewalWriter(t, p, secret)
	for _, mode := range []string{"changed", "lost", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			id := consulSessionToolCreate(t, writer, t.Name())
			key := "outside-destroy/" + t.Name() + "/key"
			consulHeldKeysAcquire(t, writer, id, key)
			profile := p
			var calls func() int32
			var processed <-chan struct{}
			if mode != "changed" {
				proxy, count, ch := consulSessionToolFault(t, p, mode)
				profile = proxy
				calls = count.Load
				processed = ch
			}
			s, d, _ := consulWorkspaceState(t, profile, secret)
			consulSelectHeldKeysSession(t, s, d, id)
			s.PrepareSelectedHTTPSessionDestroy(d)
			consume(t, s, d)
			if d.confirmation == nil {
				t.Fatal(d.Error())
			}
			if mode == "changed" {
				read, _ := consul.KeyPutRequest(key, []byte("changed"), nil, nil)
				consulRenewalDo(t, writer, read)
				s.Confirm(d, true)
				consume(t, s, d)
				if d.last.Result.Attempted || d.last.Result.HTTP != nil || d.last.Result.HTTPSessionDestroy == nil || d.last.Result.HTTPSessionDestroy.Revalidated || !d.last.Result.HTTPSessionDestroy.Recheck.Complete {
					t.Fatal("changed original key version authorized PUT", d.Error())
				}
				nativeSessionDestroyCapture(t, s, d, "native-changed-refused")
				return
			}
			s.Confirm(d, true)
			await(t, processed)
			if mode == "cancel" {
				s.RequestCancel(d)
			}
			consume(t, s, d)
			if d.unknown == nil || !d.unknown.Result.WriteOutcomeUnknown || d.unknown.Result.HTTPSessionDestroy == nil || calls() != 1 {
				t.Fatal("original processed write was acknowledged or replayed", d.Error())
			}
			before, err := encodeOperationReceipt(*d.unknown)
			if err != nil {
				t.Fatal(err)
			}
			nativeSessionDestroyCapture(t, s, d, "native-"+mode+"-unknown")
			s.Recover(d)
			consume(t, s, d)
			if d.needsRecovery || calls() != 1 {
				t.Fatal("unknown destroy recovery executed a request", d.Error())
			}
			consulWorkspaceInput(t, s, d, consul.Request{Method: "GET", Path: "/v1/kv/", Query: map[string][]string{"recurse": {""}}})
			s.Run(d)
			consume(t, s, d)
			after, err := encodeOperationReceipt(*d.unknown)
			if err != nil || !bytes.Equal(before, after) || !d.hasUnknownWrites() || calls() != 1 {
				t.Fatal("later root read changed unknown destroy record", err)
			}
		})
	}
}

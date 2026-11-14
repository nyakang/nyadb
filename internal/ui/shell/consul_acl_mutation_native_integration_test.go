package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/kvworkspace"
	"github.com/nyakang/nyadb/locales"
)

func TestConsulNativePublicShellACLMutationOriginalPlansParkingFilesAndRecovery(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(nil)
	p, secret := consulShellGridProfile(t)
	p, gate := consulShellAgentMutationRelay(t, p, "/v1/acl/token", "PUT")
	s, tt, p, _ := consulShellGridConnect(t, p, secret)
	if !s.OpenKVRequests(p.ID) {
		t.Fatal("original ACL write document unavailable")
	}
	first := s.currentTab()
	d := first.kv
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	if !s.OpenKVRequests(p.ID) {
		t.Fatal("second original document unavailable")
	}
	second := s.currentTab()
	consulShellGridWait(t, tt, func() bool { return !second.kv.Busy() })
	s.selectTab(first.id)
	originalEditor := d.Request()
	var exported bytes.Buffer
	s.kvWorkspace.SetFiles(kvworkspace.Files{SavePath: func(string) (string, error) { return "owned-memory-acl-write.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error { exported.Reset(); return write(&exported) }})
	capture := func(stage string, labels ...string) {
		t.Helper()
		for _, language := range []string{"zh-Hans", "en"} {
			i18n.SetLocaleFunc(func() string { return language })
			for _, size := range [][2]int{{1280, 800}, {720, 520}} {
				view := ui.NewTester(func(c *ui.Context) { s.View(c) }, size[0], size[1])
				view.Frame()
				for _, label := range labels {
					box, ok := view.Find(locales.Text(label))
					if !ok || box.W <= 0 || box.H <= 0 || box.X < 0 || box.Y < 0 || box.X+box.W > float32(size[0])+1 || box.Y+box.H > float32(size[1])+1 {
						t.Fatal("original ACL mutation Shell control clipped", stage, language, label)
					}
				}
				// Capture only authored noncredential input and hidden evidence.
				if dir := os.Getenv("NYADB_CONSUL_ACL_MUTATION_NATIVE_IMAGES"); dir != "" {
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal("ACL write image directory unavailable")
					}
					file, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("%s-%dx%d-shell-acl-write-%s.png", language, size[0], size[1], stage)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
					if err != nil {
						t.Fatal("ACL write image path unavailable")
					}
					err = png.Encode(file, view.Image())
					closed := file.Close()
					if err != nil || closed != nil {
						t.Fatal("ACL write image capture failed")
					}
				}
			}
		}
		i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	}
	openForm := func() {
		t.Helper()
		consulShellGridClick(t, tt, "KV 请求工具")
		if err := tt.ChooseMenuItem(locales.Text("ACL 原生写入")); err != nil {
			t.Fatal("public ACL mutation menu unavailable")
		}
		tt.Frame()
	}
	transfer := func(button string, photograph bool) []byte {
		t.Helper()
		consulShellGridClick(t, tt, button)
		if photograph {
			capture("export-consent", "我确认传出此原输入或回执中的秘密", "确认传出 ACL 写入内容", "取消")
		}
		consulShellGridClick(t, tt, "我确认传出此原输入或回执中的秘密")
		consulShellGridClick(t, tt, "确认传出 ACL 写入内容")
		consulShellGridWait(t, tt, func() bool { return !d.FileBusy() })
		return bytes.Clone(exported.Bytes())
	}
	openForm()
	s.kvWorkspace.SetHTTPACLMutationForm(d, kvworkspace.HTTPACLMutationInput{Resource: "token", Operation: "create", Definition: ` {"Description":"owned Shell ACL token"} `})
	capture("form", "ACL 资源类型", "ACL 写入操作", "检查并确认 ACL 写入")
	consulShellGridClick(t, tt, "检查并确认 ACL 写入")
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	if gate.writes.Load() != 0 || d.Request() != originalEditor {
		t.Fatal("Shell preview dispatched a mutation or changed request editor")
	}
	var original contract.KVHTTPACLMutationResult
	var originalImpact []byte
	if secret == "" {
		capture("failed-preview", "显示 ACL 影响记录（含秘密）", "导出完整 ACL 影响记录…", "返回 ACL 写入输入")
		originalImpact = transfer("导出完整 ACL 影响记录…", true)
		var record struct {
			Observation struct {
				Evidence contract.KVHTTPACLMutationImpactEvidence
			}
		}
		if json.Unmarshal(originalImpact, &record) != nil || record.Observation.Evidence.Preview.Complete || len(record.Observation.Evidence.Preview.Reads) != 1 || record.Observation.Evidence.Preview.Reads[0].Result.Receipt.HTTP == nil || record.Observation.Evidence.Preview.Reads[0].Result.Receipt.HTTP.Status != 401 {
			t.Fatal("Shell ACL-disabled preview lost original refusal")
		}
		consulShellGridClick(t, tt, "返回 ACL 写入输入")
	} else {
		capture("confirmation", "我确认执行此原连接 ACL 写入", "确认执行 ACL 写入", "取消")
		consulShellGridClick(t, tt, "查看 ACL 影响记录")
		capture("impact", "显示 ACL 影响记录（含秘密）", "返回 ACL 写入确认")
		consulShellGridClick(t, tt, "返回 ACL 写入确认")
		consulShellGridClick(t, tt, "我确认执行此原连接 ACL 写入")
		consulShellGridClick(t, tt, "确认执行 ACL 写入")
		select {
		case <-gate.processed:
		case <-time.After(20 * time.Second):
			t.Fatal("original ACL mutation was not processed")
		}
		s.selectTab(second.id)
		gate.release <- struct{}{}
		consulShellGridWait(t, tt, func() bool { return gate.delivered.Load() == 1 })
		if !d.Busy() || s.currentTab() != second {
			t.Fatal("foreign Shell tab consumed original ACL write")
		}
		s.selectTab(first.id)
		consulShellGridWait(t, tt, func() bool { return !d.Busy() })
		if d.Request() != originalEditor {
			t.Fatal("original ACL write replaced request editor")
		}
		if _, ok := d.LastReceipt(); ok {
			t.Fatal("ACL write entered generic receipt")
		}
		capture("hidden", "显示 ACL 写入原正文（含秘密）", "导出完整 ACL 写入回执…", "返回 ACL 写入输入")
		var record struct {
			Result contract.KVHTTPACLMutationResult
		}
		if json.Unmarshal(transfer("导出完整 ACL 写入回执…", true), &record) != nil {
			t.Fatal("original Shell ACL write export invalid")
		}
		original = record.Result
		if original.LocalRecordID == "" || original.Scope.Namespace.ProfileID != p.ID || original.Scope.Generation != d.Scope().Generation || original.Impact == nil || !original.Impact.Revalidated || !original.Receipt.Completed || original.Receipt.WriteOutcomeUnknown {
			t.Fatal("Shell write lost original preview/recheck/receipt")
		}
		var native struct{ AccessorID, SecretID string }
		if original.Receipt.HTTP == nil || json.Unmarshal(original.Receipt.HTTP.Body, &native) != nil || native.AccessorID == "" || native.SecretID == "" {
			t.Fatal("original generated token identity missing")
		}
		t.Cleanup(func() {
			provider, err := s.runtime.KVRequests(p.ID)
			if err != nil {
				t.Error("cleanup provider unavailable")
				return
			}
			lease, err := provider.ReserveKV(9371)
			if err != nil {
				t.Error("cleanup lease unavailable")
				return
			}
			defer lease.Close()
			facet := lease.(contract.KVHTTPACLMutating)
			plan, err := facet.PrepareHTTPACLMutation(contract.KVHTTPACLMutationRequest{Resource: "token", Operation: "delete", Identity: native.AccessorID})
			if err != nil {
				t.Error("cleanup preparation failed")
				return
			}
			r, err := facet.ExecuteHTTPACLMutation(context.Background(), plan)
			if err != nil || !r.Receipt.Completed {
				t.Error("owned token cleanup failed")
			}
		})
	}
	consulShellGridClick(t, tt, "返回请求文本")
	s.sessions.Disconnect(p.ID)
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	if !d.NeedsRecovery() {
		t.Fatal("original ACL document did not require recovery")
	}
	if err := s.runtime.Connect(context.Background(), p, secret); err != nil {
		t.Fatal("original ACL reconnect failed")
	}
	writes := gate.writes.Load()
	s.kvWorkspace.Recover(d)
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	if d.NeedsRecovery() || gate.writes.Load() != writes || d.Request() != originalEditor {
		t.Fatal("Shell recovery replayed ACL write or replaced text")
	}
	if secret == "" {
		openForm()
		consulShellGridClick(t, tt, "查看 ACL 影响记录")
		capture("recovered", "显示 ACL 影响记录（含秘密）", "返回 ACL 写入输入")
		if !bytes.Equal(transfer("导出完整 ACL 影响记录…", false), originalImpact) {
			t.Fatal("recovery changed original failed preview")
		}
		consulShellGridClick(t, tt, "返回 ACL 写入输入")
	} else {
		consulShellGridClick(t, tt, "ACL 写入回执")
		capture("recovered", "显示 ACL 写入原正文（含秘密）", "返回 ACL 写入输入")
		var record struct {
			Result contract.KVHTTPACLMutationResult
		}
		if json.Unmarshal(transfer("导出完整 ACL 写入回执…", false), &record) != nil || !reflect.DeepEqual(record.Result, original) || d.Scope().Generation <= original.Scope.Generation {
			t.Fatal("recovery relabeled original write observations or receipt")
		}
	}
	consulShellGridClick(t, tt, "返回请求文本")
	s.selectTab(second.id)
	s.closeTabID(first.id)
	s.kvWorkspace.DecideLeave(ui.Services{}, true)
	if s.currentTab() != second || d.Editor != nil {
		t.Fatal("inactive ACL write close changed tab or retained document")
	}
}

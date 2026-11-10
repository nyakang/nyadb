package kvworkspace

import (
	"bytes"
	"context"
	"encoding/base64"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

func TestKVLeaseManagementBilingualNativeFormsConfirmationAndCompleteObservation(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			s, d, l := managementTestState(t)
			l.execute = func(_ context.Context, p contract.KVPreparation) (contract.KVResult, error) {
				r := contract.KVResult{Request: p.Snapshot(), Attempted: true, Completed: true, Native: []byte{1}, Reply: &contract.KVReply{Kind: "lease_revoke"}}
				if p.Snapshot().Method == leaseGrantMethod {
					r.Reply = &contract.KVReply{Kind: "lease_grant", Lease: &contract.KVLeaseReceipt{ID: 9007199254740993, TTL: 60}}
				}
				return r, nil
			}
			d.selectedBrowser = true
			tt := workspaceTester(s, d, size[0], size[1])
			workspaceClick(t, tt, "管理租约")
			if err := tt.ChooseMenuItem(locales.Text("授予租约")); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if d.leaseManagement == nil {
				t.Fatal("menu failed to open dedicated form")
			}
			workspaceClick(t, tt, "预览授予请求")
			if d.confirmation == nil || l.executes.Load() != 0 {
				t.Fatal("grant preview dispatched")
			}
			workspaceClick(t, tt, "我确认执行此原生写入请求")
			workspaceClick(t, tt, "确认执行 KV 写入")
			consume(t, s, d)
			workspaceClick(t, tt, "读取已授予租约")
			consume(t, s, d)
			workspaceClick(t, tt, "管理租约")
			if err := tt.ChooseMenuItem(locales.Text("预览撤销租约")); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			workspaceClick(t, tt, "读取撤销关联键")
			consume(t, s, d)
			if d.confirmation == nil {
				t.Fatal("revoke observation failed", d.Error())
			}
			d.confirmation.keyRow = 1
			tt.Frame()
			workspaceClick(t, tt, "复制完整键 Base64")
			if tt.Clipboard() != base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{255}, 4096)) {
				t.Fatal("confirmation key truncated")
			}
			workspaceClick(t, tt, "复制完整撤销观察")
			if tt.Clipboard() != managementObservationJSON(d.confirmation.management) {
				t.Fatal("confirmation observation incomplete")
			}
			workspaceClick(t, tt, "我确认执行此原生写入请求")
			workspaceClick(t, tt, "确认执行 KV 写入")
			consume(t, s, d)
			if d.last.LeaseManagement == nil || d.last.LeaseManagement.Observation == nil || l.executes.Load() != 2 {
				t.Fatal("native management UI did not retain original receipt")
			}
		}
	}
}

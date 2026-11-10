package kvworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

func TestAuthManagementBilingualNativeControlsImmutableStepsAndCompleteClipboard(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			s, d, l := authFormState(t)
			s.ChooseMethod(d, AuthManagementMethod)
			permission := contract.KVAuthPermission{Access: "READWRITE", Key: bytes.Repeat([]byte{255}, 4096), End: []byte{0}, NativeExtensions: []byte{0xa0, 6, 7}}
			l.execute = func(_ context.Context, p contract.KVPreparation) (contract.KVResult, error) {
				record := p.(contract.KVPreparationRecord).RecordedRequest()
				r := contract.KVResult{Request: record.Request, RequestOmittedFields: record.OmittedFields, Attempted: true, Completed: true, Code: "OK", Native: []byte{1, 2, 3}, JSON: `{"header":{"revision":"9007199254740993"}}`, Reply: &contract.KVReply{Auth: &contract.KVAuthReceipt{}}}
				switch p.Snapshot().Method {
				case authPath("UserList"):
					r.Reply.Auth.Users = []string{" exact user "}
				case authPath("RoleList"):
					r.Reply.Auth.Roles = []string{" exact role "}
				case authPath("RoleGet"):
					r.Reply.Auth.Permissions = []contract.KVAuthPermission{permission.Clone()}
				}
				return r, nil
			}
			tt := workspaceTester(s, d, size[0], size[1])
			tt.Frame()
			if l.executes.Load() != 0 || l.connects.Load() != 0 {
				t.Fatal("opening management UI performed IO")
			}
			workspaceClick(t, tt, "角色目录")
			workspaceClick(t, tt, "刷新权限目录")
			consume(t, s, d)
			s.SelectAuthName(d, " exact role ")
			workspaceClick(t, tt, "读取名称详情")
			consume(t, s, d)
			d.authState().detailRow = 0
			tt.Frame()
			workspaceCapture(t, tt, language, size, "auth-permission-complete-range")
			workspaceClick(t, tt, "复制完整权限")
			var copied contract.KVAuthPermission
			if json.Unmarshal([]byte(tt.Clipboard()), &copied) != nil || !bytes.Equal(copied.Key, permission.Key) || !bytes.Equal(copied.NativeExtensions, permission.NativeExtensions) {
				t.Fatal("complete permission clipboard truncated native bytes/extensions")
			}
			workspaceClick(t, tt, "预览撤销所选权限")
			if d.authDraft != nil || d.Error() == "" {
				t.Fatal("unknown permission fields were silently rewritten")
			}
			workspaceClick(t, tt, "管理用户与角色")
			if err := tt.ChooseMenuItem(locales.Text("创建用户")); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			workspaceClick(t, tt, "原始名称")
			tt.Type(" exact user ")
			workspaceClick(t, tt, "临时密码")
			tt.Type(" original temporary secret ")
			d.authDraft.initialRoles = " exact role \n another role "
			tt.Frame()
			workspaceCapture(t, tt, language, size, "auth-user-create-temporary-password")
			workspaceClick(t, tt, "预览 Auth 写入步骤")
			if d.confirmation == nil || len(d.confirmation.auth.plans) != 3 {
				t.Fatal("public form did not prepare immutable independent steps", d.Error())
			}
			workspaceCapture(t, tt, language, size, "auth-user-create-step-confirmation")
			workspaceClick(t, tt, "复制完整 Auth 预览")
			if strings.Contains(tt.Clipboard(), " original temporary secret ") || !strings.Contains(tt.Clipboard(), "request_omitted_fields") || !strings.Contains(tt.Clipboard(), " another role ") {
				t.Fatal("full preview leaked secret or discarded initial step")
			}
			workspaceClick(t, tt, "我确认顺序执行这些原生写入步骤")
			workspaceClick(t, tt, "确认执行 Auth 写入")
			consume(t, s, d)
			if d.last == nil || len(d.last.AuthManagement.Steps) != 3 || d.last.Error != "" {
				t.Fatal("public confirmation lost original step receipts", d.Error())
			}
			workspaceClick(t, tt, "复制完整 JSON")
			if !strings.Contains(tt.Clipboard(), "original_step_receipts") || strings.Contains(tt.Clipboard(), " original temporary secret ") {
				t.Fatal("receipt copy incomplete or exposed password")
			}
			workspaceCapture(t, tt, language, size, "auth-create-complete-step-receipts")
		}
	}
}

func TestAuthManagementRequestFilesRemainPureAndRejectSecrets(t *testing.T) {
	for _, view := range []string{"users", "roles"} {
		request := authJSON(AuthManagementMethod, map[string]string{"view": view})
		raw, err := EncodeRequest(request)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := DecodeRequest(raw)
		if err != nil || decoded != request {
			t.Fatal("management envelope lost pure view selection", err)
		}
	}
	for _, text := range []string{`{"view":"users","password":"must-not-save"}`, `{"view":null}`, `{"view":"users","view":"roles"}`, `{"view":"other"}`, `[]`} {
		if _, err := EncodeRequest(contract.KVRequest{Method: AuthManagementMethod, JSON: text}); err == nil {
			t.Fatal("invalid/secret management request file accepted")
		}
	}
	s, d, l := authFormState(t)
	s.Close(d)
	d = s.Open(2, 7, authJSON(AuthManagementMethod, map[string]string{"view": "roles"}))
	d.lease = l
	tt := workspaceTester(s, d, 720, 520)
	tt.Frame()
	if l.executes.Load() != 0 || d.authState().category != "roles" {
		t.Fatal("opening original file executed a read or changed its view")
	}
}

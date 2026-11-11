package shell

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/consul"
	"github.com/nyakang/nyadb/locales"
)

func TestConsulNativePublicShellGridKeyPrefixDeleteConfirmationEvidenceAndDisconnect(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(nil)
	p, secret := consulShellGridProfile(t)
	p.ConsulOptions.Prefix = "native/shell-prefix/" + t.Name() + "/"
	s, tt, p, services := consulShellGridConnect(t, p, secret)
	writer := consulShellMutationSeed(t, s, p)
	do := func(r consul.Request) contract.KVResult {
		request, err := consul.DocumentRequest(r)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := writer.Prepare(request)
		if err != nil {
			t.Fatal(err)
		}
		result, err := writer.Execute(context.Background(), plan)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	object := consulShellGridOpenNamespace(t, s, tt, services, p)
	s.openQualifiedData(services, s.sessions.Current().Generation, "kv", object)
	grid := s.currentTab()
	d := grid.data
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	s.dataWorkspace.DeleteKeyPrefix(d)
	tt.Frame()
	if !tt.HasText(locales.Text("按前缀删除候选键")) || !d.HasDraft() {
		t.Fatal("Shell did not host original prefix input")
	}
	if err := tt.Click(locales.Text("输入完整删除前缀")); err != nil {
		t.Fatal(err)
	}
	tt.Command("selectAll")
	tt.Type(p.ConsulOptions.Prefix)
	captureConsulShellGrid(t, tt, "prefix-delete-form")
	consulShellGridClick(t, tt, "预览候选键删除")
	consulShellGridWait(t, tt, func() bool { return tt.HasText(locales.Text("确认候选键删除")) })
	consulShellGridClick(t, tt, "复制完整预览")
	if !strings.Contains(tt.Clipboard(), p.ConsulOptions.Prefix) || !strings.Contains(tt.Clipboard(), `"Scope"`) || !strings.Contains(tt.Clipboard(), `"BeforeImages"`) {
		t.Fatal("Shell prefix omitted complete original candidate observation")
	}
	captureConsulShellGrid(t, tt, "prefix-delete-confirmation")
	consulShellGridClick(t, tt, "取消")
	if !d.HasDraft() {
		t.Fatal("confirmation cancel discarded original prefix input")
	}
	consulShellGridClick(t, tt, "预览候选键删除")
	consulShellGridWait(t, tt, func() bool { return tt.HasText(locales.Text("确认候选键删除")) })
	later := p.ConsulOptions.Prefix + "later"
	put, err := consul.KeyPutRequest(later, []byte("preserved"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	do(put)
	consulShellGridClick(t, tt, "确认并删除候选键")
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	if d.HasDraft() {
		t.Fatal("Shell complete prefix receipt did not settle", d.Error())
	}
	consulShellGridClick(t, tt, "前缀候选原始观察")
	consulShellGridClick(t, tt, "复制完整观察")
	if !strings.Contains(tt.Clipboard(), `"Scope"`) || !strings.Contains(tt.Clipboard(), p.ConsulOptions.Prefix+"key") {
		t.Fatal("Shell original observation lost after commit")
	}
	consulShellGridClick(t, tt, "关闭")
	consulShellGridClick(t, tt, "原生事务回执")
	consulShellGridClick(t, tt, "复制完整回执")
	var receipt contract.KVResult
	if json.Unmarshal([]byte(tt.Clipboard()), &receipt) != nil || receipt.HTTP == nil || !receipt.HTTP.Acknowledged || receipt.WriteOutcomeUnknown {
		t.Fatal("Shell prefix original transaction receipt missing")
	}
	captureConsulShellGrid(t, tt, "prefix-delete-receipt")
	consulShellGridClick(t, tt, "关闭")
	read, err := consul.KeyReadRequest(later)
	if err != nil {
		t.Fatal(err)
	}
	out := do(read)
	if out.HTTP == nil || len(out.HTTP.Entries) != 1 || string(out.HTTP.Entries[0].Value) != "preserved" {
		t.Fatal("Shell prefix deleted later key")
	}
	s.disconnectMySQLProfile(p.ID)
	tt.Frame()
	if s.runtime.IsConnected(p.ID) || d.Page() != nil || s.tabIndex(grid.id) >= 0 {
		t.Fatal("Shell prefix disconnect retained scoped document")
	}
}

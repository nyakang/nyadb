package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/consul"
	"github.com/nyakang/nyadb/locales"
)

func TestConsulNativePublicShellGridKeyCopyConfirmationReceiptAndDisconnect(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(nil)
	p, secret := consulShellGridProfile(t)
	p.ConsulOptions.Prefix = "native/shell-copy/" + t.Name() + "/"
	s, tt, p, services := consulShellGridConnect(t, p, secret)
	writer := consulShellMutationSeed(t, s, p)
	object := consulShellGridOpenNamespace(t, s, tt, services, p)
	s.openQualifiedData(services, s.sessions.Current().Generation, "kv", object)
	grid := s.currentTab()
	d := grid.data
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	read := func(key string) contract.KVResult {
		r, err := consul.KeyReadRequest(key)
		if err != nil {
			t.Fatal(err)
		}
		request, err := consul.DocumentRequest(r)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := writer.Prepare(request)
		if err != nil {
			t.Fatal(err)
		}
		out, err := writer.Execute(context.Background(), plan)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	source, destination := p.ConsulOptions.Prefix+"key", p.ConsulOptions.Prefix+"target"
	before := read(source).HTTP.Entries
	s.dataWorkspace.CopyKey(d, 0)
	tt.Frame()
	if !tt.HasText(locales.Text("复制键到新键")) {
		t.Fatal("Shell did not host original key copy form")
	}
	if err := tt.Click(locales.Text("输入目标完整键")); err != nil {
		t.Fatal(err)
	}
	tt.Command("selectAll")
	tt.Type(destination)
	captureConsulShellGrid(t, tt, "key-copy-form")
	consulShellGridClick(t, tt, "预览复制")
	consulShellGridWait(t, tt, func() bool { return tt.HasText(locales.Text("确认键更改")) })
	consulShellGridClick(t, tt, "复制完整预览")
	if !strings.Contains(tt.Clipboard(), source) || !strings.Contains(tt.Clipboard(), destination) || !strings.Contains(tt.Clipboard(), `"BeforeImages"`) {
		t.Fatal("Shell copy confirmation lost original source/target")
	}
	captureConsulShellGrid(t, tt, "key-copy-confirmation")
	consulShellGridClick(t, tt, "取消")
	if !d.HasDraft() || !s.dataWorkspace.CanSave(d) {
		t.Fatal("Shell copy cancel discarded draft")
	}
	consulShellGridClick(t, tt, "保存数据更改")
	consulShellGridWait(t, tt, func() bool { return tt.HasText(locales.Text("确认键更改")) })
	consulShellGridClick(t, tt, "确认并提交更改")
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	if d.HasDraft() || !reflect.DeepEqual(before, read(source).HTTP.Entries) || !bytes.Equal(read(destination).HTTP.Entries[0].Value, []byte{255, 0}) {
		t.Fatal("Shell copy changed native source/target", d.Error())
	}
	consulShellGridClick(t, tt, "原生事务回执")
	consulShellGridClick(t, tt, "复制完整回执")
	var receipt contract.KVResult
	if json.Unmarshal([]byte(tt.Clipboard()), &receipt) != nil || receipt.HTTP == nil || !receipt.HTTP.Acknowledged || receipt.WriteOutcomeUnknown {
		t.Fatal("Shell copy receipt not retained")
	}
	captureConsulShellGrid(t, tt, "key-copy-receipt")
	consulShellGridClick(t, tt, "关闭")
	s.disconnectMySQLProfile(p.ID)
	tt.Frame()
	if s.runtime.IsConnected(p.ID) || d.Page() != nil || s.tabIndex(grid.id) >= 0 {
		t.Fatal("Shell copy disconnect retained original scoped document")
	}
}

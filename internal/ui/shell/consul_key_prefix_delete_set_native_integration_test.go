package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/consul"
	"github.com/nyakang/nyadb/locales"
)

func TestConsulNativePublicShellPrefixSetExplicitConfirmationsOriginalEvidenceAndDisconnect(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(nil)
	p, secret := consulShellGridProfile(t)
	p.ConsulOptions.Prefix = "native/shell-prefix-set/" + t.Name() + "/"
	s, tt, p, services := consulShellGridConnect(t, p, secret)
	writer := consulShellMutationSeed(t, s, p)
	do := func(r consul.Request) contract.KVResult {
		t.Helper()
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
	prefix := p.ConsulOptions.Prefix + "captured/"
	var ops []consul.TxnOperation
	for i := 0; i < 43; i++ {
		value, flags := []byte{0, 255}, ^uint64(0)
		ops = append(ops, consul.TxnOperation{KV: &consul.TxnKV{Verb: "set", Key: prefix + fmt.Sprintf("%04d", i), Value: &value, Flags: &flags}})
	}
	seed, err := consul.TxnRequest(ops)
	if err != nil {
		t.Fatal(err)
	}
	do(seed)
	object := consulShellGridOpenNamespace(t, s, tt, services, p)
	s.openQualifiedData(services, s.sessions.Current().Generation, "kv", object)
	grid := s.currentTab()
	d := grid.data
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	s.dataWorkspace.DeleteKeyPrefixSet(d)
	tt.Frame()
	if !d.HasDraft() || !tt.HasText(locales.Text("按前缀分批删除候选键")) {
		t.Fatal("Shell did not host guarded batch form")
	}
	consulShellGridClick(t, tt, "输入完整分批删除前缀")
	tt.Command("selectAll")
	tt.Type(prefix)
	captureConsulShellGrid(t, tt, "prefix-set-form")
	consulShellGridClick(t, tt, "捕获完整候选键")
	consulShellGridWait(t, tt, func() bool { return tt.HasText(locales.Text("完整候选与分批进度")) })
	consulShellGridClick(t, tt, "复制完整候选键列表")
	var keys []string
	if json.Unmarshal([]byte(tt.Clipboard()), &keys) != nil || len(keys) != 43 {
		t.Fatal("Shell capture used visible page")
	}
	captureConsulShellGrid(t, tt, "prefix-set-review")
	later := prefix + "later"
	put, err := consul.KeyPutRequest(later, []byte("later"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	do(put)
	for index := 0; index < 3; index++ {
		consulShellGridClick(t, tt, fmt.Sprintf(locales.Text("预览第 %d 批删除"), index+1))
		title := fmt.Sprintf(locales.Text("确认第 %d 批候选键删除"), index+1)
		consulShellGridWait(t, tt, func() bool { return tt.HasText(title) })
		if index == 0 {
			consulShellGridClick(t, tt, "复制完整预览")
			if !strings.Contains(tt.Clipboard(), `"BeforeImages"`) || !strings.Contains(tt.Clipboard(), prefix) {
				t.Fatal("Shell original batch preview lost")
			}
			captureConsulShellGrid(t, tt, "prefix-set-confirmation")
			consulShellGridClick(t, tt, "取消本批确认")
			consulShellGridClick(t, tt, fmt.Sprintf(locales.Text("预览第 %d 批删除"), 1))
			consulShellGridWait(t, tt, func() bool { return tt.HasText(title) })
		}
		consulShellGridClick(t, tt, "确认并删除本批候选键")
		if index < 2 {
			consulShellGridWait(t, tt, func() bool { return tt.HasText(locales.Text("完整候选与分批进度")) })
		} else {
			consulShellGridWait(t, tt, func() bool { return !d.Busy() })
		}
	}
	if d.HasDraft() || !tt.HasText(locales.Text("分批删除进度与回执")) {
		t.Fatal("Shell final receipt did not settle")
	}
	consulShellGridClick(t, tt, "复制完整内容")
	var ledger struct {
		Candidates, ConfirmedBatches int
		Batches                      []struct{ NativeJSON, Outcome string }
	}
	if json.Unmarshal([]byte(tt.Clipboard()), &ledger) != nil || ledger.Candidates != 43 || ledger.ConfirmedBatches != 3 || len(ledger.Batches) != 3 {
		t.Fatal("Shell complete original ledger lost")
	}
	for _, batch := range ledger.Batches {
		if batch.Outcome != "confirmed" || batch.NativeJSON == "" {
			t.Fatal("Shell per-batch receipt incomplete")
		}
	}
	captureConsulShellGrid(t, tt, "prefix-set-ledger")
	consulShellGridClick(t, tt, fmt.Sprintf(locales.Text("第 %d 批原事务回执"), 1))
	consulShellGridClick(t, tt, "复制完整回执")
	if tt.Clipboard() != ledger.Batches[0].NativeJSON {
		t.Fatal("Shell receipt replaced earlier bytes")
	}
	captureConsulShellGrid(t, tt, "prefix-set-receipt")
	consulShellGridClick(t, tt, "关闭")
	consulShellGridClick(t, tt, "关闭")
	read, err := consul.KeyReadRequest(later)
	if err != nil {
		t.Fatal(err)
	}
	result := do(read)
	if result.HTTP == nil || len(result.HTTP.Entries) != 1 || string(result.HTTP.Entries[0].Value) != "later" {
		t.Fatal("Shell deleted later uncaptured key")
	}
	s.disconnectMySQLProfile(p.ID)
	tt.Frame()
	if s.runtime.IsConnected(p.ID) || d.Page() != nil || s.tabIndex(grid.id) >= 0 {
		t.Fatal("Shell disconnect retained scoped batch document")
	}
}

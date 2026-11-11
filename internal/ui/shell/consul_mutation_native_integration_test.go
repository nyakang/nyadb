package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/consul"
	"github.com/nyakang/nyadb/internal/ui/dataworkspace"
	"github.com/nyakang/nyadb/locales"
)

func consulShellMutationSeed(t *testing.T, s *State, p connections.Profile) contract.KVRequestLease {
	t.Helper()
	provider, err := s.runtime.KVRequests(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	w, err := provider.ReserveKV(999)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	native, _ := consul.KeyPutRequest(p.ConsulOptions.Prefix+"key", []byte{255, 0}, nil, nil)
	r, _ := consul.DocumentRequest(native)
	plan, err := w.Prepare(r)
	if err != nil {
		t.Fatal(err)
	}
	out, err := w.Execute(context.Background(), plan)
	if err != nil || out.HTTP == nil || !out.HTTP.Succeeded {
		t.Fatal("owned native seed failed", err)
	}
	return w
}

func consulShellMutationEdit(t *testing.T, s *State, tt *ui.Tester, services ui.Services, d *dataworkspace.Document) {
	t.Helper()
	s.dataWorkspace.EditCell(services, d, 0, "value", contract.Cell{Kind: contract.Binary, Bytes: []byte{0, 255, 7}})
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	if !d.HasDraft() {
		t.Fatal("original Shell shared-grid draft missing", d.Error())
	}
	consulShellGridClick(t, tt, "保存数据更改")
	consulShellGridWait(t, tt, func() bool { return tt.HasText(locales.Text("确认键更改")) })
	consulShellGridClick(t, tt, "复制完整预览")
	if !strings.Contains(tt.Clipboard(), `"BeforeImages"`) || !strings.Contains(tt.Clipboard(), `"Operations"`) || !strings.Contains(tt.Clipboard(), consulShellMutationKey(d)) {
		t.Fatal("Shell preview lost original native scope")
	}
}

func consulShellMutationKey(d *dataworkspace.Document) string { return d.Page().Rows[0][0].Value.Text }

func TestConsulNativePublicShellGridMutationConfirmationParkingReceiptAndDisconnect(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(nil)
	p, secret := consulShellGridProfile(t)
	p.ConsulOptions.Prefix = "native/shell-mutation/" + t.Name() + "/"
	release := make(chan struct{})
	p, armed, _, acknowledged := consulShellGridRelay(t, p, consulShellMutationRelayOptions{mode: "park", release: release})
	s, tt, p, services := consulShellGridConnect(t, p, secret)
	writer := consulShellMutationSeed(t, s, p)
	object := consulShellGridOpenNamespace(t, s, tt, services, p)
	s.openQualifiedData(services, s.sessions.Current().Generation, "kv", object)
	grid := s.currentTab()
	d := grid.data
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	consulShellMutationEdit(t, s, tt, services, d)
	captureConsulShellGrid(t, tt, "mutation-confirmation")
	consulShellGridClick(t, tt, "取消")
	if !d.HasDraft() || !s.dataWorkspace.CanSave(d) {
		t.Fatal("cancel discarded original draft")
	}
	consulShellGridClick(t, tt, "保存数据更改")
	consulShellGridWait(t, tt, func() bool { return tt.HasText(locales.Text("确认键更改")) })
	parked := p
	parked.ID, parked.Name = 0, "Consul parked mutation profile"
	parked, err := s.profileService.Create(context.Background(), parked)
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	armed.Store(true)
	consulShellGridClick(t, tt, "确认并提交更改")
	select {
	case <-acknowledged:
	case <-time.After(20 * time.Second):
		t.Fatal("original parked Shell write not processed")
	}
	s.selectProfileByName(parked.Name)
	close(release)
	// Consume frames for another profile after genuine server completion.
	native, _ := consul.KeyReadRequest(p.ConsulOptions.Prefix + "key")
	r, _ := consul.DocumentRequest(native)
	deadline := time.Now().Add(20 * time.Second)
	processed := false
	for time.Now().Before(deadline) {
		plan, err := writer.Prepare(r)
		if err != nil {
			t.Fatal(err)
		}
		out, err := writer.Execute(context.Background(), plan)
		if err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if out.HTTP != nil && len(out.HTTP.Entries) == 1 && bytes.Equal(out.HTTP.Entries[0].Value, []byte{0, 255, 7}) {
			processed = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !processed || !d.HasDraft() || !d.Busy() {
		t.Fatal("foreign Shell profile consumed original completion")
	}
	s.selectProfileByName(p.Name)
	s.selectTab(grid.id)
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	if d.HasDraft() || !bytes.Equal(d.Page().Rows[0][1].Value.Bytes, []byte{0, 255, 7}) {
		t.Fatal("original Shell commit failed", d.Error())
	}
	consulShellGridClick(t, tt, "原生事务回执")
	consulShellGridClick(t, tt, "复制完整回执")
	var receipt contract.KVResult
	if json.Unmarshal([]byte(tt.Clipboard()), &receipt) != nil || receipt.HTTP == nil || !receipt.HTTP.Acknowledged || !receipt.HTTP.Succeeded || receipt.WriteOutcomeUnknown {
		t.Fatal("Shell lost original complete commit receipt")
	}
	captureConsulShellGrid(t, tt, "mutation-receipt")
	consulShellGridClick(t, tt, "关闭")
	s.disconnectMySQLProfile(p.ID)
	tt.Frame()
	if s.runtime.IsConnected(p.ID) || d.Page() != nil || s.tabIndex(grid.id) >= 0 {
		t.Fatal("confirmed original Shell disconnect retained scoped grid")
	}
}

func TestConsulNativePublicShellGridMutationProcessedReplyUnknownLeaveAndExplicitClose(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(nil)
	for _, mode := range []string{"lost", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			p, secret := consulShellGridProfile(t)
			p.ConsulOptions.Prefix = "native/shell-mutation-fault/" + strings.ReplaceAll(t.Name(), "/", "-") + "/"
			p, armed, calls, processed := consulShellGridRelay(t, p, consulShellMutationRelayOptions{mode: mode})
			s, tt, p, services := consulShellGridConnect(t, p, secret)
			writer := consulShellMutationSeed(t, s, p)
			object := consulShellGridOpenNamespace(t, s, tt, services, p)
			s.openQualifiedData(services, s.sessions.Current().Generation, "kv", object)
			grid := s.currentTab()
			d := grid.data
			consulShellGridWait(t, tt, func() bool { return !d.Busy() })
			consulShellMutationEdit(t, s, tt, services, d)
			armed.Store(true)
			consulShellGridClick(t, tt, "确认并提交更改")
			select {
			case <-processed:
			case <-time.After(20 * time.Second):
				t.Fatal("original Shell write was not processed")
			}
			if mode == "cancel" {
				consulShellGridClick(t, tt, "停止数据操作")
			}
			consulShellGridWait(t, tt, func() bool { return !d.Busy() })
			if !d.HasDraft() || s.dataWorkspace.CanSave(d) {
				t.Fatal("unknown Shell write lost draft/replay barrier")
			}
			s.closeTabID(grid.id)
			tt.Frame()
			if s.tabIndex(grid.id) < 0 || !s.RequestWorkbenchClose(func() { t.Fatal("unknown workbench closed without explicit decision") }) {
				t.Fatal("unknown original receipt bypassed leave guard")
			}
			consulShellGridClick(t, tt, "原生事务回执")
			consulShellGridClick(t, tt, "复制完整回执")
			original := tt.Clipboard()
			var receipt contract.KVResult
			if json.Unmarshal([]byte(original), &receipt) != nil || !receipt.WriteOutcomeUnknown || receipt.Request == (contract.KVRequest{}) {
				t.Fatal("Shell lost original unknown receipt")
			}
			captureConsulShellGrid(t, tt, "mutation-unknown-"+mode)
			consulShellGridClick(t, tt, "关闭")
			native, _ := consul.KeyReadRequest(p.ConsulOptions.Prefix + "key")
			r, _ := consul.DocumentRequest(native)
			plan, err := writer.Prepare(r)
			if err != nil {
				t.Fatal(err)
			}
			actual, err := writer.Execute(context.Background(), plan)
			if err != nil || actual.HTTP == nil || len(actual.HTTP.Entries) != 1 || !bytes.Equal(actual.HTTP.Entries[0].Value, []byte{0, 255, 7}) {
				t.Fatal("fault preceded genuine write", err)
			}
			consulShellGridClick(t, tt, "原生事务回执")
			consulShellGridClick(t, tt, "复制完整回执")
			if tt.Clipboard() != original || calls.Load() != 1 {
				t.Fatal("later native read replaced unknown receipt or replayed original write")
			}
			consulShellGridClick(t, tt, "关闭")
			consulShellGridClick(t, tt, "关闭原写入文档")
			captureConsulShellGrid(t, tt, "mutation-unknown-close-"+mode)
			consulShellGridClick(t, tt, "取消")
			if s.tabIndex(grid.id) < 0 {
				t.Fatal("cancel closed original unknown document")
			}
			consulShellGridClick(t, tt, "关闭原写入文档")
			consulShellGridClick(t, tt, "关闭并放弃本地草稿与证据")
			if s.tabIndex(grid.id) >= 0 || d.Page() != nil || !s.runtime.IsConnected(p.ID) || calls.Load() != 1 {
				t.Fatal("explicit original close replayed write or retired live session")
			}
		})
	}
}

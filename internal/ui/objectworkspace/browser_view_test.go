package objectworkspace

import (
	"context"
	"reflect"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func browserTester(s *State, width int) (*ui.Tester, *ui.Services) {
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) {
		services = c.Services()
		theme.Apply(c)
		s.Consume(c.Services())
		s.BrowserView(c)
		s.Modals(c)
	}, width, 600)
	return tt, &services
}

func browserFixture() (*State, *BrowserSnapshot, *Session) {
	b := &BrowserSnapshot{ProfileID: 7, Database: "app", Connected: true, Opened: true,
		Objects: []contract.DBObject{{Name: "orders", Kind: "表"}, {Name: "other", Kind: "表"}}}
	session := &Session{ProfileID: 7, Generation: 31, Connected: true}
	s := New(Dependencies{Browser: func() BrowserSnapshot { return *b }, Session: func() Session { return *session },
		SessionFor: func(int64) Session { return *session }, Runner: func() TableRunner {
			return func(context.Context, int64, mysqlsvc.MySQLTableChange) error { return nil }
		}, BatchExecutor: func(context.Context, int64, uint64, []mysqlsvc.ObjectBatchItem) []mysqlsvc.ObjectBatchResult {
			return nil
		}})
	s.SelectRow(7, 0)
	return s, b, session
}

func TestBrowserStatesKeepUnopenedScopesBlankAndNeverDispatch(t *testing.T) {
	for _, engine := range []string{"MySQL", "PostgreSQL", "SQLite"} {
		t.Run(engine, func(t *testing.T) {
			s, b, _ := browserFixture()
			s.deps.Capabilities = func(int64) contract.Capabilities { return contract.Capabilities{DesignTables: engine == "MySQL"} }
			reads, opens := 0, 0
			s.deps.LoadObjects = func(ui.Services, string, bool) { reads++ }
			s.deps.RefreshDatabases = func(ui.Services) { reads++ }
			s.deps.OpenData = func(ui.Services, int64, string, string) { opens++ }
			tt, _ := browserTester(s, 900)
			for _, state := range []struct {
				name              string
				connected, opened bool
				database          string
			}{
				{"no-profile", false, false, ""}, {"disconnected", false, true, "app"},
				{"connecting", false, false, "app"}, {"failed", false, false, "app"},
				{"connected-no-database", true, false, ""}, {"unopened", true, false, "app"},
			} {
				b.Connected, b.Opened, b.Database = state.connected, state.opened, state.database
				if state.name == "no-profile" {
					b.ProfileID = 0
				} else {
					b.ProfileID = 7
				}
				tt.Frame()
				for _, label := range []string{"自动递增值", "orders", "删除所选对象…", "对象分组名称", "全部分组"} {
					if tt.HasText(label) {
						t.Fatalf("%s: blank scope exposed %q", state.name, label)
					}
				}
				if tt.HasText("双击打开数据库") != (state.name == "unopened") {
					t.Fatalf("%s: unexpected opening hint", state.name)
				}
				for _, label := range []string{"打开选中表数据", "新建表", "筛选对象：表"} {
					if err := tt.Click(label); err != nil {
						t.Fatal(err)
					}
				}
				if state.name != "connected-no-database" {
					label := "刷新数据库列表"
					if b.Database != "" {
						label = "刷新元数据：" + b.Database
					}
					if err := tt.Click(label); err != nil {
						t.Fatal(err)
					}
				}
				if reads != 0 || opens != 0 || s.tableDialog {
					t.Fatalf("%s: disabled action dispatched", state.name)
				}
			}
			if err := tt.Click("更多对象操作"); err != nil {
				t.Fatal(err)
			}
			if len(tt.Menu()) != 0 {
				t.Fatal("unopened scope exposed an enabled more menu")
			}
			b.Database = ""
			tt.Frame()
			if err := tt.Click("刷新数据库列表"); err != nil {
				t.Fatal(err)
			}
			if reads != 1 {
				t.Fatal("connected profile could not refresh database list")
			}
		})
	}
}

func TestBrowserOpenedEmptyLoadingErrorAndCachedRows(t *testing.T) {
	s, b, _ := browserFixture()
	reads, cancels := 0, 0
	s.deps.LoadObjects = func(ui.Services, string, bool) { reads++ }
	s.deps.CancelObjects = func() { cancels++ }
	tt, _ := browserTester(s, 900)
	b.Pending = "app"
	tt.Frame()
	if !tt.HasText("orders") || !tt.HasText("正在读取数据库对象…") {
		t.Fatal("refresh hid cached rows or loading feedback")
	}
	if err := tt.Click("刷新元数据：app"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("取消加载"); err != nil {
		t.Fatal(err)
	}
	if reads != 0 || cancels != 1 {
		t.Fatal("pending read could refresh or could not cancel")
	}
	b.Pending = ""
	b.Errors = map[string]string{"app": "denied"}
	tt.Frame()
	if !tt.HasText("orders") || !tt.HasText("读取对象失败：denied") {
		t.Fatal("failure hid cached rows or error")
	}
	if err := tt.Click("重试"); err != nil {
		t.Fatal(err)
	}
	if reads != 1 {
		t.Fatal("cached error retry was unreachable")
	}
	b.Errors = nil
	b.Objects = nil
	tt.Frame()
	if !tt.HasText("名称") || !tt.HasText("自动递增值") || !tt.HasText("当前筛选下没有可见的数据库对象") {
		t.Fatal("opened empty database lost headers or feedback")
	}
	if err := tt.Click("新建表"); err != nil {
		t.Fatal(err)
	}
	if !s.tableDialog || s.tableDatabase != "app" {
		t.Fatal("empty database lost its create action")
	}
}

func TestBrowserMoreMenuKeepsOpeningSelectionAndRejectsStaleScope(t *testing.T) {
	for _, mutation := range []string{"selection", "profile", "database", "generation", "reset", "close"} {
		t.Run(mutation, func(t *testing.T) {
			s, b, session := browserFixture()
			tt, services := browserTester(s, 900)
			if err := tt.Click("更多对象操作"); err != nil {
				t.Fatal(err)
			}
			want := []string{"复制所选对象", "粘贴对象…", "删除所选对象…", "-", "新建对象", "-", "对象分组筛选", "管理组…", "-", "用户与权限", "继续对象草稿"}
			if !reflect.DeepEqual(tt.Menu(), want) {
				t.Fatalf("menu order: %v", tt.Menu())
			}
			switch mutation {
			case "selection":
				s.SelectRow(7, 1)
			case "profile":
				b.ProfileID = 8
				session.ProfileID = 8
				s.SelectRow(8, 1)
			case "database":
				b.Database = "other"
				s.SelectRow(7, 1)
			case "generation":
				session.Generation++
			case "reset":
				s.ResetBrowser(7)
				s.SelectRow(7, 1)
			case "close":
				s.CloseAll()
				s.SelectRow(7, 1)
			}
			tt.Frame()
			if err := tt.ChooseMenuItem("复制所选对象"); err != nil {
				t.Fatal(err)
			}
			raw := (*services).ReadClipboard()
			if mutation != "selection" {
				if raw != "" {
					t.Fatalf("stale %s menu copied new target", mutation)
				}
				return
			}
			clip, err := DecodeObjectClipboard(raw)
			if err != nil || clip.ProfileID != 7 || len(clip.Objects) != 1 || clip.Objects[0].Name != "orders" {
				t.Fatalf("opening selection lost: %+v %v", clip, err)
			}
		})
	}
}

func TestBrowserMoreMenuMultiSelectionPasteCapabilitiesAndDraft(t *testing.T) {
	s, _, session := browserFixture()
	b := s.currentBrowser()
	b.selection.Add(objectKey{7, "app", "表", "orders"})
	b.selection.Add(objectKey{7, "app", "表", "other"})
	tt, services := browserTester(s, 900)
	if err := tt.Click("更多对象操作"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("删除所选对象…"); err != nil {
		t.Fatal(err)
	}
	if s.batch == nil || len(s.batch.items) != 2 || s.batch.items[0].Ref.Name != "orders" || s.batch.items[1].Ref.Name != "other" {
		t.Fatal("batch menu lost multi-selection")
	}
	s.batch = nil
	tt.Frame()
	raw, _ := EncodeObjectClipboardFor(8, []mysqlsvc.ObjectIdentity{{Database: "app", Name: "orders", Kind: "表"}})
	(*services).WriteClipboard(raw)
	status := ""
	s.deps.Status = func(v string) { status = v }
	if err := tt.Click("更多对象操作"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("粘贴对象…"); err != nil {
		t.Fatal(err)
	}
	if s.batch != nil || status == "" {
		t.Fatal("foreign connection clipboard was accepted")
	}
	raw, _ = EncodeObjectClipboardFor(7, []mysqlsvc.ObjectIdentity{{Database: "app", Name: "orders", Kind: "表"}})
	(*services).WriteClipboard(raw)
	if err := tt.Click("更多对象操作"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("粘贴对象…"); err != nil {
		t.Fatal(err)
	}
	if s.batch == nil || s.batch.items[0].Action != "copy" || s.batch.items[0].Ref.Database != "app" {
		t.Fatal("same-profile paste was not dispatched")
	}
	s.batch = nil
	s.deps.Capabilities = func(int64) contract.Capabilities { return contract.Capabilities{} }
	s.parkedObjectOp = &objectOperation{profile: 8, text: "original draft"}
	tt.Frame()
	if err := tt.Click("更多对象操作"); err != nil {
		t.Fatal(err)
	}
	for _, path := range [][]string{{"删除所选对象…"}, {"粘贴对象…"}, {"用户与权限"}, {"新建对象", "新建视图"}, {"继续对象草稿"}} {
		if err := tt.ChooseMenuItem(path...); err == nil {
			t.Fatalf("unsupported action enabled: %v", path)
		}
	}
	tt.CloseMenu()
	s.parkedObjectOp.profile = session.ProfileID
	tt.Frame()
	if err := tt.Click("更多对象操作"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("继续对象草稿"); err != nil {
		t.Fatal(err)
	}
	if s.objectOp == nil || s.objectOp.profile != 7 || s.objectOp.text != "original draft" || s.parkedObjectOp != nil {
		t.Fatal("draft recovery lost original identity or text")
	}
}

func TestObjectGroupModalCommitsCapturedSelectionAndCancelsInput(t *testing.T) {
	s, _, _ := browserFixture()
	writes := 0
	s.deps.SaveObjectGroups = func(profile int64, database string, values map[string]string) {
		writes++
		if profile != 7 || database != "app" {
			t.Fatal("group write targeted another scope")
		}
	}
	tt, _ := browserTester(s, 900)
	open := func() {
		t.Helper()
		if err := tt.Click("更多对象操作"); err != nil {
			t.Fatal(err)
		}
		if err := tt.ChooseMenuItem("管理组…"); err != nil {
			t.Fatal(err)
		}
	}
	open()
	if err := tt.Click("对象分组名称"); err != nil {
		t.Fatal(err)
	}
	tt.Type("discarded")
	if err := tt.Click("关闭"); err != nil {
		t.Fatal(err)
	}
	if writes != 0 || s.groupDialog != nil {
		t.Fatal("closing saved uncommitted group input")
	}
	open()
	if s.groupDialog.name != "" {
		t.Fatal("closed input leaked into a new modal")
	}
	s.SelectRow(7, 1)
	if err := tt.Click("对象分组名称"); err != nil {
		t.Fatal(err)
	}
	tt.Type("Favorites")
	if err := tt.Click("分组所选对象"); err != nil {
		t.Fatal(err)
	}
	if s.ObjectGroup(7, "app", "表", "orders") != "Favorites" || s.ObjectGroup(7, "app", "表", "other") != "" || writes != 1 {
		t.Fatal("group commit used live selection")
	}
	if err := tt.Click("对象分组名称"); err != nil {
		t.Fatal(err)
	}
	tt.Command("selectAll")
	tt.Type("Renamed")
	if err := tt.Click("重命名分组"); err != nil {
		t.Fatal(err)
	}
	if s.ObjectGroup(7, "app", "表", "orders") != "Renamed" {
		t.Fatal("rename did not persist")
	}
	if err := tt.Click("关闭"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("更多对象操作"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("对象分组筛选", "Renamed"); err != nil {
		t.Fatal(err)
	}
	if len(s.SelectedObjects()) != 1 || s.SelectedObjects()[0].Name != "orders" {
		t.Fatal("group filter did not match persisted identity")
	}
	open()
	if err := tt.Click("删除分组"); err != nil {
		t.Fatal(err)
	}
	if s.ObjectGroup(7, "app", "表", "orders") != "" || s.currentBrowser().groupFilter != "" {
		t.Fatal("group deletion did not release the active filter")
	}
}

func TestObjectGroupModalRevokesOnScopeChangeAndClose(t *testing.T) {
	for _, mutation := range []string{"profile", "database", "disconnect", "unopened", "generation", "reset", "close"} {
		t.Run(mutation, func(t *testing.T) {
			s, b, session := browserFixture()
			writes := 0
			s.deps.SaveObjectGroups = func(int64, string, map[string]string) { writes++ }
			tt, _ := browserTester(s, 900)
			if err := tt.Click("更多对象操作"); err != nil {
				t.Fatal(err)
			}
			if err := tt.ChooseMenuItem("管理组…"); err != nil {
				t.Fatal(err)
			}
			s.groupDialog.name = "uncommitted"
			switch mutation {
			case "profile":
				b.ProfileID = 8
			case "database":
				b.Database = "other"
			case "disconnect":
				b.Connected = false
			case "unopened":
				b.Opened = false
			case "generation":
				session.Generation++
			case "reset":
				s.ResetBrowser(7)
			case "close":
				s.CloseAll()
			}
			tt.Frame()
			if s.groupDialog != nil || writes != 0 || tt.HasText("对象分组名称") {
				t.Fatalf("%s did not revoke modal", mutation)
			}
		})
	}
}

func TestBrowserToolbarLayoutAndLocalization(t *testing.T) {
	previousMode := theme.CurrentMode()
	defer theme.SetMode(previousMode)
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, dark := range []bool{false, true} {
			if dark {
				theme.SetMode(theme.ModeDark)
			} else {
				theme.SetMode(theme.ModeLight)
			}
			for _, scale := range []float32{1, 1.25, 1.5} {
				for _, width := range []int{900, 320} {
					s, b, _ := browserFixture()
					tt := ui.NewTester(func(c *ui.Context) {
						base := ui.LightTheme()
						if dark {
							base = ui.DarkTheme()
						}
						c.SetTheme(base)
						theme.Apply(c)
						s.BrowserView(c)
					}, width, 600)
					tt.SetScale(scale)
					more, ok := tt.Find(locales.Text("更多对象操作"))
					refresh, refreshOK := tt.Find(locales.Text("刷新元数据：") + "app")
					if !ok || !refreshOK || more.Y != refresh.Y || more.X+more.W > refresh.X || refresh.X+refresh.W > float32(width) {
						t.Fatalf("%s/%v/%v/%d toolbar not single-row: %+v %+v", language, dark, scale, width, more, refresh)
					}
					if err := tt.Click(locales.Text("更多对象操作")); err != nil {
						t.Fatal(err)
					}
					if err := tt.ChooseMenuItem(locales.Text("管理组…")); err != nil {
						t.Fatal(err)
					}
					tt.CloseMenu()
					b.Connected = false
					tt.Frame()
					if tt.HasText(locales.Text("自动递增值")) {
						t.Fatal("disconnected translated view retained headers")
					}
				}
			}
		}
	}
}

func TestBrowserMoreMenuCreatesDefinitionsAndAllowsConnectionPermissions(t *testing.T) {
	s, b, _ := browserFixture()
	s.deps.ObjectExecutor = func(context.Context, int64, mysqlsvc.ObjectPlan) mysqlsvc.ObjectOutcome {
		return mysqlsvc.ObjectOutcome{}
	}
	tt, _ := browserTester(s, 900)
	if err := tt.Click("更多对象操作"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("新建对象", "新建视图"); err != nil {
		t.Fatal(err)
	}
	if s.objectOp == nil || s.objectOp.profile != 7 || s.objectOp.ref.Database != "app" || s.objectOp.ref.Kind != "视图" {
		t.Fatal("new definition menu lost its scope")
	}
	s.objectOp = nil
	s.SelectRow(7, -1)
	tt.Frame()
	if err := tt.Click("更多对象操作"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("复制所选对象"); err == nil {
		t.Fatal("empty selection could copy objects")
	}
	if err := tt.ChooseMenuItem("删除所选对象…"); err == nil {
		t.Fatal("empty selection could delete objects")
	}
	tt.CloseMenu()
	b.Database = ""
	b.Opened = false
	s.deps.AccountLoader = func(context.Context, int64) ([]mysqlsvc.MySQLAccount, error) { return nil, nil }
	s.deps.AccountExecutor = func(context.Context, int64, uint64, []mysqlsvc.AccountChange) mysqlsvc.DDLOutcome {
		return mysqlsvc.DDLOutcome{}
	}
	tt.Frame()
	if err := tt.Click("更多对象操作"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("用户与权限"); err != nil {
		t.Fatal(err)
	}
	if s.accounts == nil || s.accounts.profile != 7 {
		t.Fatal("connection permissions were lost without an opened database")
	}
}

func TestObjectGroupExclusionAndEscapeLeaveUncommittedInputUnsaved(t *testing.T) {
	s, _, _ := browserFixture()
	s.AssignObjectGroup("Existing")
	writes := 0
	s.deps.SaveObjectGroups = func(int64, string, map[string]string) { writes++ }
	tt, _ := browserTester(s, 900)
	if err := tt.Click("更多对象操作"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("管理组…"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("从组中排除"); err != nil {
		t.Fatal(err)
	}
	if s.ObjectGroup(7, "app", "表", "orders") != "" || writes != 1 {
		t.Fatal("exclusion did not persist the captured target")
	}
	if err := tt.Click("对象分组名称"); err != nil {
		t.Fatal(err)
	}
	tt.Type("discarded")
	tt.Key(0, ui.KeyEscape)
	if s.groupDialog != nil || writes != 1 {
		t.Fatal("Escape saved the group input or retained the modal")
	}
}

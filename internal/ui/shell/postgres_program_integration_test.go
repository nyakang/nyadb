package shell

import (
	"context"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/favoritesworkspace"
	"github.com/nyakang/nyadb/internal/ui/navigation"
)

// This uses a real PostgreSQL session and the shared MyGo workbench. Images are
// Tester evidence; they do not represent an OS desktop or original differential.
func TestPostgreSQLProgramShellFixture(t *testing.T) {
	if os.Getenv("NYADB_POSTGRES_IT") != "1" {
		t.Skip("requires explicit disposable PostgreSQL fixture")
	}
	port, err := strconv.ParseUint(os.Getenv("NYADB_POSTGRES_PORT"), 10, 16)
	if err != nil || port == 0 {
		t.Fatal("missing fixture port")
	}
	s, _, service, _ := profileTestState(t)
	t.Cleanup(s.CloseMySQLSessions)
	p, err := service.Create(context.Background(), connections.Profile{Name: "PostgreSQL program fixture", Kind: connections.PostgreSQL, Host: "127.0.0.1", Port: uint16(port), Username: "nyadb_it", Database: "nyadb_it", TLSMode: "disable"})
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1280, 800)
	tt.Frame()
	if err = tt.Click("连接所选数据库"); err != nil {
		t.Fatal(err)
	}
	wait := func(ready func() bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !ready() && time.Now().Before(deadline) {
			tt.Frame()
			time.Sleep(2 * time.Millisecond)
		}
		tt.Frame()
		if !ready() {
			t.Fatal("workbench request did not settle", s.sessions.Current().Error)
		}
	}
	wait(func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	if s.sessions.StatusFor(p.ID) != "已连接" {
		t.Fatal("PostgreSQL profile did not connect")
	}
	raw, err := s.runtime.Session(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := contract.Require[contract.QueryProvider](raw)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := provider.ReserveQuery(99)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	ctx := context.Background()
	_, err = lease.Execute(ctx, "public", `CREATE SCHEMA nyadb_program_ui;CREATE SCHEMA nyadb_program_ui_other;
CREATE SEQUENCE nyadb_program_ui.counter AS bigint START WITH 9223372036854775805;
CREATE TABLE nyadb_program_ui.effects(value text);
CREATE FUNCTION nyadb_program_ui.over(arg integer) RETURNS bigint LANGUAGE SQL AS $$SELECT nextval('nyadb_program_ui.counter')$$;
CREATE FUNCTION nyadb_program_ui.over(arg text) RETURNS text LANGUAGE SQL AS $$SELECT arg$$;
CREATE FUNCTION nyadb_program_ui_other.over(arg integer) RETURNS integer LANGUAGE SQL AS $$SELECT arg$$;
CREATE PROCEDURE nyadb_program_ui.mutate(arg integer) LANGUAGE plpgsql AS $$BEGIN INSERT INTO nyadb_program_ui.effects VALUES ('executed');END$$;`)
	if err != nil {
		t.Fatal(err)
	}
	cleaned := false
	defer func() {
		if !cleaned {
			_, err := lease.Execute(ctx, "public", `DROP SCHEMA IF EXISTS nyadb_program_ui CASCADE;DROP SCHEMA IF EXISTS nyadb_program_ui_other CASCADE`)
			if err != nil {
				t.Error(err)
			}
		}
	}()
	s.sessions.RefreshDatabases(services.Invalidate)
	wait(func() bool { return !s.sessions.Current().Refreshing() })
	s.openMySQLDatabase(services, "nyadb_program_ui", true)
	wait(func() bool {
		return s.sessions.Current().Pending == "" && s.sessions.Current().Cache["nyadb_program_ui"] != nil
	})
	if !s.objectCapabilitiesFor(p.ID).DesignTables || !s.toolbarActionAvailable("函数") || !s.toolbarActionAvailable("其它") {
		t.Fatal("read-only browse availability incorrectly depends on design capability")
	}
	if err = tt.Click("函数"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("over(integer)") || !tt.HasText("over(text)") {
		t.Fatal("function directory collapsed overloads")
	}
	if err = tt.Click("其它"); err != nil {
		t.Fatal(err)
	}
	originalGeneration := s.sessions.Current().Generation
	s.sessions.Current().Generation++
	tt.Frame()
	if err = tt.ChooseMenuItem("序列"); err != nil {
		t.Fatal(err)
	}
	if s.objectWorkspace.Category(p.ID) != "函数" {
		t.Fatal("stale PostgreSQL toolbar menu changed category")
	}
	s.sessions.Current().Generation = originalGeneration
	if err = tt.Click("over(integer)"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	first := s.currentTab()
	ref := s.runtime.Namespace(p.ID, "nyadb_program_ui").Object("function", "over(integer)")
	if first.kind != tabProgram || first.object.Object() != ref || first.object.ProgramData().Definition == "" {
		t.Fatal("qualified function definition missing")
	}
	s.openMySQLProgram("nyadb_program_ui", "over(integer)", "函数")
	if s.currentTab() != first || len(s.tabs) != 2 {
		t.Fatal("qualified same-object reopening created a duplicate tab")
	}
	s.openMySQLProgram("nyadb_program_ui", "over(text)", "函数")
	second := s.currentTab()
	if second == first || len(s.tabs) != 3 {
		t.Fatal("overloads shared a document")
	}
	s.showObjects("全部")
	tt.Frame()
	if err = tt.Click("其它"); err != nil {
		t.Fatal(err)
	}
	if err = tt.ChooseMenuItem("序列"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if s.objectWorkspace.FilterKind(p.ID) != "序列" || !tt.HasText("counter") || tt.HasText("effects") {
		t.Fatal("sequence category did not filter objects")
	}
	s.objectWorkspace.SelectRow(p.ID, 0)
	tt.Frame()
	if err = tt.Click("查看程序定义"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	sequence := s.currentTab()
	if sequence.kind != tabProgram || sequence.object.Object().Kind != "sequence" || !tt.HasText("序列起点") || !tt.HasText("9223372036854775805") {
		t.Fatal("sequence view lost exact parameters")
	}
	_, hasParameters := tt.Find("参数")
	_, hasReturnType := tt.Find("返回类型")
	if hasParameters || hasReturnType {
		t.Fatal("program inspector retained the previous function's metadata")
	}
	for _, shortcut := range []struct {
		mod ui.Modifiers
		key ui.Key
	}{{ui.Ctrl, ui.KeyEnter}, {ui.Ctrl, ui.KeyD}, {0, ui.KeyDelete}} {
		tt.Key(shortcut.mod, shortcut.key)
	}
	tt.Frame()
	if s.objectWorkspace.TableDialogOpen() || s.currentTab() != sequence {
		t.Fatal("sequence shortcuts entered table operations")
	}
	if dir := os.Getenv("NYADB_PROGRAM_EVIDENCE_DIR"); dir != "" {
		if err = os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for _, size := range []struct{ width, height int }{{1280, 800}, {840, 560}} {
			tt.SetSize(size.width, size.height)
			tt.Frame()
			path := filepath.Join(dir, "postgres-sequence-tester-"+strconv.Itoa(size.width)+".png")
			f, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			err = png.Encode(f, tt.Image())
			closeErr := f.Close()
			if err != nil || closeErr != nil {
				t.Fatal(err, closeErr)
			}
		}
		tt.SetSize(1280, 800)
		tt.Frame()
	}
	s.showObjects("全部")
	s.openMySQLDatabase(services, "nyadb_program_ui_other", true)
	wait(func() bool {
		return s.sessions.Current().Pending == "" && s.sessions.Current().Cache["nyadb_program_ui_other"] != nil
	})
	s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: "nyadb_program_ui_other", Category: "函数", Object: "over(integer)"})
	other := s.currentTab()
	if other == first || other.object.Object().Schema != "nyadb_program_ui_other" || first.object.Object().Schema != "nyadb_program_ui" {
		t.Fatal("cross-schema programs reused another document")
	}
	// A real search resolves the overload signature and reuses the original tab.
	s.openObjectSearch()
	tt.Frame()
	if err = tt.Click("查找对象名称、注释或定义"); err != nil {
		t.Fatal(err)
	}
	tt.Type("over(text)")
	if err = tt.Click("开始查找"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return tt.HasText("找到 1 项") })
	results, ok := tt.Find("对象查找结果")
	if !ok {
		t.Fatal("search result table missing")
	}
	// The same text also appears in an existing tab caption behind the modal.
	// Select the result row inside the modal, not that background caption.
	tt.ClickAt(results.X+40, results.Y+38)
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if s.currentTab() != second {
		t.Fatal("search did not resolve the original overload document")
	}
	wait(func() bool { return s.sessions.Current().Pending == "" })
	s.openMySQLProgram("nyadb_program_ui", "counter", "序列")
	entry, ok := s.currentFavorite()
	if !ok || entry.Object != sequence.object.Object() {
		t.Fatal("sequence favorite lost qualified identity")
	}
	// Connect a second profile to the same catalog. Its identically named
	// objects have separate documents; a favorite resolves its original profile.
	p2 := p
	p2.ID, p2.Name = 0, "PostgreSQL second program profile"
	p2, err = service.Create(ctx, p2)
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.selectProfileByName(p2.Name)
	s.startMySQLSession(services)
	wait(func() bool { return s.sessions.StatusFor(p2.ID) != "连接中" })
	s.openMySQLDatabase(services, "nyadb_program_ui", true)
	wait(func() bool {
		return s.sessions.Current().Pending == "" && s.sessions.Current().Cache["nyadb_program_ui"] != nil
	})
	s.openMySQLProgram("nyadb_program_ui", "counter", "序列")
	secondSequence := s.currentTab()
	if secondSequence == sequence || secondSequence.object.Object().ProfileID != p2.ID {
		t.Fatal("profiles shared a sequence document")
	}
	_, err = lease.Execute(ctx, "public", `COMMENT ON SEQUENCE nyadb_program_ui.counter IS 'fresh favorite metadata'`)
	if err != nil {
		t.Fatal(err)
	}
	s.openFavorite(services, favoritesworkspace.Item{Entry: entry, Available: true})
	wait(func() bool {
		return s.currentTab() == sequence && sequence.object.ProgramData().Comment == "fresh favorite metadata"
	})
	if s.selectedProfileID != p.ID || secondSequence.object.ProgramData().Comment != "" {
		t.Fatal("favorite changed a different profile's snapshot")
	}
	wait(func() bool { return s.sessions.Current().Pending == "" })
	// A failed metadata refresh retains both directory cache and open documents.
	_, err = lease.Execute(ctx, "public", "CREATE FUNCTION nyadb_program_ui.large() RETURNS text LANGUAGE SQL AS $body$SELECT '"+strings.Repeat("x", 1<<20)+"'::text$body$")
	if err != nil {
		t.Fatal(err)
	}
	beforeCount := len(s.sessions.Current().Cache["nyadb_program_ui"])
	s.sessions.LoadObjects("nyadb_program_ui", true, services.Invalidate)
	wait(func() bool { return s.sessions.Current().Pending == "" })
	if s.sessions.Current().Errors["nyadb_program_ui"] == "" || len(s.sessions.Current().Cache["nyadb_program_ui"]) != beforeCount || sequence.object.ProgramData().Comment != "fresh favorite metadata" {
		t.Fatal("failed refresh replaced usable metadata")
	}
	_, err = lease.Execute(ctx, "public", "DROP FUNCTION nyadb_program_ui.large()")
	if err != nil {
		t.Fatal(err)
	}
	result, err := lease.Execute(ctx, "public", `SELECT last_value,is_called,(SELECT count(*) FROM nyadb_program_ui.effects) FROM nyadb_program_ui.counter`)
	if err != nil || len(result.Sets) != 1 || len(result.Sets[0].Rows) != 1 {
		t.Fatal("non-execution verification failed", err)
	}
	row := result.Sets[0].Rows[0]
	if row[0].Text != "9223372036854775805" || row[1].Text != "false" || row[2].Text != "0" {
		t.Fatal("browsing executed a routine or advanced its sequence", row)
	}
	// Cleanup must precede disconnect, whose contract revokes original leases.
	_, err = lease.Execute(ctx, "public", `DROP SCHEMA nyadb_program_ui CASCADE;DROP SCHEMA nyadb_program_ui_other CASCADE`)
	if err != nil {
		t.Fatal(err)
	}
	cleaned = true
	// The deferred cleanup remains idempotent after the original lease is closed.
	lease.Release()
	s.disconnectMySQLProfile(p.ID)
	tt.Frame()
	for _, doc := range []*tab{first, second, sequence, other} {
		if doc.object.ProgramData().Definition != "" || doc.object.ProgramData().Sequence != nil {
			t.Fatal("disconnect retained program metadata")
		}
	}
	if secondSequence.object.ProgramData().Sequence == nil {
		t.Fatal("disconnect cleared a different profile's document")
	}
	s.disconnectMySQLProfile(p2.ID)
	s.CloseMySQLSessions()
	s.CloseMySQLSessions()
	if secondSequence.object.ProgramData().Sequence != nil {
		t.Fatal("repeated close retained sequence metadata")
	}
}

package objectworkspace

import (
	"context"
	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"testing"
)

func TestObjectCompletionCarriesUIAndRuntimeGenerationsSeparately(t *testing.T) {
	session := Session{ProfileID: 7, Generation: 31, Connected: true}
	var event ObjectEvent
	s := New(Dependencies{Session: func() Session { return session }, SessionFor: func(int64) Session { return session }, RuntimeGeneration: func(int64) uint64 { return 8 }, ObjectChanged: func(_ ui.Services, e ObjectEvent) { event = e }})
	ch := make(chan mysqlsvc.ObjectOutcome, 1)
	ch <- mysqlsvc.ObjectOutcome{DDLOutcome: mysqlsvc.DDLOutcome{State: "executed", Verified: true}}
	s.objectOp = &objectOperation{profile: 7, generation: 8, sessionGeneration: 31, ref: mysqlsvc.ObjectIdentity{Database: "db", Name: "v", Kind: "view"}, action: "create", result: ch, busy: true}
	tt := ui.NewTester(func(c *ui.Context) { s.Consume(c.Services()) }, 300, 200)
	tt.Frame()
	if event.Generation != 31 || event.ProfileID != 7 || s.objectOp.busy {
		t.Fatalf("completion lost original UI generation: %+v", event)
	}
}
func TestObjectCreateNameChangesPreserveEditedDefinition(t *testing.T) {
	session := Session{ProfileID: 7, Generation: 31, Connected: true}
	s := New(Dependencies{Session: func() Session { return session }, SessionFor: func(int64) Session { return session }, ObjectExecutor: func(context.Context, int64, mysqlsvc.ObjectPlan) mysqlsvc.ObjectOutcome {
		return mysqlsvc.ObjectOutcome{}
	}})
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.Modals(c) }, 900, 700)
	s.BeginObject(services, 7, mysqlsvc.ObjectIdentity{Database: "db", Name: "original", Kind: "procedure"}, "create")
	s.objectOp.text = "CREATE PROCEDURE `db`.`original`() BEGIN SELECT 'original'; SELECT 2; END"
	tt.Frame()
	if e := tt.Click("新对象名称"); e != nil {
		t.Fatal(e)
	}
	tt.Key(ui.Ctrl, ui.KeyA)
	tt.Type("renamed")
	if s.objectOp.ref.Name != "renamed" || s.objectOp.original.Name != "renamed" {
		t.Fatalf("form identity did not change: %+v", s.objectOp.ref)
	}
	if s.objectOp.text != "CREATE PROCEDURE `db`.`renamed`() BEGIN SELECT 'original'; SELECT 2; END" {
		t.Fatalf("name change discarded body: %s", s.objectOp.text)
	}
}
func TestObjectGroupPersistenceUsesQualifiedIdentity(t *testing.T) {
	snapshot := BrowserSnapshot{ProfileID: 7, Database: "one", Objects: []mysqlsvc.MySQLDBObject{{Name: "same", Kind: "表"}}}
	session := Session{ProfileID: 7, Generation: 1, Connected: true}
	records := map[string]string{}
	s := New(Dependencies{Browser: func() BrowserSnapshot { return snapshot }, Session: func() Session { return session }})
	read := func(k string) (string, error) { return records[k], nil }
	write := func(k, v string) error { records[k] = v; return nil }
	s.SetObjectGroupPreferences(read, write)
	s.SelectRow(7, 0)
	s.AssignObjectGroup("Favorites")
	if len(records) != 1 {
		t.Fatal("group was not persisted")
	}
	s.ResetBrowser(7)
	s.currentBrowser()
	if s.ObjectGroup(7, "one", "表", "same") != "Favorites" {
		t.Fatal("group did not restore")
	}
	snapshot.Database = "two"
	s.currentBrowser()
	if s.ObjectGroup(7, "two", "表", "same") != "" {
		t.Fatal("group identity leaked across databases")
	}
}
func TestVersionedObjectClipboardRejectsUnknownAndKeepsProfile(t *testing.T) {
	raw, e := EncodeObjectClipboardFor(9, []mysqlsvc.ObjectIdentity{{Database: "db", Name: "t", Kind: "表"}})
	if e != nil {
		t.Fatal(e)
	}
	clip, e := DecodeObjectClipboard(raw)
	if e != nil || clip.ProfileID != 9 || clip.Objects[0].Database != "db" {
		t.Fatal("clipboard lost source identity")
	}
	for _, bad := range []string{"Navicat private format", `{"format":"nyadb-objects","version":2,"objects":[{"Database":"db","Name":"t","Kind":"表"}]}`} {
		if _, e = DecodeObjectClipboard(bad); e == nil {
			t.Fatal("foreign or unknown clipboard accepted")
		}
	}
}

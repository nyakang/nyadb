package objectworkspace

import (
	"testing"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func TestProgramDocumentKeepsQualifiedIdentityAndOwnsSnapshots(t *testing.T) {
	rows := int64(9)
	source := mysqlsvc.MySQLDBObject{Name: "cleanup", Kind: "存储过程", Definition: "DELETE FROM logs", Definer: "admin@localhost", RowsEstimate: &rows}
	d := newProgram(41, "app", source)
	source.Definition, rows = "changed cache", 99
	if d == nil || d.ProfileID() != 41 || d.Database() != "app" || d.Name() != "cleanup" || d.Kind() != "存储过程" || d.Data().Definition != "DELETE FROM logs" || *d.Data().RowsEstimate != 9 {
		t.Fatal("document lost its qualified identity or shared a mutable metadata snapshot")
	}
	snapshot := d.Data()
	snapshot.Definer = "other@localhost"
	*snapshot.RowsEstimate = 100
	if d.Data().Definer != "admin@localhost" || *d.Data().RowsEstimate != 9 {
		t.Fatal("host presentation mutated the document snapshot")
	}
	for _, data := range []mysqlsvc.MySQLDBObject{{Name: "other", Kind: "存储过程"}, {Name: "cleanup", Kind: "函数"}} {
		if d.Update(data) || d.Data().Definition != "DELETE FROM logs" {
			t.Fatal("another object replaced the original definition")
		}
	}
	source.Definition = "DELETE FROM archived_logs"
	if !d.Update(source) || d.Data().Definition != source.Definition {
		t.Fatal("same-object metadata refresh did not update the document")
	}
	d.Close()
	d.Close()
	if d.Update(source) || d.Data().Definition != "" || d.Data().Name != "" || d.ProfileID() != 41 || d.Database() != "app" || d.Name() != "cleanup" {
		t.Fatal("closed document retained metadata, changed identity or reopened")
	}
}

func TestProgramDocumentsAdmitOnlyQualifiedSupportedObjects(t *testing.T) {
	for _, kind := range []string{"函数", "存储过程", "事件"} {
		d := newProgram(41, "app", mysqlsvc.MySQLDBObject{Name: "same_name", Kind: kind})
		if d == nil || !Supports(kind) {
			t.Fatalf("supported object rejected: %s", kind)
		}
	}
	for _, kind := range []string{"", "表", "视图", "SQL", "PROCEDURE"} {
		if Supports(kind) || newProgram(41, "app", mysqlsvc.MySQLDBObject{Name: "same_name", Kind: kind}) != nil {
			t.Fatalf("non-program object admitted: %s", kind)
		}
	}
	for _, input := range []struct {
		profile        int64
		database, name string
	}{{0, "app", "cleanup"}, {-1, "app", "cleanup"}, {41, "", "cleanup"}, {41, "app", ""}} {
		if newProgram(input.profile, input.database, mysqlsvc.MySQLDBObject{Name: input.name, Kind: "存储过程"}) != nil {
			t.Fatal("incomplete identity admitted")
		}
	}
}

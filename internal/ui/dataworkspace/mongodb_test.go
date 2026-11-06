package dataworkspace

import (
	"errors"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestMongoDBDocumentActionsRejectSQLAndUseQualifiedLayout(t *testing.T) {
	s := New(Dependencies{})
	d := s.OpenObject(1, contract.ObjectRef{ProfileID: 7, Catalog: "db", Kind: "collection", Name: "records"}, contract.Capabilities{Documents: true, EditableTables: true})
	if d.Kind() != "集合" {
		t.Fatal("document identity")
	}
	if _, err := mysqlDataSQLSelection(d, false); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("generated SQL for MongoDB", err)
	}
	s.openDataTransfer(d, "import")
	if d.transfer.Open {
		t.Fatal("SQL import opened for document database")
	}
	d.transfer.Format = "SQL"
	s.startDataExport(ui.Services{}, d)
	if d.transfer.Err == "" {
		t.Fatal("SQL export accepted")
	}
	if key := mysqlDataLayoutKey(d); len(key) < 19 || key[:19] != "engine_data_layout:" {
		t.Fatal("unqualified layout", key)
	}
}

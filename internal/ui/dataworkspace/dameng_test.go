package dataworkspace

import (
	"errors"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestDamengDocumentRejectsUnverifiedSQLExportAndRetainsNativeImportModes(t *testing.T) {
	s := New(Dependencies{})
	d := s.OpenObject(1, contract.ObjectRef{ProfileID: 7, Catalog: "DAMENG", Schema: "Native", Kind: "table", Name: "Records"}, contract.Capabilities{Query: true, Export: true, Import: true})
	d.importDialect = "dameng"
	if _, err := mysqlDataSQLSelection(d, false); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("native document generated MySQL SQL literals", err)
	}
	d.transfer.Format = "SQL"
	s.startDataExport(ui.Services{}, d)
	if d.transfer.Err == "" {
		t.Fatal("native SQL export accepted unsupported dialect")
	}
	if choices := mysqlDataImportTargetChoices(d); len(choices) != 2 {
		t.Fatal("native existing/new-table import not offered", choices)
	}
}

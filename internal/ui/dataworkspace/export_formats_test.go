package dataworkspace

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"testing"
)

func TestExplicitExportFormatsRejectUnverifiedSQLBeforeOpeningDestination(t *testing.T) {
	d := &Document{exportFormats: []string{"csv", "json"}}
	d.transfer.Format = "SQL"
	s := &State{}
	s.startDataExport(ui.Services{}, d)
	if d.transfer.Err != contract.ErrUnsupported.Error() || d.transfer.Busy {
		t.Fatal("unverified SQL export was admitted")
	}
	if !d.supportsExportFormat("CSV") || d.supportsExportFormat("Parquet") {
		t.Fatal("adapter export formats not applied")
	}
	if !(&Document{}).supportsExportFormat("SQL") {
		t.Fatal("legacy format availability changed")
	}
}

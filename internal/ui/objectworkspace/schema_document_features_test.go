package objectworkspace

import (
	"context"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestSchemaDocumentOnlyToolsRequireOperationDiscoveryAndOriginalReservation(t *testing.T) {
	for _, missing := range []string{"", "availability", "snapshot", "reservation", "design_table", "drop_table", "rename_table"} {
		t.Run("missing_"+missing, func(t *testing.T) {
			w := Session{ProfileID: 83, Generation: 5, Connected: true}
			deps := Dependencies{
				Session: func() Session { return w }, SessionFor: func(int64) Session { return w },
				Capabilities: func(int64) contract.Capabilities { return contract.Capabilities{DesignTables: true, Objects: true} },
				SchemaTarget: func(id int64, db, name string) contract.SchemaDesignTarget {
					return contract.SchemaDesignTarget{Object: contract.ObjectRef{ProfileID: id, Catalog: db, Kind: "table", Name: name}, Generation: 91}
				},
				SchemaDesignFor: func(int64) contract.SchemaDesignOperations {
					if missing == "snapshot" {
						return contract.SchemaDesignOperations{}
					}
					return contract.SchemaDesignOperations{Snapshot: func(context.Context, contract.SchemaDesignTarget) (contract.SchemaDesignSnapshot, error) {
						t.Fatal("tool discovery performed metadata I/O")
						return contract.SchemaDesignSnapshot{}, nil
					}}
				},
				SchemaDocumentsFor: func(int64) (contract.SchemaDesignDocumentsProvider, error) {
					if missing == "reservation" {
						return nil, contract.ErrUnsupported
					}
					return schemaOwnerTestProvider(func(contract.SchemaDesignDocumentRequest) (contract.SchemaDesignDocumentLease, error) {
						t.Fatal("tool discovery reserved a proof or executor")
						return nil, nil
					}), nil
				},
				Availability: func(_ int64, operation string, target contract.SchemaDesignTarget) contract.Availability {
					return contract.Availability{Enabled: operation != "copy_table" && operation != missing && target.Object.ProfileID == w.ProfileID}
				},
			}
			if missing == "availability" {
				deps.Availability = nil
			}
			s := New(deps)
			t.Cleanup(s.CloseAll)
			complete := missing != "availability" && missing != "snapshot" && missing != "reservation"
			if s.CanCreateTable(83, "main") != (complete && missing != "design_table") || s.CanDropTable(83, "main", "t") != (complete && missing != "drop_table") || s.CanRenameTable(83, "main", "t") != (complete && missing != "rename_table") {
				t.Fatal("document-only tools ignored missing discovery, scope or reservation")
			}
			if s.CanCopyTable(83, "main", "t") || s.CanChangeTable(83) || s.CanCreateTable(84, "main") || s.CanDropTable(84, "main", "t") || s.CanRenameTable(84, "main", "t") {
				t.Fatal("document-only feature discovery advertised copy, administration or another profile")
			}
		})
	}
}

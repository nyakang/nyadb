package dbruntime_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

type schemaDocumentFeaturesHandle struct {
	schemaDocumentHandle
	features contract.SchemaDesignDocumentFeatures
}

func (h *schemaDocumentFeaturesHandle) SchemaDesignDocumentFeatures() contract.SchemaDesignDocumentFeatures {
	return h.features
}

func TestSchemaDocumentFeaturesPublishOnlyBoundOriginalOperationsWithoutIOOrReservation(t *testing.T) {
	var calls, reads, reserves atomic.Int32
	h := &schemaDocumentFeaturesHandle{}
	h.schemaHandle.handle = handle{id: 1, caps: contract.Capabilities{DesignTables: true, Objects: true}}
	h.load = func(_ context.Context, target contract.SchemaDesignTarget) (contract.SchemaDesignSnapshot, error) {
		reads.Add(1)
		return schemaLeaseSnapshot(target), nil
	}
	h.reserve = func(contract.SchemaDesignDocumentRequest) (contract.SchemaDesignDocumentLease, error) {
		reserves.Add(1)
		return &schemaDocumentFake{}, nil
	}
	can := func(target contract.SchemaDesignTarget) bool {
		calls.Add(1)
		if target.Generation != 973 {
			t.Error("feature discovery borrowed public generation")
		}
		return target.Object.ProfileID == 1 && target.Object.Catalog == "main" && target.Object.Schema == "" && target.Object.Name == "t" && target.Object.Kind == "table"
	}
	h.features = contract.SchemaDesignDocumentFeatures{CanApply: can, CanDrop: can, CanRename: can}
	m := dbruntime.New()
	t.Cleanup(func() { _ = m.Close() })
	m.Register(connections.RQLite, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) { return h, nil }))
	p, _ := connections.Defaults(connections.RQLite)
	p.ID, p.Name, p.Host = 1, "document features", "localhost"
	published, err := m.ConnectSession(context.Background(), p, "")
	if err != nil {
		t.Fatal(err)
	}
	target := contract.SchemaDesignTarget{Object: contract.ObjectRef{ProfileID: 1, Catalog: "main", Kind: "table", Name: "t"}, Generation: published.Generation()}
	if !published.Capabilities().DesignTables || !published.Capabilities().Objects {
		t.Fatal("document-only features were clipped by session-level executor gates")
	}
	ops := m.SchemaDesign(1)
	if ops.Apply != nil || ops.Drop != nil || ops.Rename != nil || ops.Copy != nil {
		t.Fatal("feature discovery exposed a session executor")
	}
	features := m.SchemaDesignDocumentFeatures(1)
	if features.CanApply == nil || features.CanDrop == nil || features.CanRename == nil || features.CanCopy != nil {
		t.Fatal("document features changed supported operations")
	}
	for _, operation := range []string{"design_table", "drop_table", "rename_table"} {
		if !m.Availability(1, operation, target).Enabled {
			t.Fatal("original document operation unavailable", operation)
		}
	}
	if m.Availability(1, "copy_table", target).Enabled || reads.Load() != 0 || reserves.Load() != 0 {
		t.Fatal("feature discovery performed I/O, reserved a proof or advertised copy")
	}
	before := calls.Load()
	wrong := target
	wrong.Generation++
	if features.CanApply(wrong) || calls.Load() != before {
		t.Fatal("foreign public generation reached the raw provider")
	}
	wrong = target
	wrong.Object.Schema = "foreign"
	if features.CanApply(wrong) {
		t.Fatal("native feature accepted another namespace")
	}
	if err := m.Disconnect(1); err != nil {
		t.Fatal(err)
	}
	before = calls.Load()
	if features.CanApply(target) || calls.Load() != before || m.Availability(1, "design_table", target).Enabled {
		t.Fatal("captured original features survived disconnect")
	}
}

type schemaFeaturesWithoutReservation struct {
	*schemaHandle
	features contract.SchemaDesignDocumentFeatures
}

func (h *schemaFeaturesWithoutReservation) SchemaDesignDocumentFeatures() contract.SchemaDesignDocumentFeatures {
	return h.features
}

func TestSchemaDocumentFeaturesRequireBothProductCapabilityAndRealReservationProvider(t *testing.T) {
	for _, missing := range []string{"reservation", "snapshot", "product"} {
		t.Run(missing, func(t *testing.T) {
			features := contract.SchemaDesignDocumentFeatures{CanApply: func(contract.SchemaDesignTarget) bool { return true }, CanDrop: func(contract.SchemaDesignTarget) bool { return true }}
			load := func(context.Context, contract.SchemaDesignTarget) (contract.SchemaDesignSnapshot, error) {
				return contract.SchemaDesignSnapshot{}, nil
			}
			var raw contract.Session
			if missing == "reservation" {
				raw = &schemaFeaturesWithoutReservation{schemaHandle: &schemaHandle{handle: handle{id: 1, caps: contract.Capabilities{DesignTables: true, Objects: true}}, load: load}, features: features}
			} else {
				h := &schemaDocumentFeaturesHandle{features: features}
				h.id, h.caps, h.load = 1, contract.Capabilities{DesignTables: true, Objects: true}, load
				h.reserve = func(contract.SchemaDesignDocumentRequest) (contract.SchemaDesignDocumentLease, error) {
					return &schemaDocumentFake{}, nil
				}
				if missing == "snapshot" {
					h.load = nil
				}
				if missing == "product" {
					h.caps.DesignTables, h.caps.Objects = false, false
				}
				raw = h
			}
			m := dbruntime.New()
			t.Cleanup(func() { _ = m.Close() })
			m.Register(connections.SQLite, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) { return raw, nil }))
			s, err := m.ConnectSession(context.Background(), profile(1), "")
			if err != nil {
				t.Fatal(err)
			}
			if s.Capabilities().DesignTables || s.Capabilities().Objects {
				t.Fatal("incomplete original-document provider advertised executors")
			}
		})
	}
}

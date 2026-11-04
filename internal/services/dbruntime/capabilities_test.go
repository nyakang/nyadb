package dbruntime_test

import (
	"context"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

type maintenanceCapabilityHandle struct {
	handle
	onCapabilities func()
}

func (h *maintenanceCapabilityHandle) Capabilities() contract.Capabilities {
	if h.onCapabilities != nil {
		f := h.onCapabilities
		h.onCapabilities = nil
		f()
	}
	return h.caps
}
func (h *maintenanceCapabilityHandle) Maintenance() contract.MaintenanceOperations {
	return contract.MaintenanceOperations{
		Actions: func(contract.SchemaDesignTarget) []contract.MaintenanceAction {
			return []contract.MaintenanceAction{{ID: "VACUUM"}}
		},
		Plan: func(context.Context, contract.MaintenanceRequest) (contract.MaintenancePlan, error) {
			return contract.MaintenancePlan{}, nil
		},
		Execute: func(context.Context, contract.MaintenancePlan) contract.MaintenanceOutcome {
			return contract.MaintenanceOutcome{}
		},
	}
}

func TestMaintenanceAvailabilitySurvivesDisconnectionDuringLookup(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	h := &maintenanceCapabilityHandle{handle: handle{id: 1, caps: contract.Capabilities{Maintenance: true}}}
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) {
		return h, nil
	}))
	s, err := m.ConnectSession(context.Background(), profile(1), "")
	if err != nil {
		t.Fatal(err)
	}
	target := contract.SchemaDesignTarget{Object: contract.ObjectRef{ProfileID: 1, Catalog: "main", Schema: "main", Kind: "namespace"}, Generation: s.Generation()}
	if got := m.Availability(1, "maintenance", target); !got.Enabled {
		t.Fatal(got)
	}
	// A restore/disconnect can revoke the published session between reading
	// product capabilities and acquiring the bound maintenance operations.
	h.onCapabilities = func() {
		if err := m.Disconnect(1); err != nil {
			t.Fatal(err)
		}
	}
	if got := m.Availability(1, "maintenance", target); got.Enabled || got.Reason == "" {
		t.Fatal(got)
	}
}

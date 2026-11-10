package dbruntime_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

type recordedKVPlan struct {
	*kvFakePlan
	record contract.KVRequestRecord
}

func (p *recordedKVPlan) RecordedRequest() contract.KVRequestRecord { return p.record.Clone() }

type recordedKVLease struct {
	*kvFakeLease
	plan *recordedKVPlan
}

func (l *recordedKVLease) Prepare(request contract.KVRequest) (contract.KVPreparation, error) {
	l.plan = &recordedKVPlan{kvFakePlan: &kvFakePlan{request: request}, record: contract.KVRequestRecord{Request: contract.KVRequest{Method: request.Method, JSON: `{"name":" original user "}`}, OmittedFields: []string{"password"}}}
	return l.plan, nil
}

func TestKVOriginalAuthRecordFrozenAtPreparationAndRetirementDoesNotExposePassword(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	h := kvHandleFor(1)
	l := &recordedKVLease{kvFakeLease: &kvFakeLease{scope: h.scope.Clone()}}
	h.reserve = func(uint64) (contract.KVRequestLease, error) { return l, nil }
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) { return h, nil }))
	if err := m.Connect(context.Background(), profile(1), ""); err != nil {
		t.Fatal(err)
	}
	provider, _ := m.KVRequests(1)
	bound, _ := provider.ReserveKV(1)
	request := contract.KVRequest{Method: "/etcdserverpb.Auth/UserChangePassword", JSON: `{"name":" original user ","password":"original transient secret"}`}
	p, err := bound.Prepare(request)
	if err != nil || p.Snapshot() != request {
		t.Fatal("complete immutable authorization was altered", err)
	}
	view := p.(contract.KVPreparationRecord).RecordedRequest()
	view.OmittedFields[0] = "changed by caller"
	l.plan.record.OmittedFields[0] = "changed after preparation"
	if p.(contract.KVPreparationRecord).RecordedRequest().OmittedFields[0] != "password" {
		t.Fatal("runtime did not freeze original record")
	}
	if err := bound.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := bound.Execute(context.Background(), p)
	if !errors.Is(err, contract.ErrRevoked) || r.Attempted || l.executes.Load() != 0 || strings.Contains(r.Request.JSON, "original transient secret") || len(r.RequestOmittedFields) != 1 || r.RequestOmittedFields[0] != "password" {
		t.Fatal("early original retirement leaked authorization or dispatched", err)
	}
}

func TestKVOriginalAuthRecorderIsOptionalForExistingPreparations(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	h := kvHandleFor(1)
	l := &kvFakeLease{scope: h.scope.Clone()}
	h.reserve = func(uint64) (contract.KVRequestLease, error) { return l, nil }
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) { return h, nil }))
	if err := m.Connect(context.Background(), profile(1), ""); err != nil {
		t.Fatal(err)
	}
	provider, _ := m.KVRequests(1)
	bound, _ := provider.ReserveKV(1)
	request := contract.KVRequest{Method: "Put", JSON: "original"}
	p, _ := bound.Prepare(request)
	record := p.(contract.KVPreparationRecord).RecordedRequest()
	if record.Request != request || len(record.OmittedFields) != 0 {
		t.Fatal("existing non-secret preparation changed")
	}
}

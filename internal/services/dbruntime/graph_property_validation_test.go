package dbruntime_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

type propertyValidationHandle struct {
	*handle
	lease *propertyValidationLease
}
type propertyValidationLease struct {
	contract.QueryLease
	calls, released int
}

func (h *propertyValidationHandle) ReserveQuery(uint64) (contract.QueryLease, error) {
	return h.lease, nil
}
func (l *propertyValidationLease) Release() { l.released++ }
func (l *propertyValidationLease) ValidateGraphPropertyWrite(r contract.GraphPropertyWriteRequest) error {
	l.calls++
	r.After.Properties[0].Value.Text = "provider mutated its private copy"
	return contract.ErrUnsupported
}

func TestGraphPropertyPreviewValidationCapturesOriginalProviderAndRejectsRevocation(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	var handles []*propertyValidationHandle
	m.Register(connections.Neo4j, dbruntime.ProviderFunc(func(_ context.Context, p connections.Profile, _ string) (contract.Session, error) {
		h := &propertyValidationHandle{handle: &handle{id: p.ID, caps: contract.Capabilities{Query: true}}, lease: &propertyValidationLease{}}
		handles = append(handles, h)
		return h, nil
	}))
	p, _ := connections.Defaults(connections.Neo4j)
	p.ID, p.Name = 42, "Bound graph validation fixture"
	first, err := m.ConnectSession(context.Background(), p, "")
	if err != nil {
		t.Fatal(err)
	}
	query := first.(contract.QueryProvider)
	lease, err := query.ReserveQuery(7)
	if err != nil {
		t.Fatal(err)
	}
	validator, ok := lease.(contract.GraphPropertyWriteValidator)
	if !ok {
		t.Fatal("public query lease erased preview validation feature")
	}
	v, err := contract.ParseGraphPropertyExtended("point2d", `{"srid":7203,"x":1,"y":2}`)
	if err != nil {
		t.Fatal(err)
	}
	r := contract.GraphPropertyWriteRequest{Namespace: contract.NamespaceRef{ProfileID: 42, Catalog: "neo4j"}, After: v}
	if err = validator.ValidateGraphPropertyWrite(r); !errors.Is(err, contract.ErrUnsupported) || handles[0].lease.calls != 1 || r.After.Properties[0].Value.Text != "7203" {
		t.Fatal("bound preview validation lost provider error or mutated caller components", err)
	}
	foreign := r.Clone()
	foreign.Namespace.ProfileID = 99
	if err = validator.ValidateGraphPropertyWrite(foreign); !errors.Is(err, contract.ErrRevoked) || handles[0].lease.calls != 1 {
		t.Fatal("foreign profile reached original preview validator")
	}
	fresh, err := m.ConnectSession(context.Background(), p, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = validator.ValidateGraphPropertyWrite(r); !errors.Is(err, contract.ErrRevoked) || handles[0].lease.calls != 1 || handles[1].lease.calls != 0 {
		t.Fatal("retired preview reached the replacement provider")
	}
	lease.Release()
	first.Close()
	if !m.IsConnected(42) || m.Generation(42) != fresh.Generation() || handles[1].closed.Load() != 0 {
		t.Fatal("original preview cleanup closed replacement")
	}
}

package dbruntime_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

type lateGraphHandle struct {
	*handle
	started, release chan struct{}
}

func (h *lateGraphHandle) Namespaces(context.Context) ([]contract.Namespace, error) { return nil, nil }
func (h *lateGraphHandle) Browse(context.Context, contract.NamespaceRef) ([]contract.DBObject, error) {
	return nil, nil
}
func (h *lateGraphHandle) TableMetadata(context.Context, contract.ObjectRef) (*contract.TableStructure, error) {
	return nil, contract.ErrUnsupported
}
func (h *lateGraphHandle) GraphSchema(_ context.Context, n contract.NamespaceRef) (contract.GraphSchemaSnapshot, error) {
	close(h.started)
	<-h.release
	return contract.GraphSchemaSnapshot{Namespace: n}, nil
}

func TestNeo4jGraphMetadataRejectsLateOriginalGeneration(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	original := &lateGraphHandle{handle: &handle{id: 42, caps: contract.Capabilities{Metadata: true}}, started: make(chan struct{}), release: make(chan struct{})}
	next := &handle{id: 42}
	m.Register(connections.Neo4j, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) {
		if original.closed.Load() == 0 {
			return original, nil
		}
		return next, nil
	}))
	p, _ := connections.Defaults(connections.Neo4j)
	p.ID = 42
	p.Name = "Graph ownership fixture"
	first, e := m.ConnectSession(context.Background(), p, "")
	if e != nil {
		t.Fatal(e)
	}
	graph, e := contract.Require[contract.GraphMetadataProvider](first)
	if e != nil {
		t.Fatal(e)
	}
	out := make(chan error, 1)
	go func() {
		_, e := graph.GraphSchema(context.Background(), contract.NamespaceRef{ProfileID: 42, Catalog: "neo4j"})
		out <- e
	}()
	<-original.started
	// Replacement first closes the original raw handle, then publishes itself.
	fresh, e := m.ConnectSession(context.Background(), p, "")
	if e != nil {
		t.Fatal(e)
	}
	close(original.release)
	if e = <-out; !errors.Is(e, contract.ErrRevoked) {
		t.Fatal("late original graph metadata published", e)
	}
	first.Close()
	if m.Generation(42) != fresh.Generation() || next.closed.Load() != 0 {
		t.Fatal("original cleanup reached replacement")
	}
}

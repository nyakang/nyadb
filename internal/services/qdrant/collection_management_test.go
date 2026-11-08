package qdrant

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type forgedCollectionObservation struct {
	snapshot contract.CollectionManagementSnapshot
}

func (o forgedCollectionObservation) Snapshot() contract.CollectionManagementSnapshot {
	return o.snapshot
}

type forgedCollectionPlan struct{}

func (forgedCollectionPlan) Action() string { return "collection_delete" }
func (forgedCollectionPlan) Preview() contract.CollectionManagementRequest {
	return contract.CollectionManagementRequest{Method: "DELETE", Path: "/collections/owned"}
}
func (forgedCollectionPlan) Affected() []contract.NamespaceRef { return nil }

func TestQdrantCollectionManagementNeutralHandlesKeepOriginalProofAndBooleanEvidence(t *testing.T) {
	var mutations atomic.Int32
	s, _ := unitManagementLease(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/aliases" {
			fmt.Fprint(w, `{"status":"ok","result":{"aliases":[]}}`)
			return
		}
		if r.Method == "PATCH" {
			mutations.Add(1)
			fmt.Fprint(w, `{"status":"ok","result":false}`)
			return
		}
		fmt.Fprint(w, `{"status":"ok","result":{"status":"green","config":{"params":{"on_disk_payload":false},"metadata":{"old":true,"n":9007199254740993}}}}`)
	})
	o := contract.ObjectRef{ProfileID: s.ProfileID(), Kind: "collection_management", Name: "collection_management"}
	ref := s.Namespace("owned")
	for _, bad := range []contract.ObjectRef{{ProfileID: o.ProfileID + 1, Kind: o.Kind, Name: o.Name}, {ProfileID: o.ProfileID, Catalog: "owned", Kind: o.Kind, Name: o.Name}, {ProfileID: o.ProfileID, Schema: "public", Kind: o.Kind, Name: o.Name}} {
		if s.SupportsCollectionManagement(bad, "collection_update") {
			t.Fatal("unqualified/foreign management identity accepted")
		}
		if _, err := s.ReserveCollectionManagementDocument(bad, 2); !errors.Is(err, contract.ErrUnsupported) {
			t.Fatal(err)
		}
	}
	lease, err := s.ReserveCollectionManagementDocument(o, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if lease.Object() != o || lease.Generation() != s.Generation() {
		t.Fatal("native original identity lost")
	}
	before, err := lease.Read(context.Background(), []contract.NamespaceRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := before.Snapshot()
	if len(snapshot.Configurations) != 1 || !strings.Contains(snapshot.Configurations[0].JSON, "9007199254740993") || len(snapshot.Reads) != 3 {
		t.Fatal("complete qualified original snapshot lost")
	}
	snapshot.Targets[0].ProfileID++
	snapshot.Configurations[0].JSON = "{}"
	snapshot.Reads[0].Response.Body[0] = 'x'
	if before.Snapshot().Targets[0] != ref || before.Snapshot().Reads[0].Response.Body[0] == 'x' {
		t.Fatal("snapshot accessor shared native state")
	}
	draft := contract.CollectionManagementDraft{Action: "collection_update", Collection: ref, Configuration: `{"metadata":{}}`, TimeoutSeconds: 2}
	if _, err = lease.Prepare(forgedCollectionObservation{before.Snapshot()}, draft); !errors.Is(err, contract.ErrNamespaceChanged) {
		t.Fatal("forged observation accepted", err)
	}
	plan, err := lease.Prepare(before, draft)
	if err != nil {
		t.Fatal(err)
	}
	preview := plan.Preview()
	preview.Body[0] = 'x'
	affected := plan.Affected()
	affected[0].Catalog = "other"
	if string(plan.Preview().Body) != draft.Configuration || plan.Affected()[0] != ref {
		t.Fatal("mutable preview escaped immutable native handle")
	}
	other, err := s.ReserveCollectionManagementDocument(o, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err = other.Prepare(before, draft); !errors.Is(err, contract.ErrNamespaceChanged) {
		t.Fatal("foreign lease accepted original proof", err)
	}
	if _, err = other.Submit(context.Background(), plan); !errors.Is(err, ErrManagementUsed) {
		t.Fatal("foreign lease accepted original plan", err)
	}
	if _, err = lease.Submit(context.Background(), forgedCollectionPlan{}); !errors.Is(err, ErrManagementUsed) {
		t.Fatal("forged plan dispatched", err)
	}
	if mutations.Load() != 0 {
		t.Fatal("pure/foreign preparation dispatched")
	}
	r, err := lease.Submit(context.Background(), plan)
	if err != nil || !r.Receipt.HasResult || r.Receipt.Result || r.Receipt.Collection != ref || r.State != "response" {
		t.Fatal("native false receipt lost or prematurely completed", err, r.State)
	}
	r.Receipt.Response.Body[0] = 'x'
	r, err = lease.Verify(context.Background())
	if err != nil || r.State != "unchanged" || !r.Matched || !r.Receipt.HasResult || r.Receipt.Result || !strings.Contains(string(r.Receipt.Response.Body), `"result":false`) {
		t.Fatal("native false/original readback evidence changed", err, r.State)
	}
	if _, err = lease.Submit(context.Background(), plan); !errors.Is(err, ErrManagementUsed) || mutations.Load() != 1 {
		t.Fatal("neutral original plan replayed", err)
	}
	lease.Close()
	lease.Close()
	if _, err = lease.Read(context.Background(), []contract.NamespaceRef{ref}); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed original handle reopened", err)
	}
}

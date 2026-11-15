package rqlite

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func reserveSchemaUnit(t *testing.T, s *Session, id, name string) *schemaDocument {
	t.Helper()
	l, err := s.ReserveSchemaDesignDocument(contract.SchemaDesignDocumentRequest{DocumentID: id, Target: schemaUnitTarget(s, name)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l.(*schemaDocument)
}

func TestSchemaDocumentOwnsOriginalProofAndReleasesReservation(t *testing.T) {
	var requests atomic.Int32
	s := mockSession(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		fmt.Fprint(w, schemaUnitJSON(t, schemaUnitOriginal()))
	}, nil)
	first := reserveSchemaUnit(t, s, "first", "original")
	second := reserveSchemaUnit(t, s, "second", "original")
	ops := first.Operations()
	if s.SchemaDesign().Apply != nil || !s.Capabilities().DesignTables || !s.Capabilities().Objects || first.OriginalOutcome() != nil {
		t.Fatal("document tools exposed a session executor or synthesized a receipt")
	}
	original, err := ops.Snapshot(context.Background(), first.target)
	if err != nil {
		t.Fatal(err)
	}
	same, err := ops.Snapshot(context.Background(), first.target)
	if err != nil || same.Schema.Revision != original.Schema.Revision {
		t.Fatal("original document proof changed on a schema-only read", err)
	}
	peer, err := second.Operations().Snapshot(context.Background(), second.target)
	if err != nil || peer.Schema.Revision == original.Schema.Revision {
		t.Fatal("sibling document acquired original proof", err)
	}
	if _, err := s.schemaSnapshotGuardScoped(original, first.scope); err != nil {
		t.Fatal("owning document could not verify complete baseline", err)
	}
	for _, scope := range []string{s.dataScope, second.scope, ""} {
		if _, err := s.schemaSnapshotGuardScoped(original, scope); !errors.Is(err, schemaddl.ErrConflict) {
			t.Fatal("document proof transferred to another scope", err)
		}
	}
	invalid := first.target
	invalid.Object.Name = "other"
	if _, err := ops.Snapshot(context.Background(), invalid); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("leased provider accepted another original target", err)
	}
	if _, err := s.ReserveSchemaDesignDocument(contract.SchemaDesignDocumentRequest{DocumentID: first.id, Target: first.target}); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("duplicate live document ID accepted", err)
	}
	if requests.Load() != 3 {
		t.Fatal("invalid lease operation dispatched", requests.Load())
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if first.scope != "" {
		t.Fatal("closed document retained signing key")
	}
	if _, err := ops.Snapshot(context.Background(), first.target); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("captured binding survived close", err)
	}
	replacement := reserveSchemaUnit(t, s, "first", "original")
	if _, err := s.schemaSnapshotGuardScoped(original, replacement.scope); !errors.Is(err, schemaddl.ErrConflict) {
		t.Fatal("reused document ID inherited a closed document's proof", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if second.scope != "" || replacement.scope != "" || len(s.schemaDocuments) != 0 {
		t.Fatal("session close did not revoke all original document leases")
	}
}

func TestSchemaDocumentFeaturesPreserveQualifiedScopeAndNeverReserveProofOrRequest(t *testing.T) {
	var requests atomic.Int32
	s := mockSession(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		fmt.Fprint(w, schemaUnitJSON(t, schemaUnitOriginal()))
	}, nil)
	features := s.SchemaDesignDocumentFeatures()
	target := schemaUnitTarget(s, "original")
	if features.CanApply == nil || features.CanDrop == nil || features.CanRename == nil || features.CanCopy == nil || !features.CanApply(target) || !features.CanDrop(target) || !features.CanRename(target) || !features.CanCopy(target) {
		t.Fatal("original document feature discovery changed supported operations")
	}
	for _, change := range []func(*contract.SchemaDesignTarget){func(t *contract.SchemaDesignTarget) { t.Object.ProfileID++ }, func(t *contract.SchemaDesignTarget) { t.Generation++ }, func(t *contract.SchemaDesignTarget) { t.Object.Catalog = "other" }, func(t *contract.SchemaDesignTarget) { t.Object.Schema = "main" }, func(t *contract.SchemaDesignTarget) { t.Object.Kind = "view" }, func(t *contract.SchemaDesignTarget) { t.Object.Name = "sqlite_schema" }} {
		wrong := target
		change(&wrong)
		if features.CanApply(wrong) || features.CanDrop(wrong) || features.CanRename(wrong) {
			t.Fatal("feature discovery accepted another scope", wrong)
		}
	}
	if requests.Load() != 0 || len(s.schemaDocuments) != 0 {
		t.Fatal("feature discovery performed I/O or created a proof")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if features.CanApply(target) || features.CanDrop(target) || features.CanRename(target) {
		t.Fatal("captured feature survived original session close")
	}
}

func TestSchemaDocumentCancelCloseWaitsOriginalReadAcknowledgement(t *testing.T) {
	started, ack := make(chan struct{}), make(chan struct{})
	var requests atomic.Int32
	s := mockSession(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		close(started)
		<-ack
		fmt.Fprint(w, schemaUnitJSON(t, schemaUnitOriginal()))
	}, nil)
	d := reserveSchemaUnit(t, s, "held", "original")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		snapshot, err := d.Operations().Snapshot(ctx, d.target)
		if snapshot.Schema.SQLite != nil {
			err = errors.New("canceled baseline published")
		}
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("original read did not dispatch")
	}
	cancel()
	closed := make(chan struct{})
	go func() { _ = d.Close(); close(closed) }()
	select {
	case <-result:
		t.Fatal("cancellation abandoned original acknowledgement")
	case <-closed:
		t.Fatal("close abandoned original acknowledgement")
	case <-time.After(30 * time.Millisecond):
	}
	close(ack)
	select {
	case err := <-result:
		if !errors.Is(err, contract.ErrRevoked) && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("original read did not settle")
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("close did not settle after original acknowledgement")
	}
	if requests.Load() != 1 || !s.Alive() {
		t.Fatal("document cancellation replayed request or closed its session")
	}
}

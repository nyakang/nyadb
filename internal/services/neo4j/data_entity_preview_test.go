package neo4j

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type forgedEntityPlan struct{}

func (forgedEntityPlan) Snapshot() contract.DataMutationSnapshot {
	return contract.DataMutationSnapshot{}
}

func TestOriginalEditRegistrationProtectsSignedCaptureUntilCloseAcknowledgement(t *testing.T) {
	s := fakeSessionTest(t)
	l := dataLeaseTest(t, s, "label", "owned")
	entity := Value{Kind: "node", Node: &Node{ElementID: "original", Labels: []string{"owned"}}}
	snapshot := dataEditSnapshotTest(t, l, entity, nil)
	started, release := make(chan struct{}), make(chan struct{})
	prepared := make(chan error, 1)
	done := make(chan error, 1)
	go func() {
		_, err := l.prepareAndSaveEdits(context.Background(), func() ([]preparedDataEdit, error) {
			close(started)
			<-release
			p, err := l.prepareEntityEdits([]contract.DataEdit{{Kind: "delete", Original: snapshot}})
			prepared <- err
			return p, err
		})
		done <- err
	}()
	<-started
	closed := make(chan struct{})
	go func() { _ = l.Close(); close(closed) }()
	<-l.ctx.Done()
	select {
	case <-closed:
		t.Fatal("Close acknowledged before original signed capture settled")
	default:
	}
	close(release)
	if err := <-prepared; err != nil {
		t.Fatal("Close cleared original signature key before captured request acknowledgement", err)
	}
	if err := <-done; !errors.Is(err, context.Canceled) && !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("revoked original capture reached native dispatch or returned invalid-input evidence", err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not join the original capture")
	}
}
func (forgedEntityPlan) Close() error { return nil }

func TestEntityPreviewHandlesCannotFollowForeignDocumentsCloseOrCallerCopies(t *testing.T) {
	s := fakeSessionTest(t)
	l := dataLeaseTest(t, s, "label", "owned")
	p := &entityDataPlan{owner: l, prepared: []preparedDataEdit{{edit: contract.DataEdit{Kind: "insert"}}}, snapshot: contract.DataMutationSnapshot{Object: l.object, Edits: []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"property.bytes": {Kind: contract.Binary, Bytes: []byte{1}}}}}}}
	l.entityPlan = p
	x := p.Snapshot()
	x.Edits[0].Kind = "delete"
	x.Edits[0].Values["property.bytes"].Bytes[0] = 9
	if p.Snapshot().Edits[0].Kind != "insert" || p.Snapshot().Edits[0].Values["property.bytes"].Bytes[0] != 1 {
		t.Fatal("preview accessor exposed executor-owned state")
	}
	if _, err := l.submitEntityPlan(context.Background(), 42, "neo4j", "owned", forgedEntityPlan{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("caller-created preview was submission authorization", err)
	}
	foreign := dataLeaseTest(t, s, "label", "owned")
	if _, err := foreign.submitEntityPlan(context.Background(), 42, "neo4j", "owned", p); !errors.Is(err, ErrInvalid) {
		t.Fatal("preview crossed original documents", err)
	}
	_ = p.Close()
	_ = p.Close()
	if len(p.Snapshot().Edits) != 0 || l.entityPlan != nil || p.prepared != nil || p.snapshot.NativeJSON != "" {
		t.Fatal("preview close retained captured state")
	}
	if _, err := l.submitEntityPlan(context.Background(), 42, "neo4j", "owned", p); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed preview dispatched", err)
	}
	q := &entityDataPlan{owner: l, snapshot: contract.DataMutationSnapshot{NativeJSON: "original"}}
	l.entityPlan = q
	if err := l.Close(); err != nil || q.Snapshot().NativeJSON != "" || !q.closed {
		t.Fatal("original document close retained preview data", err)
	}
}

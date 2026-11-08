package qdrant

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type forgedSnapshotObservation struct{ metadata contract.SnapshotMetadata }

func TestQdrantSnapshotAdapterCloseJoinsRejectedOriginalStream(t *testing.T) {
	for _, sessionClose := range []bool{false, true} {
		t.Run(map[bool]string{false: "document", true: "session"}[sessionClose], func(t *testing.T) {
			s, _, _ := unitSnapshotLease(t)
			o := contract.ObjectRef{ProfileID: s.ProfileID(), Kind: "snapshot_management", Name: "snapshot_management"}
			l, err := s.ReserveSnapshotManagementDocument(o, 2)
			if err != nil {
				t.Fatal(err)
			}
			source := &blockingSnapshotSource{reading: make(chan struct{}), closing: make(chan struct{}), acknowledge: make(chan struct{}), release: make(chan struct{})}
			var once sync.Once
			ack := func() { once.Do(func() { close(source.acknowledge) }) }
			t.Cleanup(ack)
			done := make(chan error, 1)
			go func() {
				_, e := l.Upload(context.Background(), forgedSnapshotPlan{}, source, contract.SnapshotTransferOptions{MaxBytes: 1})
				done <- e
			}()
			waitSessionSignal(t, source.closing)
			closed := make(chan struct{})
			go func() {
				if sessionClose {
					s.Close()
				} else {
					l.Close()
				}
				close(closed)
			}()
			select {
			case <-closed:
				t.Fatal("close settled before rejected original source acknowledgement")
			case <-time.After(30 * time.Millisecond):
			}
			ack()
			waitSessionSignal(t, closed)
			if err = <-done; !errors.Is(err, ErrSnapshotUsed) || source.closes.Load() != 1 || source.reads.Load() != 0 {
				t.Fatal("rejected source dispatched/closed repeatedly", err)
			}
		})
	}
}

func (o forgedSnapshotObservation) Snapshot() contract.SnapshotMetadata { return o.metadata }

type forgedSnapshotPlan struct{}

func (forgedSnapshotPlan) Action() string                    { return "upload" }
func (forgedSnapshotPlan) Target() contract.SnapshotTarget   { return contract.SnapshotTarget{} }
func (forgedSnapshotPlan) Preview() contract.SnapshotRequest { return contract.SnapshotRequest{} }
func (forgedSnapshotPlan) UploadSource() (contract.SnapshotSource, bool) {
	return contract.SnapshotSource{}, true
}
func (forgedSnapshotPlan) RestoresData() bool { return true }

func TestQdrantSnapshotAdapterQualifiedTargetsAndImmutableHandles(t *testing.T) {
	s, _, h := unitSnapshotLease(t)
	o := contract.ObjectRef{ProfileID: s.ProfileID(), Kind: "snapshot_management", Name: "snapshot_management"}
	target := contract.SnapshotTarget{Scope: contract.SnapshotCollection, Collection: s.Namespace("owned")}
	for _, bad := range []contract.SnapshotTarget{{Scope: contract.SnapshotStorage, Collection: target.Collection}, {Scope: contract.SnapshotCollection, Collection: contract.NamespaceRef{ProfileID: o.ProfileID + 1, Catalog: "owned"}}, {Scope: contract.SnapshotShard, Collection: contract.NamespaceRef{ProfileID: o.ProfileID, Catalog: "owned", Schema: "public"}}, {Scope: contract.SnapshotCollection, Collection: target.Collection, Shard: 1}} {
		if s.SupportsSnapshots(o, bad, "create") {
			t.Fatal("foreign/unqualified snapshot capability published")
		}
	}
	if s.SupportsSnapshots(o, contract.SnapshotTarget{Scope: contract.SnapshotStorage}, "recover") || s.SupportsSnapshots(o, target, "stream") {
		t.Fatal("unsupported storage recovery/collection stream advertised")
	}
	badObject := o
	badObject.Catalog = "owned"
	if _, err := s.ReserveSnapshotManagementDocument(badObject, 2); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal(err)
	}
	l, err := s.ReserveSnapshotManagementDocument(o, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	before, err := l.Read(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	m := before.Snapshot()
	m.Collections[0].ProfileID++
	m.Reads[0].Response.Body[0] = 'x'
	m.Snapshots[0].Raw[0] = 'x'
	if before.Snapshot().Collections[0].ProfileID != o.ProfileID || before.Snapshot().Snapshots[0].Raw[0] == 'x' || before.Snapshot().Reads[0].Response.Body[0] == 'x' {
		t.Fatal("neutral observation exposes original buffers")
	}
	if _, err = l.Prepare(forgedSnapshotObservation{before.Snapshot()}, contract.SnapshotDraft{Action: "create"}); !errors.Is(err, contract.ErrNamespaceChanged) {
		t.Fatal(err)
	}
	for _, draft := range []contract.SnapshotDraft{{Action: "create", Source: contract.SnapshotSource{Name: "foreign"}}, {Action: "download", Wait: true, Selected: before.Snapshot().Snapshots[0]}, {Action: "upload", Recovery: contract.SnapshotRecovery{Location: "foreign"}}} {
		if _, err = l.Prepare(before, draft); !errors.Is(err, ErrInvalid) {
			t.Fatal("unsupported draft fields silently accepted", err)
		}
	}
	p, err := l.Prepare(before, contract.SnapshotDraft{Action: "create", Wait: true})
	if err != nil {
		t.Fatal(err)
	}
	preview := p.Preview()
	preview.Path = "/foreign"
	if p.Preview().Path == preview.Path || p.Target() != target || p.RestoresData() {
		t.Fatal("neutral plan changed original authorization")
	}
	other, err := s.ReserveSnapshotManagementDocument(o, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err = other.Prepare(before, contract.SnapshotDraft{Action: "create"}); !errors.Is(err, contract.ErrNamespaceChanged) {
		t.Fatal(err)
	}
	sink := newSnapshotSink()
	if _, err = other.Download(context.Background(), p, sink, contract.SnapshotTransferOptions{MaxBytes: 100}); !errors.Is(err, ErrSnapshotUsed) || sink.closed.Load() != 1 {
		t.Fatal("foreign stream was not closed/rejected", err)
	}
	if _, err = l.Submit(context.Background(), forgedSnapshotPlan{}); !errors.Is(err, ErrSnapshotUsed) || h.mutations.Load() != 0 {
		t.Fatal("forged plan reached network", err)
	}
	r, err := l.Submit(context.Background(), p)
	if err != nil || r.State != "response" || r.Object != o || r.Target != target {
		t.Fatal(err, r.State)
	}
	r.Receipt.Description.Raw[0] = 'x'
	r, err = l.Verify(context.Background())
	if err != nil || r.State != "applied" || !r.Matched || r.Receipt.Description.Raw[0] == 'x' {
		t.Fatal("original archive evidence lost", err)
	}
	if _, err = l.Submit(context.Background(), p); !errors.Is(err, ErrSnapshotUsed) || h.mutations.Load() != 1 {
		t.Fatal("neutral plan replayed", err)
	}
}

func TestQdrantSnapshotAdapterTransfersNullFalseAndExactCollectionCount(t *testing.T) {
	for _, scope := range []contract.SnapshotScope{contract.SnapshotCollection, contract.SnapshotShard} {
		t.Run(string(scope), func(t *testing.T) {
			s, _, h := unitSnapshotLease(t)
			o := contract.ObjectRef{ProfileID: s.ProfileID(), Kind: "snapshot_management", Name: "snapshot_management"}
			l, err := s.ReserveSnapshotManagementDocument(o, 2)
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			target := contract.SnapshotTarget{Scope: scope, Collection: s.Namespace("owned")}
			before, err := l.Read(context.Background(), target)
			if err != nil {
				t.Fatal(err)
			}
			selected := before.Snapshot().Snapshots[0]
			p, err := l.Prepare(before, contract.SnapshotDraft{Action: "download", Selected: selected})
			if err != nil {
				t.Fatal(err)
			}
			sink := newSnapshotSink()
			r, err := l.Download(context.Background(), p, sink, contract.SnapshotTransferOptions{MaxBytes: 100})
			if err != nil || !r.Transfer.Complete || r.Transfer.SHA256 != snapshotHash(h.archive) || r.Transfer.Received != uint64(len(h.archive)) || sink.closed.Load() != 1 {
				t.Fatal("neutral archive transfer", err)
			}
			r.Transfer.Prefix[0] = 'x'
			before, err = l.Read(context.Background(), target)
			if err != nil {
				t.Fatal(err)
			}
			source := contract.SnapshotSource{Name: "restore.snapshot", Size: uint64(len(h.archive)), SHA256: snapshotHash(h.archive)}
			p, err = l.Prepare(before, contract.SnapshotDraft{Action: "upload", Wait: true, Source: source, Recovery: contract.SnapshotRecovery{Priority: "snapshot"}})
			if err != nil {
				t.Fatal(err)
			}
			if got, ok := p.UploadSource(); !ok || got != source || !p.RestoresData() {
				t.Fatal("prepared source changed")
			}
			r, err = l.Upload(context.Background(), p, io.NopCloser(bytes.NewReader(h.archive)), contract.SnapshotTransferOptions{MaxBytes: 100})
			if err != nil || r.Receipt.HasNullResult != (scope == contract.SnapshotShard) || r.Receipt.HasResult != (scope == contract.SnapshotCollection) || r.Receipt.SourceSHA256 != source.SHA256 {
				t.Fatal("null/boolean/source evidence collapsed", err)
			}
			r.Receipt.Response.Body[0] = 'x'
			r, err = l.Verify(context.Background())
			if err != nil || r.State != "observed" || r.Matched || !r.HasCount || r.Count != ^uint64(0) || r.Receipt.Response.Body[0] == 'x' {
				t.Fatal("exact collection observation lost/fabricated proof", err)
			}
			before, err = l.Read(context.Background(), target)
			if err != nil {
				t.Fatal(err)
			}
			h.mu.Lock()
			h.falseDelete = true
			h.mu.Unlock()
			p, err = l.Prepare(before, contract.SnapshotDraft{Action: "delete", Wait: true, Selected: before.Snapshot().Snapshots[0]})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = l.Submit(context.Background(), p); err != nil {
				t.Fatal(err)
			}
			r, err = l.Verify(context.Background())
			if err != nil || r.State != "unchanged" || !r.Receipt.HasResult || r.Receipt.Result {
				t.Fatal("native false collapsed", err)
			}
		})
	}
}

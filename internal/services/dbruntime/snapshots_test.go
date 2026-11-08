package dbruntime_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

type snapshotHandle struct {
	tasksHandle
	documents map[uint64]*snapshotLease
}

func (h *snapshotHandle) SupportsSnapshots(o contract.ObjectRef, t contract.SnapshotTarget, action string) bool {
	return o.ProfileID == h.id && action == "upload" && t.Collection.ProfileID == h.id
}
func (h *snapshotHandle) ReserveSnapshotManagementDocument(o contract.ObjectRef, id uint64) (contract.SnapshotLease, error) {
	l := &snapshotLease{object: o}
	h.documents[id] = l
	return l, nil
}

type snapshotLease struct {
	object                                                         contract.ObjectRef
	reads, prepares, submits, uploads, downloads, verifies, closed atomic.Int32
	closeOnce                                                      sync.Once
}
type snapshotObservation struct{ m contract.SnapshotMetadata }

func (o *snapshotObservation) Snapshot() contract.SnapshotMetadata { return o.m }

type snapshotPlan struct {
	target  contract.SnapshotTarget
	source  contract.SnapshotSource
	request contract.SnapshotRequest
}

func (p *snapshotPlan) Action() string                                { return "upload" }
func (p *snapshotPlan) Target() contract.SnapshotTarget               { return p.target }
func (p *snapshotPlan) Preview() contract.SnapshotRequest             { return p.request }
func (p *snapshotPlan) UploadSource() (contract.SnapshotSource, bool) { return p.source, true }
func (p *snapshotPlan) RestoresData() bool                            { return true }
func (l *snapshotLease) Object() contract.ObjectRef                   { return l.object }
func (l *snapshotLease) Generation() uint64                           { return 1 }
func (l *snapshotLease) Read(_ context.Context, target contract.SnapshotTarget) (contract.SnapshotObservation, error) {
	l.reads.Add(1)
	return &snapshotObservation{contract.SnapshotMetadata{Object: l.object, Target: target, Collections: []contract.NamespaceRef{target.Collection}, Snapshots: []contract.SnapshotDescription{{Name: "original.snapshot", Size: ^uint64(0), Raw: []byte(`{"size":18446744073709551615}`)}}, Reads: []contract.SnapshotRead{{Response: contract.SnapshotResponse{Body: []byte("original")}}}}}, nil
}
func (l *snapshotLease) Prepare(o contract.SnapshotObservation, d contract.SnapshotDraft) (contract.SnapshotPlan, error) {
	l.prepares.Add(1)
	return &snapshotPlan{target: o.Snapshot().Target, source: d.Source, request: contract.SnapshotRequest{Method: "POST", Path: "/owned", Body: d.Selected.Raw}}, nil
}
func (l *snapshotLease) receipt() contract.SnapshotResult {
	return contract.SnapshotResult{Object: l.object, Action: "upload", State: "response", Receipt: contract.SnapshotReceipt{Action: "upload", State: "completed", HasResult: true, Result: false, Response: contract.SnapshotResponse{Body: []byte(`{"result":false}`)}, Description: &contract.SnapshotDescription{Raw: []byte("original")}, SourceBytesRead: ^uint64(0), SourceSHA256: "original"}, Transfer: contract.SnapshotTransfer{Prefix: []byte("original"), Response: contract.SnapshotResponse{Body: []byte("original")}}}
}
func (l *snapshotLease) Submit(context.Context, contract.SnapshotPlan) (contract.SnapshotResult, error) {
	l.submits.Add(1)
	return l.receipt(), nil
}
func (l *snapshotLease) Upload(_ context.Context, _ contract.SnapshotPlan, r io.ReadCloser, _ contract.SnapshotTransferOptions) (contract.SnapshotResult, error) {
	l.uploads.Add(1)
	defer r.Close()
	return l.receipt(), nil
}
func (l *snapshotLease) Download(_ context.Context, _ contract.SnapshotPlan, w io.WriteCloser, _ contract.SnapshotTransferOptions) (contract.SnapshotResult, error) {
	l.downloads.Add(1)
	defer w.Close()
	return l.receipt(), nil
}
func (l *snapshotLease) Verify(context.Context) (contract.SnapshotResult, error) {
	l.verifies.Add(1)
	r := l.receipt()
	r.State = "indeterminate"
	return r, errors.New("readback unavailable")
}
func (l *snapshotLease) Close() error { l.closeOnce.Do(func() { l.closed.Add(1) }); return nil }

type snapshotAckStream struct {
	entered, release chan struct{}
	closed           atomic.Int32
}

func (s *snapshotAckStream) Read([]byte) (int, error)    { return 0, io.EOF }
func (s *snapshotAckStream) Write(p []byte) (int, error) { return len(p), nil }
func (s *snapshotAckStream) Close() error                { s.closed.Add(1); close(s.entered); <-s.release; return nil }

func TestSnapshotsPublishedGenerationHandlesRetainedEvidenceAndRejectedStreamAcknowledgement(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	var handles []*snapshotHandle
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(_ context.Context, p connections.Profile, _ string) (contract.Session, error) {
		h := &snapshotHandle{tasksHandle: tasksHandle{handle: handle{id: p.ID, caps: contract.Capabilities{Metadata: true}}}, documents: map[uint64]*snapshotLease{}}
		handles = append(handles, h)
		return h, nil
	}))
	if err := m.Connect(context.Background(), profile(2), ""); err != nil {
		t.Fatal(err)
	}
	first, err := m.ConnectSession(context.Background(), profile(1), "")
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.Snapshots(1)
	if err != nil {
		t.Fatal(err)
	}
	o := contract.ObjectRef{ProfileID: 1, Kind: "snapshot_management", Name: "snapshot_management"}
	target := contract.SnapshotTarget{Scope: contract.SnapshotShard, Collection: contract.NamespaceRef{ProfileID: 1, Catalog: "owned"}}
	foreign := o
	foreign.ProfileID = 2
	if p.SupportsSnapshots(foreign, target, "upload") {
		t.Fatal("foreign snapshot capability")
	}
	if _, err = p.ReserveSnapshotManagementDocument(foreign, 1); err == nil {
		t.Fatal("foreign snapshot lease")
	}
	l, err := p.ReserveSnapshotManagementDocument(o, 1)
	if err != nil {
		t.Fatal(err)
	}
	if l.Generation() != first.Generation() || l.Generation() == handles[1].documents[1].Generation() {
		t.Fatal("raw generation leaked")
	}
	before, err := l.Read(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	md := before.Snapshot()
	md.Collections[0].Catalog = "foreign"
	md.Snapshots[0].Raw[0] = 'x'
	md.Reads[0].Response.Body[0] = 'x'
	if before.Snapshot().Collections[0] != target.Collection || before.Snapshot().Snapshots[0].Raw[0] == 'x' || before.Snapshot().Reads[0].Response.Body[0] == 'x' {
		t.Fatal("public observation shares original storage")
	}
	draft := contract.SnapshotDraft{Action: "upload", Selected: contract.SnapshotDescription{Raw: []byte("original")}, Source: contract.SnapshotSource{Name: "original", Size: ^uint64(0)}}
	if _, err = l.Prepare(&snapshotObservation{before.Snapshot()}, draft); !errors.Is(err, contract.ErrNamespaceChanged) {
		t.Fatal("forged observation", err)
	}
	plan, err := l.Prepare(before, draft)
	if err != nil {
		t.Fatal(err)
	}
	draft.Selected.Raw[0] = 'x'
	preview := plan.Preview()
	preview.Body[0] = 'x'
	if string(plan.Preview().Body) != "original" {
		t.Fatal("draft/preview mutated original plan")
	}
	other, err := p.ReserveSnapshotManagementDocument(o, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err = other.Prepare(before, draft); !errors.Is(err, contract.ErrNamespaceChanged) {
		t.Fatal(err)
	}
	if _, err = other.Submit(context.Background(), plan); !errors.Is(err, contract.ErrNamespaceChanged) {
		t.Fatal(err)
	}
	r, err := l.Submit(context.Background(), plan)
	if err != nil || !r.Receipt.HasResult || r.Receipt.Result || r.Receipt.SourceBytesRead != ^uint64(0) {
		t.Fatal("false/uint64 receipt lost", err)
	}
	r.Receipt.Response.Body[0] = 'x'
	r.Receipt.Description.Raw[0] = 'x'
	r.Transfer.Prefix[0] = 'x'
	second, err := m.ConnectSession(context.Background(), profile(1), "")
	if err != nil || second.Generation() <= first.Generation() {
		t.Fatal(err)
	}
	if p.SupportsSnapshots(o, target, "upload") {
		t.Fatal("old provider followed replacement")
	}
	if _, err = l.Read(context.Background(), target); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal(err)
	}
	if _, err = l.Prepare(before, draft); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal(err)
	}
	for _, op := range []func() (contract.SnapshotResult, error){func() (contract.SnapshotResult, error) { return l.Submit(context.Background(), plan) }, func() (contract.SnapshotResult, error) { return l.Verify(context.Background()) }} {
		r, err = op()
		if !errors.Is(err, contract.ErrRevoked) || r.State != "indeterminate" || !r.Receipt.HasResult || r.Receipt.Result || r.Receipt.Description.Raw[0] == 'x' || r.Transfer.Prefix[0] == 'x' {
			t.Fatal("replacement discarded/replayed original evidence", err)
		}
		r.Receipt.Description.Raw[0] = 'x'
	}
	for _, upload := range []bool{true, false} {
		stream := &snapshotAckStream{entered: make(chan struct{}), release: make(chan struct{})}
		done := make(chan error, 1)
		go func() {
			var e error
			if upload {
				_, e = l.Upload(context.Background(), plan, stream, contract.SnapshotTransferOptions{MaxBytes: 1})
			} else {
				_, e = l.Download(context.Background(), plan, stream, contract.SnapshotTransferOptions{MaxBytes: 1})
			}
			done <- e
		}()
		select {
		case <-stream.entered:
		case <-time.After(2 * time.Second):
			t.Fatal("original stream close not requested")
		}
		select {
		case <-done:
			t.Fatal("rejection settled before original stream close acknowledgement")
		default:
		}
		close(stream.release)
		if e := <-done; !errors.Is(e, contract.ErrRevoked) || stream.closed.Load() != 1 {
			t.Fatal(e)
		}
	}
	current, err := m.Snapshots(1)
	if err != nil {
		t.Fatal(err)
	}
	newLease, err := current.ReserveSnapshotManagementDocument(o, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer newLease.Close()
	if _, err = newLease.Prepare(before, draft); !errors.Is(err, contract.ErrNamespaceChanged) {
		t.Fatal("replacement accepted old observation", err)
	}
	var closers sync.WaitGroup
	for range 3 {
		closers.Go(func() { l.Close() })
	}
	closers.Wait()
	old := handles[1].documents[1]
	if old.reads.Load() != 1 || old.prepares.Load() != 1 || old.submits.Load() != 1 || old.uploads.Load() != 0 || old.downloads.Load() != 0 || old.verifies.Load() != 0 || old.closed.Load() != 1 || handles[2].documents[1].closed.Load() != 0 || !m.IsConnected(1) {
		t.Fatal("old work/close crossed replacement")
	}
}

func TestSnapshotsCloseJoinsRejectedOriginalStreams(t *testing.T) {
	for _, sessionClose := range []bool{false, true} {
		t.Run(map[bool]string{false: "document", true: "session"}[sessionClose], func(t *testing.T) {
			m := dbruntime.New()
			defer m.Close()
			m.Register(connections.SQLite, dbruntime.ProviderFunc(func(_ context.Context, p connections.Profile, _ string) (contract.Session, error) {
				return &snapshotHandle{tasksHandle: tasksHandle{handle: handle{id: p.ID, caps: contract.Capabilities{Metadata: true}}}, documents: map[uint64]*snapshotLease{}}, nil
			}))
			if err := m.Connect(context.Background(), profile(1), ""); err != nil {
				t.Fatal(err)
			}
			p, err := m.Snapshots(1)
			if err != nil {
				t.Fatal(err)
			}
			l, err := p.ReserveSnapshotManagementDocument(contract.ObjectRef{ProfileID: 1, Kind: "snapshot_management", Name: "snapshot_management"}, 1)
			if err != nil {
				t.Fatal(err)
			}
			stream := &snapshotAckStream{entered: make(chan struct{}), release: make(chan struct{})}
			var once sync.Once
			ack := func() { once.Do(func() { close(stream.release) }) }
			defer ack()
			done := make(chan error, 1)
			go func() {
				_, e := l.Upload(context.Background(), &snapshotPlan{}, stream, contract.SnapshotTransferOptions{MaxBytes: 1})
				done <- e
			}()
			select {
			case <-stream.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("rejected source not closed")
			}
			closed := make(chan struct{})
			go func() {
				if sessionClose {
					m.Disconnect(1)
				} else {
					l.Close()
				}
				close(closed)
			}()
			select {
			case <-closed:
				t.Fatal("published close settled before original rejected source acknowledgement")
			case <-time.After(30 * time.Millisecond):
			}
			ack()
			select {
			case <-closed:
			case <-time.After(2 * time.Second):
				t.Fatal("published close did not settle after acknowledgement")
			}
			if e := <-done; !errors.Is(e, contract.ErrNamespaceChanged) || stream.closed.Load() != 1 {
				t.Fatal(e)
			}
		})
	}
}

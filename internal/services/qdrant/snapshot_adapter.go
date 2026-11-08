package qdrant

import (
	"bytes"
	"context"
	"io"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func (s *Session) snapshotObject(o contract.ObjectRef) bool {
	return o == (contract.ObjectRef{ProfileID: s.ProfileID(), Kind: "snapshot_management", Name: "snapshot_management"})
}
func (s *Session) snapshotTarget(t contract.SnapshotTarget) (SnapshotTarget, error) {
	n := SnapshotTarget{Scope: SnapshotScope(t.Scope), Shard: t.Shard}
	if t.Scope == contract.SnapshotStorage {
		if t.Collection != (contract.NamespaceRef{}) {
			return n, ErrInvalid
		}
	} else {
		if t.Collection.ProfileID != s.ProfileID() || t.Collection.Schema != "" {
			return n, ErrInvalid
		}
		n.Collection = t.Collection.Catalog
	}
	_, err := n.path()
	return n, err
}
func (s *Session) SupportsSnapshots(o contract.ObjectRef, target contract.SnapshotTarget, action string) bool {
	if !s.Alive() || !s.snapshotObject(o) {
		return false
	}
	t, err := s.snapshotTarget(target)
	if err != nil {
		return false
	}
	switch action {
	case "read", "create", "delete", "download":
		return true
	case "upload", "recover":
		return t.Scope != StorageSnapshots
	case "stream":
		return t.Scope == ShardSnapshots
	}
	return false
}
func (s *Session) ReserveSnapshotManagementDocument(o contract.ObjectRef, document uint64) (contract.SnapshotLease, error) {
	if !s.Alive() {
		return nil, contract.ErrRevoked
	}
	if !s.snapshotObject(o) {
		return nil, contract.ErrUnsupported
	}
	l, err := s.ReserveSnapshotDocument(document)
	if err != nil {
		return nil, err
	}
	return &snapshotAdapterDocument{object: o, native: l}, nil
}

type snapshotAdapterDocument struct {
	object    contract.ObjectRef
	native    *SnapshotLease
	streamMu  sync.Mutex
	streams   sync.WaitGroup
	closed    bool
	closeOnce sync.Once
	closeErr  error
}

func (d *snapshotAdapterDocument) stream() func() {
	d.streamMu.Lock()
	defer d.streamMu.Unlock()
	if d.closed {
		return func() {}
	}
	d.streams.Add(1)
	_, release, err := d.native.session.request(context.Background())
	if err != nil {
		release = func() {}
	}
	return func() { d.streams.Done(); release() }
}

type snapshotAdapterObservation struct {
	owner  *snapshotAdapterDocument
	native SessionSnapshotObservation
}
type snapshotAdapterPlan struct {
	owner  *snapshotAdapterDocument
	native SessionSnapshotPlan
}

func (d *snapshotAdapterDocument) Object() contract.ObjectRef { return d.object }
func (d *snapshotAdapterDocument) Generation() uint64         { return d.native.session.Generation() }
func (d *snapshotAdapterDocument) target(t SnapshotTarget) contract.SnapshotTarget {
	out := contract.SnapshotTarget{Scope: contract.SnapshotScope(t.Scope), Shard: t.Shard}
	if t.Collection != "" {
		out.Collection = d.native.session.Namespace(t.Collection)
	}
	return out
}
func snapshotDescription(d SnapshotDescription) contract.SnapshotDescription {
	return contract.SnapshotDescription{Name: d.Name, CreationTime: d.CreationTime, Checksum: d.Checksum, Size: d.Size, HasCreationTime: d.HasCreationTime, Raw: bytes.Clone(d.Raw)}
}
func snapshotResponse(r Response) contract.SnapshotResponse {
	return contract.SnapshotResponse{Status: r.Status, ContentType: r.ContentType, Body: bytes.Clone(r.Body), Incomplete: r.Incomplete}
}
func (d *snapshotAdapterDocument) metadata(m SnapshotMetadata) contract.SnapshotMetadata {
	out := contract.SnapshotMetadata{Object: d.object, Target: d.target(m.Target), Configuration: string(m.Config)}
	for _, name := range m.Collections {
		out.Collections = append(out.Collections, d.native.session.Namespace(name))
	}
	for _, a := range m.Aliases {
		out.Aliases = append(out.Aliases, contract.CollectionAlias{Alias: d.native.session.Namespace(a.Alias), Collection: d.native.session.Namespace(a.Collection)})
	}
	for _, s := range m.Snapshots {
		out.Snapshots = append(out.Snapshots, snapshotDescription(s))
	}
	for _, r := range m.Reads {
		out.Reads = append(out.Reads, contract.SnapshotRead{Response: snapshotResponse(r.Response), BodyTruncated: r.BodyTruncated, OriginalBodyBytes: r.OriginalBodyBytes})
	}
	return out
}
func (o *snapshotAdapterObservation) Snapshot() contract.SnapshotMetadata {
	return o.owner.metadata(o.native.Snapshot())
}
func (p *snapshotAdapterPlan) Action() string { return p.native.Action() }
func (p *snapshotAdapterPlan) Target() contract.SnapshotTarget {
	return p.owner.target(p.native.Target())
}
func (p *snapshotAdapterPlan) Preview() contract.SnapshotRequest {
	r := p.native.Preview()
	return contract.SnapshotRequest{Method: r.Method, Path: r.Path, ContentType: r.ContentType, Body: r.Body}
}
func (p *snapshotAdapterPlan) UploadSource() (contract.SnapshotSource, bool) {
	name, size, checksum, ok := p.native.UploadSource()
	return contract.SnapshotSource{Name: name, Size: size, SHA256: checksum}, ok
}
func (p *snapshotAdapterPlan) RestoresData() bool { return p.native.RestoresData() }
func (d *snapshotAdapterDocument) Read(ctx context.Context, target contract.SnapshotTarget) (contract.SnapshotObservation, error) {
	t, err := d.native.session.snapshotTarget(target)
	if err != nil {
		return nil, err
	}
	o, err := d.native.Read(ctx, t)
	return &snapshotAdapterObservation{owner: d, native: o}, err
}
func (d *snapshotAdapterDocument) Prepare(before contract.SnapshotObservation, draft contract.SnapshotDraft) (contract.SnapshotPlan, error) {
	o, ok := before.(*snapshotAdapterObservation)
	if !ok || o == nil || o.owner != d {
		return nil, contract.ErrNamespaceChanged
	}
	draft = contract.CloneSnapshotDraft(draft)
	selected := draft.Selected
	hasSelected := selected.Name != "" || selected.CreationTime != "" || selected.Checksum != "" || selected.Size != 0 || selected.HasCreationTime || len(selected.Raw) != 0
	hasSource := draft.Source != (contract.SnapshotSource{})
	hasRecovery := draft.Recovery != (contract.SnapshotRecovery{})
	if draft.Wait && (draft.Action == "download" || draft.Action == "stream") {
		return nil, ErrInvalid
	}
	if hasSelected && draft.Action != "delete" && draft.Action != "download" || hasSource && draft.Action != "upload" || hasRecovery && draft.Action != "recover" && draft.Action != "upload" || draft.Action == "upload" && (draft.Recovery.Location != "" || draft.Recovery.Checksum != "") {
		return nil, ErrInvalid
	}
	var p SessionSnapshotPlan
	var err error
	switch draft.Action {
	case "create":
		p, err = d.native.PrepareCreate(o.native, draft.Wait)
	case "delete":
		p, err = d.native.PrepareDelete(o.native, SnapshotDescription{Name: selected.Name, CreationTime: selected.CreationTime, Checksum: selected.Checksum, Size: selected.Size, HasCreationTime: selected.HasCreationTime, Raw: bytes.Clone(selected.Raw)}, draft.Wait)
	case "recover":
		p, err = d.native.PrepareRecovery(o.native, SnapshotRecovery{Location: draft.Recovery.Location, Checksum: draft.Recovery.Checksum, Priority: draft.Recovery.Priority}, draft.Wait)
	case "upload":
		p, err = d.native.PrepareUpload(o.native, draft.Source.Name, draft.Source.Size, draft.Source.SHA256, draft.Recovery.Priority, draft.Wait)
	case "download":
		p, err = d.native.PrepareDownload(o.native, SnapshotDescription{Name: selected.Name, CreationTime: selected.CreationTime, Checksum: selected.Checksum, Size: selected.Size, HasCreationTime: selected.HasCreationTime, Raw: bytes.Clone(selected.Raw)})
	case "stream":
		p, err = d.native.PrepareStream(o.native)
	default:
		return nil, ErrInvalid
	}
	if err != nil {
		return nil, err
	}
	return &snapshotAdapterPlan{owner: d, native: p}, nil
}
func (d *snapshotAdapterDocument) result(r SnapshotResult) contract.SnapshotResult {
	receipt := r.Receipt
	out := contract.SnapshotResult{Object: d.object, Action: r.Action, Target: d.target(r.Target), State: r.State, Matched: r.Matched, Count: r.Count, HasCount: r.HasCount, Metadata: d.metadata(r.Metadata), Receipt: contract.SnapshotReceipt{Action: receipt.Action, Target: d.target(receipt.Target), State: receipt.State, Response: snapshotResponse(receipt.Response), HasResult: receipt.HasResult, Result: receipt.Result, HasNullResult: receipt.HasNullResult, SourceBytesRead: receipt.SourceBytesRead, SourceSHA256: receipt.SourceSHA256}}
	if receipt.Description != nil {
		description := snapshotDescription(*receipt.Description)
		out.Receipt.Description = &description
	}
	x := r.Transfer
	out.Transfer = contract.SnapshotTransfer{Target: d.target(x.Target), Name: x.Name, Path: x.Path, Response: snapshotResponse(x.Response), ExpectedSize: x.ExpectedSize, Received: x.Received, Written: x.Written, ExpectedChecksum: x.ExpectedChecksum, SHA256: x.SHA256, ContentLength: x.ContentLength, Prefix: bytes.Clone(x.Prefix), Complete: x.Complete}
	return out
}
func (d *snapshotAdapterDocument) Submit(ctx context.Context, plan contract.SnapshotPlan) (contract.SnapshotResult, error) {
	p, ok := plan.(*snapshotAdapterPlan)
	if !ok || p == nil || p.owner != d {
		return contract.SnapshotResult{Object: d.object, State: "not_submitted"}, ErrSnapshotUsed
	}
	r, err := d.native.Submit(ctx, p.native)
	return d.result(r), err
}
func (d *snapshotAdapterDocument) Upload(ctx context.Context, plan contract.SnapshotPlan, source io.ReadCloser, options contract.SnapshotTransferOptions) (contract.SnapshotResult, error) {
	done := d.stream()
	defer done()
	p, ok := plan.(*snapshotAdapterPlan)
	if !ok || p == nil || p.owner != d {
		if source != nil {
			_ = source.Close()
		}
		return contract.SnapshotResult{Object: d.object, State: "not_submitted"}, ErrSnapshotUsed
	}
	r, err := d.native.Upload(ctx, p.native, source, SnapshotTransferOptions{MaxBytes: options.MaxBytes, Timeout: options.Timeout})
	return d.result(r), err
}
func (d *snapshotAdapterDocument) Download(ctx context.Context, plan contract.SnapshotPlan, sink io.WriteCloser, options contract.SnapshotTransferOptions) (contract.SnapshotResult, error) {
	done := d.stream()
	defer done()
	p, ok := plan.(*snapshotAdapterPlan)
	if !ok || p == nil || p.owner != d {
		if sink != nil {
			_ = sink.Close()
		}
		return contract.SnapshotResult{Object: d.object, State: "not_submitted"}, ErrSnapshotUsed
	}
	r, err := d.native.Download(ctx, p.native, sink, SnapshotTransferOptions{MaxBytes: options.MaxBytes, Timeout: options.Timeout})
	return d.result(r), err
}
func (d *snapshotAdapterDocument) Verify(ctx context.Context) (contract.SnapshotResult, error) {
	r, err := d.native.Verify(ctx)
	return d.result(r), err
}
func (d *snapshotAdapterDocument) Close() error {
	d.closeOnce.Do(func() {
		d.streamMu.Lock()
		d.closed = true
		d.streamMu.Unlock()
		d.closeErr = d.native.Close()
		d.streams.Wait()
	})
	return d.closeErr
}

var _ contract.SnapshotProvider = (*Session)(nil)
var _ contract.SnapshotLease = (*snapshotAdapterDocument)(nil)

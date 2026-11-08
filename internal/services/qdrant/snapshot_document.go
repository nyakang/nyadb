package qdrant

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// SnapshotMetadata contains bounded live native observations, not a remote
// incarnation, collection lock or complete archive/data equivalence proof.
type SnapshotMetadata struct {
	ProfileID   int64
	Target      SnapshotTarget
	Collections []string
	Aliases     []Alias
	Config      json.RawMessage
	Snapshots   []SnapshotDescription
	Reads       []SnapshotReadResponse
}

type SnapshotReadResponse struct {
	Response          Response
	BodyTruncated     bool
	OriginalBodyBytes int
}

func cloneSnapshotMetadata(m SnapshotMetadata) SnapshotMetadata {
	m.Collections, m.Aliases = slices.Clone(m.Collections), slices.Clone(m.Aliases)
	m.Config = bytes.Clone(m.Config)
	m.Snapshots = slices.Clone(m.Snapshots)
	for i := range m.Snapshots {
		m.Snapshots[i] = cloneSnapshotDescription(m.Snapshots[i])
	}
	m.Reads = slices.Clone(m.Reads)
	for i := range m.Reads {
		m.Reads[i].Response.Body = bytes.Clone(m.Reads[i].Response.Body)
	}
	return m
}

type snapshotObservation struct{ metadata SnapshotMetadata }

// The private handle is immutable. Snapshot always returns independent values.
// Foreign, copied-description and superseded observations cannot authorize I/O.
type SessionSnapshotObservation struct {
	owner *SnapshotLease
	value *snapshotObservation
}

func (o SessionSnapshotObservation) Snapshot() SnapshotMetadata {
	if o.value == nil {
		return SnapshotMetadata{}
	}
	return cloneSnapshotMetadata(o.value.metadata)
}

type SessionSnapshotPlan struct {
	owner    *SnapshotLease
	before   *snapshotObservation
	sequence uint64
	native   SnapshotPlan
	selected *SnapshotDescription
	attempt  *atomic.Bool
}

func (p SessionSnapshotPlan) Action() string         { return p.native.Action() }
func (p SessionSnapshotPlan) Target() SnapshotTarget { return p.native.Target() }
func (p SessionSnapshotPlan) Preview() Request       { return p.native.Preview() }
func (p SessionSnapshotPlan) UploadSource() (string, uint64, string, bool) {
	return p.native.UploadSource()
}
func (p SessionSnapshotPlan) RestoresData() bool {
	return p.Action() == "recover" || p.Action() == "upload"
}

type SnapshotResult struct {
	ProfileID int64
	Action    string
	Target    SnapshotTarget
	State     string
	Receipt   SnapshotReceipt
	Transfer  SnapshotTransfer
	Metadata  SnapshotMetadata
	Matched   bool // Only an explicit archive create/delete observation.
	// Recovery count is the physical COLLECTION's exact live count, including
	// for a shard target. It is not a shard count or complete recovery proof.
	Count    uint64
	HasCount bool
}

func cloneSnapshotReceipt(r SnapshotReceipt) SnapshotReceipt {
	r.Response.Body = bytes.Clone(r.Response.Body)
	if r.Description != nil {
		d := cloneSnapshotDescription(*r.Description)
		r.Description = &d
	}
	return r
}
func cloneSnapshotResult(r SnapshotResult) SnapshotResult {
	r.Receipt = cloneSnapshotReceipt(r.Receipt)
	r.Transfer = cloneSnapshotTransfer(r.Transfer)
	r.Metadata = cloneSnapshotMetadata(r.Metadata)
	return r
}

type snapshotPending struct {
	plan    SessionSnapshotPlan
	receipt SnapshotReceipt
	last    SnapshotResult
}

// SnapshotLease retains one original session and immutable observation/attempt
// handles. Public generations are assigned only at the runtime boundary.
type SnapshotLease struct {
	mu, operation sync.Mutex
	session       *Session
	document      uint64
	ctx           context.Context
	cancel        context.CancelFunc
	closed        bool
	done          chan struct{}
	sequence      uint64
	observation   *snapshotObservation
	pending       *snapshotPending
}

func (*SnapshotLease) namespaceClaim() {}

func (s *Session) ReserveSnapshotDocument(document uint64) (*SnapshotLease, error) {
	if document == 0 {
		return nil, ErrInvalid
	}
	l := &SnapshotLease{session: s, document: document, done: make(chan struct{})}
	l.ctx, l.cancel = context.WithCancel(s.ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.revoked {
		l.cancel()
		return nil, contract.ErrRevoked
	}
	if len(s.snapshots) >= 8 {
		l.cancel()
		return nil, ErrBounds
	}
	for m := range s.managers {
		if m.document == document {
			l.cancel()
			return nil, ErrInvalid
		}
	}
	for snapshot := range s.snapshots {
		if snapshot.document == document {
			l.cancel()
			return nil, ErrInvalid
		}
	}
	if s.snapshots == nil {
		s.snapshots = map[*SnapshotLease]bool{}
	}
	s.snapshots[l] = true
	return l, nil
}

func (l *SnapshotLease) begin(ctx context.Context) (context.Context, func(), error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, nil, contract.ErrRevoked
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	child, done, err := l.session.request(ctx)
	if err != nil {
		return nil, nil, err
	}
	child, cancel := context.WithCancel(child)
	stop := context.AfterFunc(l.ctx, cancel)
	return child, func() { stop(); cancel(); done() }, nil
}

func (l *SnapshotLease) read(ctx context.Context, c *Client, target SnapshotTarget) (SnapshotMetadata, error) {
	m := SnapshotMetadata{ProfileID: l.session.ProfileID(), Target: target}
	keep := func(r Response) error { return retainSnapshotRead(&m, r) }
	var r Response
	var err error
	m.Collections, r, err = c.Collections(ctx)
	if e := keep(r); e != nil {
		return m, e
	}
	if err != nil {
		return m, err
	}
	if len(m.Collections) > metadataLimit {
		return m, ErrBounds
	}
	m.Aliases, r, err = c.Aliases(ctx)
	if e := keep(r); e != nil {
		return m, e
	}
	if err != nil {
		return m, err
	}
	if len(m.Aliases) > metadataLimit {
		return m, ErrBounds
	}
	if target.Scope != StorageSnapshots {
		// A missing physical collection can be an explicit collection recovery
		// destination. Aliases are never substituted for physical targets.
		if !slices.Contains(m.Collections, target.Collection) {
			return m, l.session.validateObservation(ctx, c)
		}
		info, response, e := c.Collection(ctx, target.Collection)
		if bound := keep(response); bound != nil {
			return m, bound
		}
		if e != nil {
			return m, e
		}
		fields, e := object(info.Result)
		if e != nil {
			return m, e
		}
		m.Config, e = canonicalJSON(fields["config"])
		if e != nil {
			return m, e
		}
	}
	m.Snapshots, r, err = c.ListSnapshots(ctx, target)
	if e := keep(r); e != nil {
		return m, e
	}
	if err != nil {
		return m, err
	}
	return m, l.session.validateObservation(ctx, c)
}

func retainSnapshotRead(m *SnapshotMetadata, r Response) error {
	retained := 0
	for _, prior := range m.Reads {
		retained += len(prior.Response.Body)
	}
	e := SnapshotReadResponse{Response: r, OriginalBodyBytes: len(r.Body)}
	if len(r.Body) > requestLimit-retained {
		e.BodyTruncated = true
		e.Response.Body = bytes.Clone(r.Body[:requestLimit-retained])
	}
	m.Reads = append(m.Reads, e)
	if e.BodyTruncated {
		return ErrBounds
	}
	return nil
}

func (l *SnapshotLease) Read(ctx context.Context, target SnapshotTarget) (SessionSnapshotObservation, error) {
	if _, err := target.path(); err != nil {
		return SessionSnapshotObservation{}, err
	}
	if !l.operation.TryLock() {
		return SessionSnapshotObservation{}, ErrBusy
	}
	defer l.operation.Unlock()
	ctx, done, err := l.begin(ctx)
	if err != nil {
		return SessionSnapshotObservation{}, err
	}
	defer done()
	if l.pending != nil {
		return SessionSnapshotObservation{}, contract.ErrNamespaceBusy
	}
	l.sequence++
	l.observation = nil
	c, err := l.session.open(ctx)
	if err != nil {
		return SessionSnapshotObservation{}, err
	}
	defer c.Close()
	m, err := l.read(ctx, c, target)
	o := SessionSnapshotObservation{value: &snapshotObservation{metadata: cloneSnapshotMetadata(m)}}
	if err != nil {
		return o, err
	}
	if err = ctx.Err(); err != nil {
		return o, err
	}
	o.owner = l
	l.observation = o.value
	return o, nil
}

func sameSnapshotDescription(a, b SnapshotDescription) bool {
	return a.Name == b.Name && a.Size == b.Size && a.HasCreationTime == b.HasCreationTime && a.CreationTime == b.CreationTime && strings.EqualFold(a.Checksum, b.Checksum)
}

func (l *SnapshotLease) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		<-l.done
		return nil
	}
	l.closed = true
	l.cancel()
	l.mu.Unlock()
	l.operation.Lock()
	l.observation, l.pending = nil, nil
	l.sequence++
	l.session.mu.Lock()
	delete(l.session.snapshots, l)
	if l.session.namespaceOwner == l {
		l.session.namespaceOwner, l.session.claimedCollections = nil, nil
	}
	l.session.mu.Unlock()
	l.operation.Unlock()
	close(l.done)
	return nil
}

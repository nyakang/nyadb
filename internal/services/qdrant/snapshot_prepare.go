package qdrant

import (
	"bytes"
	"slices"
	"sync/atomic"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func (l *SnapshotLease) ready(o SessionSnapshotObservation) error {
	l.mu.Lock()
	closed := l.closed
	l.mu.Unlock()
	if closed || !l.session.Alive() {
		return contract.ErrRevoked
	}
	if l.pending != nil {
		return contract.ErrNamespaceBusy
	}
	if o.owner != l || o.value == nil || o.value != l.observation {
		return contract.ErrNamespaceChanged
	}
	return nil
}

func snapshotPhysical(m SnapshotMetadata) bool {
	if m.Target.Scope == StorageSnapshots {
		return true
	}
	return slices.Contains(m.Collections, m.Target.Collection) && len(m.Config) != 0
}
func snapshotAliasCollision(m SnapshotMetadata) bool {
	for _, a := range m.Aliases {
		if a.Alias == m.Target.Collection {
			return true
		}
	}
	return false
}
func selectedSnapshot(m SnapshotMetadata, selected SnapshotDescription) (SnapshotDescription, error) {
	if err := validateSnapshotDescription(selected); err != nil {
		return SnapshotDescription{}, err
	}
	for _, d := range m.Snapshots {
		if sameSnapshotDescription(d, selected) {
			return cloneSnapshotDescription(d), nil
		}
	}
	return SnapshotDescription{}, ErrSnapshotChanged
}

func (l *SnapshotLease) plan(o SessionSnapshotObservation, native SnapshotPlan, selected *SnapshotDescription) SessionSnapshotPlan {
	l.sequence++
	return SessionSnapshotPlan{owner: l, before: o.value, sequence: l.sequence, native: native, selected: selected, attempt: &atomic.Bool{}}
}

func (l *SnapshotLease) PrepareCreate(o SessionSnapshotObservation, wait bool) (SessionSnapshotPlan, error) {
	if !l.operation.TryLock() {
		return SessionSnapshotPlan{}, ErrBusy
	}
	defer l.operation.Unlock()
	if err := l.ready(o); err != nil {
		return SessionSnapshotPlan{}, err
	}
	if !snapshotPhysical(o.value.metadata) {
		return SessionSnapshotPlan{}, contract.ErrNamespaceChanged
	}
	native, err := PrepareSnapshotCreate(o.value.metadata.Target, wait)
	if err != nil {
		return SessionSnapshotPlan{}, err
	}
	return l.plan(o, native, nil), nil
}
func (l *SnapshotLease) PrepareDelete(o SessionSnapshotObservation, selected SnapshotDescription, wait bool) (SessionSnapshotPlan, error) {
	if !l.operation.TryLock() {
		return SessionSnapshotPlan{}, ErrBusy
	}
	defer l.operation.Unlock()
	if err := l.ready(o); err != nil {
		return SessionSnapshotPlan{}, err
	}
	d, err := selectedSnapshot(o.value.metadata, selected)
	if err != nil {
		return SessionSnapshotPlan{}, err
	}
	native, err := PrepareSnapshotDelete(o.value.metadata.Target, d.Name, wait)
	if err != nil {
		return SessionSnapshotPlan{}, err
	}
	return l.plan(o, native, &d), nil
}
func (l *SnapshotLease) PrepareRecovery(o SessionSnapshotObservation, source SnapshotRecovery, wait bool) (SessionSnapshotPlan, error) {
	if !l.operation.TryLock() {
		return SessionSnapshotPlan{}, ErrBusy
	}
	defer l.operation.Unlock()
	if err := l.ready(o); err != nil {
		return SessionSnapshotPlan{}, err
	}
	m := o.value.metadata
	if snapshotAliasCollision(m) {
		return SessionSnapshotPlan{}, contract.ErrNamespaceExists
	}
	if m.Target.Scope == ShardSnapshots && !snapshotPhysical(m) {
		return SessionSnapshotPlan{}, contract.ErrNamespaceChanged
	}
	native, err := PrepareSnapshotRecovery(m.Target, source, wait)
	if err != nil {
		return SessionSnapshotPlan{}, err
	}
	return l.plan(o, native, nil), nil
}
func (l *SnapshotLease) PrepareUpload(o SessionSnapshotObservation, name string, size uint64, checksum, priority string, wait bool) (SessionSnapshotPlan, error) {
	if !l.operation.TryLock() {
		return SessionSnapshotPlan{}, ErrBusy
	}
	defer l.operation.Unlock()
	if err := l.ready(o); err != nil {
		return SessionSnapshotPlan{}, err
	}
	m := o.value.metadata
	if snapshotAliasCollision(m) {
		return SessionSnapshotPlan{}, contract.ErrNamespaceExists
	}
	if m.Target.Scope == ShardSnapshots && !snapshotPhysical(m) {
		return SessionSnapshotPlan{}, contract.ErrNamespaceChanged
	}
	native, err := PrepareSnapshotUpload(m.Target, name, size, checksum, priority, wait)
	if err != nil {
		return SessionSnapshotPlan{}, err
	}
	return l.plan(o, native, nil), nil
}
func (l *SnapshotLease) PrepareDownload(o SessionSnapshotObservation, selected SnapshotDescription) (SessionSnapshotPlan, error) {
	if !l.operation.TryLock() {
		return SessionSnapshotPlan{}, ErrBusy
	}
	defer l.operation.Unlock()
	if err := l.ready(o); err != nil {
		return SessionSnapshotPlan{}, err
	}
	d, err := selectedSnapshot(o.value.metadata, selected)
	if err != nil {
		return SessionSnapshotPlan{}, err
	}
	path, _ := o.value.metadata.Target.path()
	name, _ := snapshotName(d.Name)
	r := Request{Method: "GET", Path: path + "/" + name}
	native := SnapshotPlan{action: "download", target: o.value.metadata.Target, preview: r, request: r}
	return l.plan(o, native, &d), nil
}
func (l *SnapshotLease) PrepareStream(o SessionSnapshotObservation) (SessionSnapshotPlan, error) {
	if !l.operation.TryLock() {
		return SessionSnapshotPlan{}, ErrBusy
	}
	defer l.operation.Unlock()
	if err := l.ready(o); err != nil {
		return SessionSnapshotPlan{}, err
	}
	m := o.value.metadata
	if m.Target.Scope != ShardSnapshots || !snapshotPhysical(m) {
		return SessionSnapshotPlan{}, ErrInvalid
	}
	path, _ := m.Target.path()
	r := Request{Method: "GET", Path: path[:len(path)-len("snapshots")] + "snapshot"}
	native := SnapshotPlan{action: "stream", target: m.Target, preview: r, request: r}
	return l.plan(o, native, nil), nil
}

func snapshotPreflightMatches(p SessionSnapshotPlan, after SnapshotMetadata) bool {
	before := p.before.metadata
	if before.ProfileID != after.ProfileID || before.Target != after.Target {
		return false
	}
	if p.Target().Scope == StorageSnapshots {
		if !slices.Equal(before.Collections, after.Collections) || !slices.Equal(before.Aliases, after.Aliases) {
			return false
		}
	} else {
		if snapshotPhysical(before) != snapshotPhysical(after) || !bytes.Equal(before.Config, after.Config) || snapshotAliasCollision(after) {
			return false
		}
	}
	if p.selected != nil {
		for _, d := range after.Snapshots {
			if sameSnapshotDescription(*p.selected, d) {
				return true
			}
		}
		return false
	}
	return true
}

func (l *SnapshotLease) claim(p SessionSnapshotPlan) error {
	s := l.session
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.revoked {
		return contract.ErrRevoked
	}
	if s.namespaceOwner != nil && s.namespaceOwner != l {
		return contract.ErrNamespaceBusy
	}
	// Arbitrary REST documents can mutate any archive, collection or alias.
	if len(s.leases) != 0 {
		return ErrManagementInUse
	}
	if p.RestoresData() {
		for d := range s.documents {
			if d.object.Catalog == p.Target().Collection {
				return ErrManagementInUse
			}
		}
	}
	s.namespaceOwner = l
	s.claimedCollections = map[string]bool{}
	if p.RestoresData() {
		s.claimedCollections[p.Target().Collection] = true
	}
	return nil
}
func (l *SnapshotLease) releaseClaim() {
	s := l.session
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.namespaceOwner == l {
		s.namespaceOwner, s.claimedCollections = nil, nil
	}
}

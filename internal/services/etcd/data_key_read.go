package etcd

import (
	"bytes"
	"context"
	"errors"
	"slices"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func (l *dataLease) keyReadObservation(r contract.KVKeyReadRequest) (dataObservation, dataBoundary, error) {
	var boundary dataBoundary
	observation, err := l.validateSnapshot(r.Snapshot)
	if err != nil {
		return observation, boundary, err
	}
	c := r.Cursor
	if c == nil || c.Database != l.object.Catalog || c.Table != l.object.Name || !slices.Equal(c.Columns, []string{"etcd_mvcc"}) || len(c.Values) != 1 || len(c.Descending) != 0 || c.RuleSignature != "" || c.FilterColumn != "" || c.FilterMode != "" || c.FilterValue != "" || c.SortColumn != "" || c.SortDescending {
		return observation, boundary, ErrInvalid
	}
	if err = l.decode(c.Values[0], &boundary); err != nil {
		return observation, boundary, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.rangeLost || !l.pageStarted || l.revision <= 0 || boundary.Revision != l.revision || boundary.Epoch != l.epoch || observation.ReadEpoch != boundary.Epoch || observation.ReadRevision != boundary.Revision || observation.ModRevision > boundary.Revision {
		return observation, boundary, contract.ErrRevoked
	}
	if boundary.Offset < 0 || boundary.Offset != c.Offset || boundary.Limit < 1 || boundary.Limit > 1000 || !validKey(boundary.Start) || !bytes.HasPrefix(boundary.Start, l.session.prefix) || l.access == nil || boundary.Range < 0 || boundary.Range >= len(l.access.Read) || !l.access.Read[boundary.Range].contains(boundary.Start) || bytes.Compare(observation.Key, boundary.Start) < 0 || !allowsKey(l.access.Read, observation.Key) {
		return observation, boundary, ErrInvalid
	}
	return observation, boundary, nil
}
func (l *dataLease) captureKeyRead(parent context.Context, id int64, catalog, name string, input contract.KVKeyReadRequest) (*contract.KVKeyRead, error) {
	if err := l.check(id, catalog, name); err != nil {
		return nil, err
	}
	r := input.Clone()
	if _, _, err := l.keyReadObservation(r); err != nil {
		return nil, err
	}
	ctx, done, err := l.begin(parent)
	if err != nil {
		return nil, err
	}
	defer done()
	// Verify grants through a separate captured physical scope. No latest KV
	// read substitutes a new value/revision for the original row or page.
	a, err := l.scopes[dataKeyScope].readPermissions(ctx, true)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	changed := l.access == nil || a.fingerprint() != l.accessFingerprint
	if changed {
		l.rangeLost = true
	}
	l.mu.Unlock()
	if changed {
		return nil, errors.Join(contract.ErrRevoked, errAccessChanged)
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = l.check(id, catalog, name); err != nil {
		return nil, err
	}
	o, b, err := l.keyReadObservation(r)
	if err != nil {
		return nil, err
	}
	return &contract.KVKeyRead{Scope: l.session.KVScope().Clone(), Key: bytes.Clone(o.Key), Revision: b.Revision}, nil
}

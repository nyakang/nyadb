package dbruntime

import (
	"context"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type transaction struct {
	owner *session
	raw   contract.DataTransaction
}

func (t *transaction) Close() { t.raw.Close() }

func sessionTransactions(s *session, r contract.DataTransactions) contract.DataTransactions {
	out := r
	unwrap := func(t contract.DataTransaction, id int64) (contract.DataTransaction, error) {
		v, ok := t.(*transaction)
		if !ok || v.owner != s {
			return nil, connections.ErrInvalid
		}
		if e := s.require(s.Capabilities().Transactions, id); e != nil {
			return nil, e
		}
		return v.raw, nil
	}
	if r.Begin != nil {
		out.Begin = func(ctx context.Context, id int64, db, table string) (contract.DataTransaction, error) {
			if e := s.require(s.Capabilities().Transactions, id); e != nil {
				return nil, e
			}
			raw, e := r.Begin(ctx, id, db, table)
			if e != nil {
				return nil, e
			}
			return &transaction{owner: s, raw: raw}, nil
		}
	}
	out.Close = func(t contract.DataTransaction) {
		if v, ok := t.(*transaction); ok && v.owner == s {
			v.Close()
		}
	}
	if r.Commit != nil {
		out.Commit = func(ctx context.Context, t contract.DataTransaction) error {
			v, e := unwrap(t, s.id)
			if e != nil {
				return e
			}
			return r.Commit(ctx, v)
		}
	}
	if r.Rollback != nil {
		out.Rollback = func(ctx context.Context, t contract.DataTransaction) error {
			v, e := unwrap(t, s.id)
			if e != nil {
				return e
			}
			return r.Rollback(ctx, v)
		}
	}
	if r.Page != nil {
		out.Page = func(ctx context.Context, t contract.DataTransaction, id int64, db, table string, o contract.DataOptions) (*contract.DataPage, error) {
			v, e := unwrap(t, id)
			if e != nil {
				return nil, e
			}
			return r.Page(ctx, v, id, db, table, o)
		}
	}
	if r.Save != nil {
		out.Save = func(ctx context.Context, t contract.DataTransaction, id int64, db, table string, edits []contract.DataEdit) (*contract.DataEditResult, error) {
			v, e := unwrap(t, id)
			if e != nil {
				return nil, e
			}
			if !s.Capabilities().EditableTables {
				return nil, contract.ErrUnsupported
			}
			return r.Save(ctx, v, id, db, table, edits)
		}
	}
	if r.Row != nil {
		out.Row = func(ctx context.Context, t contract.DataTransaction, id int64, db, table string, ref contract.RowRef) (*contract.DataRow, error) {
			v, e := unwrap(t, id)
			if e != nil {
				return nil, e
			}
			return r.Row(ctx, v, id, db, table, ref)
		}
	}
	if r.Chunk != nil {
		out.Chunk = func(ctx context.Context, t contract.DataTransaction, id int64, db, table, col string, ref contract.RowRef, offset int64, units int) (*contract.FieldChunk, error) {
			v, e := unwrap(t, id)
			if e != nil {
				return nil, e
			}
			if !s.Capabilities().FieldChunks {
				return nil, contract.ErrUnsupported
			}
			return r.Chunk(ctx, v, id, db, table, col, ref, offset, units)
		}
	}
	return out
}

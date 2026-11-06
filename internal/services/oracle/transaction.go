package oracle

import (
	"context"
	"database/sql"
	"errors"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type transaction struct {
	s      *Session
	tx     *sql.Tx
	object contract.ObjectRef
	ctx    context.Context
	cancel context.CancelFunc
	ops    sync.Mutex
	closed bool
}

func (s *Session) DataTransactions() contract.DataTransactions {
	return contract.DataTransactions{
		Begin: s.begin, Commit: s.commit, Rollback: s.rollback, Close: func(t contract.DataTransaction) { t.Close() },
		Page: func(ctx context.Context, t contract.DataTransaction, id int64, schema, name string, o contract.DataOptions) (*contract.DataPage, error) {
			v, err := s.boundTx(t, id, schema, name)
			if err != nil {
				return nil, err
			}
			v.ops.Lock()
			defer v.ops.Unlock()
			if v.closed {
				return nil, contract.ErrDataTransactionLost
			}
			ctx, done := v.request(ctx)
			defer done()
			return s.pageWith(ctx, v.tx, id, schema, name, o)
		},
		Save: func(ctx context.Context, t contract.DataTransaction, id int64, schema, name string, e []contract.DataEdit) (*contract.DataEditResult, error) {
			v, err := s.boundTx(t, id, schema, name)
			if err != nil {
				return nil, err
			}
			v.ops.Lock()
			defer v.ops.Unlock()
			if v.closed {
				return nil, contract.ErrDataTransactionLost
			}
			ctx, done := v.request(ctx)
			defer done()
			r, err := s.apply(ctx, v.tx, id, schema, name, e)
			if err != nil {
				_ = v.tx.Rollback()
				v.closed = true
				v.cancel()
				v.unregister()
			}
			return r, err
		},
		Row: func(ctx context.Context, t contract.DataTransaction, id int64, schema, name string, ref contract.RowRef) (*contract.DataRow, error) {
			v, err := s.boundTx(t, id, schema, name)
			if err != nil {
				return nil, err
			}
			v.ops.Lock()
			defer v.ops.Unlock()
			if v.closed {
				return nil, contract.ErrDataTransactionLost
			}
			ctx, done := v.request(ctx)
			defer done()
			meta, err := s.resolve(ctx, v.tx, id, schema, name, ref.Object)
			if err != nil {
				return nil, err
			}
			return s.rowWith(ctx, v.tx, meta, ref, true)
		},
		Chunk: func(ctx context.Context, t contract.DataTransaction, id int64, schema, name, field string, ref contract.RowRef, offset int64, limit int) (*contract.FieldChunk, error) {
			v, err := s.boundTx(t, id, schema, name)
			if err != nil {
				return nil, err
			}
			v.ops.Lock()
			defer v.ops.Unlock()
			if v.closed {
				return nil, contract.ErrDataTransactionLost
			}
			ctx, done := v.request(ctx)
			defer done()
			return s.chunkWith(ctx, v.tx, id, schema, name, field, ref, offset, limit)
		},
	}
}
func (s *Session) boundTx(t contract.DataTransaction, id int64, schema, name string) (*transaction, error) {
	v, ok := t.(*transaction)
	if !ok || v.s != s || v.object != s.Namespace(schema).Object("table", name) || id != s.ProfileID() {
		return nil, connections.ErrInvalid
	}
	if !s.Alive() {
		return nil, contract.ErrRevoked
	}
	return v, nil
}
func (s *Session) begin(ctx context.Context, id int64, schema, name string) (contract.DataTransaction, error) {
	child, done, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	t, err := s.resolve(child, s.db, id, schema, name, contract.ObjectRef{})
	if err != nil {
		return nil, err
	}
	if !t.Editable {
		return nil, contract.ErrDataReadOnly
	}
	// Transaction lifetime belongs to the document, independently of the Begin
	// UI request. Explicit cancellation waits for the driver's acknowledgement.
	txctx, cancel := context.WithCancel(s.ctx)
	stop := context.AfterFunc(ctx, cancel)
	tx, err := s.db.BeginTx(txctx, &sql.TxOptions{Isolation: sql.LevelDefault})
	stop()
	if err != nil {
		cancel()
		return nil, safeError(err)
	}
	if err := child.Err(); err != nil {
		cancel()
		_ = tx.Rollback()
		return nil, err
	}
	v := &transaction{s: s, tx: tx, object: t.Object, ctx: txctx, cancel: cancel}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancel()
		_ = tx.Rollback()
		return nil, contract.ErrRevoked
	}
	s.transactions[v] = true
	s.mu.Unlock()
	return v, nil
}
func (s *Session) commit(ctx context.Context, t contract.DataTransaction) error {
	v, ok := t.(*transaction)
	if !ok || v.s != s {
		return connections.ErrInvalid
	}
	v.ops.Lock()
	defer v.ops.Unlock()
	if v.closed {
		return contract.ErrDataTransactionLost
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, v.cancel)
	err := v.tx.Commit()
	stop()
	v.closed = true
	v.cancel()
	v.unregister()
	if err != nil {
		return errors.Join(contract.ErrDataCommitUncertain, safeError(err))
	}
	return nil
}
func (s *Session) rollback(ctx context.Context, t contract.DataTransaction) error {
	v, ok := t.(*transaction)
	if !ok || v.s != s {
		return connections.ErrInvalid
	}
	v.ops.Lock()
	defer v.ops.Unlock()
	if v.closed {
		return nil
	}
	err := v.tx.Rollback()
	v.closed = true
	v.cancel()
	v.unregister()
	return safeError(err)
}
func (t *transaction) unregister() { t.s.mu.Lock(); delete(t.s.transactions, t); t.s.mu.Unlock() }
func (t *transaction) Close()      { t.cancel(); _ = t.s.rollback(context.Background(), t); t.unregister() }

func (t *transaction) request(ctx context.Context) (context.Context, context.CancelFunc) {
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(t.ctx, cancel)
	return child, func() { stop(); cancel() }
}

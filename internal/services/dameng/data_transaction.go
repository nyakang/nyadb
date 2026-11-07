package dameng

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dameng/dpi"
)

// A token owns one qualified object and original physical worker. Its bounded
// pool is independent of metadata and SQL documents. No operation reconnects.
type dataTransaction struct {
	s         *Session
	object    contract.ObjectRef
	ops, mu   sync.Mutex
	ctx       context.Context
	cancel    context.CancelFunc
	closed    bool
	closeDone chan struct{}
	conn      *sql.Conn
	tx        dpi.ContextTransaction
	pending   []contract.DataEditOutcome
	applied   int
}

func (s *Session) beginDataTransaction(ctx context.Context, id int64, schema, name string) (_ contract.DataTransaction, err error) {
	o, err := s.dataObject(id, schema, name, contract.ObjectRef{})
	if err != nil {
		return nil, err
	}
	ctx, done, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	lifetime, cancel := context.WithCancel(s.ctx)
	t := &dataTransaction{s: s, object: o, ctx: lifetime, cancel: cancel, closeDone: make(chan struct{})}
	bridge := context.AfterFunc(ctx, cancel)
	defer bridge()
	defer func() {
		if err != nil {
			t.Close()
		}
	}()
	t.conn, err = s.dataDB.Conn(ctx)
	if err != nil {
		return nil, err
	}
	err = t.conn.Raw(func(raw any) error {
		begin, ok := raw.(driver.ConnBeginTx)
		if !ok {
			return contract.ErrUnsupported
		}
		v, e := begin.BeginTx(lifetime, driver.TxOptions{})
		if e != nil {
			return e
		}
		t.tx, ok = v.(dpi.ContextTransaction)
		if !ok {
			return errors.Join(contract.ErrUnsupported, v.Rollback())
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, q := range []string{"SET TRANSACTION ISOLATION LEVEL SERIALIZABLE", "LOCK TABLE " + qualified(o) + " IN EXCLUSIVE MODE"} {
		if _, err = t.conn.ExecContext(ctx, q); err != nil {
			return nil, err
		}
	}
	structure, err := structureAt(ctx, t.conn, o)
	if err != nil {
		return nil, err
	}
	if _, err = editableTableAt(ctx, t.conn, structure); err != nil {
		return nil, err
	}
	// Detach before the UI cancels its completed Begin request. A racing caller
	// or session cancellation rejects publication and closes this original handle.
	if !bridge() {
		return nil, context.Canceled
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = lifetime.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, contract.ErrRevoked
	}
	s.dataTransactions[t] = struct{}{}
	return t, nil
}

// retireLocked runs only after the original request acknowledges completion.
// Discard the physical worker even after a known rollback; never pool native
// transaction/session attributes or an uncertain connection.
func (t *dataTransaction) retireLocked() error {
	var err error
	if t.tx != nil {
		err = t.tx.Rollback()
		t.tx = nil
	}
	if t.conn != nil {
		_ = t.conn.Raw(func(any) error { return driver.ErrBadConn })
		if closeErr := t.conn.Close(); !errors.Is(closeErr, sql.ErrConnDone) {
			err = errors.Join(err, closeErr)
		}
		t.conn = nil
	}
	t.pending = nil
	t.cancel()
	t.s.mu.Lock()
	delete(t.s.dataTransactions, t)
	t.s.mu.Unlock()
	return err
}

func (t *dataTransaction) Close() {
	if t == nil {
		return
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		<-t.closeDone
		return
	}
	t.closed = true
	t.cancel()
	t.mu.Unlock()
	t.ops.Lock()
	_ = t.retireLocked()
	t.ops.Unlock()
	close(t.closeDone)
}

func (s *Session) dataToken(token contract.DataTransaction, id int64, schema, name string) (*dataTransaction, error) {
	t, ok := token.(*dataTransaction)
	if !ok || t == nil || t.s != s {
		return nil, connections.ErrInvalid
	}
	o := s.Namespace(schema).Object("table", name)
	if id != s.ProfileID() || o != t.object {
		return nil, connections.ErrInvalid
	}
	if !s.Alive() {
		return nil, contract.ErrDataTransactionLost
	}
	return t, nil
}

// Caller holds ops. Every operation has a current cancellation scope; Begin's
// context is neither used nor kept here. Close cancels before waiting for ops.
func (t *dataTransaction) request(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if t.tx == nil || t.conn == nil || t.ctx.Err() != nil {
		return nil, nil, contract.ErrDataTransactionLost
	}
	child, done, err := t.s.request(ctx)
	if err != nil {
		return nil, nil, errors.Join(contract.ErrDataTransactionLost, err)
	}
	current, cancel := context.WithCancel(child)
	stop := context.AfterFunc(t.ctx, cancel)
	return current, func() { stop(); cancel(); done() }, nil
}

func (t *dataTransaction) settle(err error) error {
	if connectionLost(err) {
		return errors.Join(contract.ErrDataTransactionLost, err, t.retireLocked())
	}
	return err
}

func (t *dataTransaction) read(ctx context.Context, o contract.ObjectRef, read func(context.Context, *sql.Conn, *contract.TableStructure) error) error {
	if o != t.object {
		return connections.ErrInvalid
	}
	structure, err := structureAt(ctx, t.conn, o)
	if err != nil {
		return err
	}
	return read(ctx, t.conn, structure)
}

func (s *Session) pageInDataTransaction(ctx context.Context, token contract.DataTransaction, id int64, schema, name string, options contract.DataOptions) (*contract.DataPage, error) {
	t, err := s.dataToken(token, id, schema, name)
	if err != nil {
		return nil, err
	}
	t.ops.Lock()
	defer t.ops.Unlock()
	ctx, done, err := t.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	page, err := s.loadDataWith(ctx, id, schema, name, options, t.read)
	return page, t.settle(err)
}

func (s *Session) rowInDataTransaction(ctx context.Context, token contract.DataTransaction, id int64, schema, name string, ref contract.RowRef) (*contract.DataRow, error) {
	t, err := s.dataToken(token, id, schema, name)
	if err != nil {
		return nil, err
	}
	t.ops.Lock()
	defer t.ops.Unlock()
	ctx, done, err := t.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	row, err := s.readDataRowWith(ctx, id, schema, name, ref, t.read)
	return row, t.settle(err)
}

func (s *Session) chunkInDataTransaction(ctx context.Context, token contract.DataTransaction, id int64, schema, name, field string, ref contract.RowRef, offset int64, units int) (*contract.FieldChunk, error) {
	t, err := s.dataToken(token, id, schema, name)
	if err != nil {
		return nil, err
	}
	t.ops.Lock()
	defer t.ops.Unlock()
	ctx, done, err := t.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	chunk, err := s.chunkDataWith(ctx, id, schema, name, field, ref, offset, units, t.read)
	return chunk, t.settle(err)
}

func (s *Session) saveInDataTransaction(ctx context.Context, token contract.DataTransaction, id int64, schema, name string, input []contract.DataEdit) (*contract.DataEditResult, error) {
	t, err := s.dataToken(token, id, schema, name)
	if err != nil {
		return nil, err
	}
	edits, err := cloneDataEdits(input)
	if err != nil {
		return nil, err
	}
	t.ops.Lock()
	defer t.ops.Unlock()
	ctx, done, err := t.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	if t.applied+len(edits) > 1000 {
		return nil, contract.ErrDataEditLimit
	}
	structure, err := structureAt(ctx, t.conn, t.object)
	if err != nil {
		return nil, t.settle(err)
	}
	key, err := editableTableAt(ctx, t.conn, structure)
	if err != nil {
		return nil, t.settle(err)
	}
	identity, err := tableIdentityAt(ctx, t.conn, t.object)
	if err != nil {
		return nil, t.settle(err)
	}
	// Native DM8 accepts SAVEPOINT and ROLLBACK TO SAVEPOINT, rejects RELEASE.
	// Reusing this fixed name replaces it; no unbounded savepoint history.
	if _, err = t.conn.ExecContext(ctx, "SAVEPOINT NYADB_DATA_BATCH"); err != nil {
		return nil, t.settle(err)
	}
	out := &contract.DataEditResult{}
	mutated := false
	err = s.applyDataEditsAt(ctx, t.conn, structure, key, identity, edits, out, &mutated)
	var pending []contract.DataEditOutcome
	if err == nil {
		pending, err = mergeDataTransactionOutcomes(t.pending, out.Outcomes)
		if err == nil {
			err = t.refreshPending(ctx, structure, key, identity, pending)
		}
	}
	if err != nil {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		_, rollbackErr := t.conn.ExecContext(cleanup, "ROLLBACK TO SAVEPOINT NYADB_DATA_BATCH")
		stop()
		if rollbackErr != nil {
			out.CommitUncertain, out.Incomplete = mutated, true
			out.Saved = append(t.receipt().Saved, out.Saved...)
			cleanupErr := t.retireLocked()
			if mutated {
				err = errors.Join(err, contract.ErrDataCommitUncertain)
			}
			return out, errors.Join(contract.ErrDataTransactionLost, err, rollbackErr, cleanupErr)
		}
		return &contract.DataEditResult{}, t.settle(err)
	}
	t.pending, t.applied = pending, t.applied+out.Applied
	return out, nil
}

func cloneTransactionOutcomes(input []contract.DataEditOutcome) ([]contract.DataEditOutcome, error) {
	if len(input) == 0 {
		return nil, nil
	}
	left, right := make([]contract.DataEdit, len(input)), make([]contract.DataEdit, len(input))
	for i, v := range input {
		left[i].Original, right[i].Original = v.Original, v.Saved
	}
	left, err := cloneDataEdits(left)
	if err != nil {
		return nil, err
	}
	right, err = cloneDataEdits(right)
	if err != nil {
		return nil, err
	}
	out := make([]contract.DataEditOutcome, len(input))
	for i, v := range input {
		out[i] = contract.DataEditOutcome{Kind: v.Kind, Original: left[i].Original, Saved: right[i].Original, GeneratedID: v.GeneratedID}
	}
	return out, nil
}

func mergeDataTransactionOutcomes(previous, batch []contract.DataEditOutcome) ([]contract.DataEditOutcome, error) {
	out := append([]contract.DataEditOutcome(nil), previous...)
	for _, v := range batch {
		found := -1
		if v.Original != nil {
			for i, old := range out {
				if old.Saved != nil && sameDataRef(old.Saved.Ref, v.Original.Ref) {
					found = i
					break
				}
			}
		}
		if found < 0 {
			out = append(out, v)
			continue
		}
		old := out[found]
		if old.Kind == "insert" && v.Kind == "delete" {
			out = append(out[:found], out[found+1:]...)
			continue
		}
		v.Original = old.Original
		if v.Kind != "delete" {
			v.Kind = old.Kind
		}
		out[found] = v
	}
	return cloneTransactionOutcomes(out)
}

// Reread all net pending rows after every batch and immediately before COMMIT.
// Trigger changes, complete LOBs and changed keys use actual original-session
// evidence, with bounded retained snapshots and no guessed commit receipt.
func (t *dataTransaction) refreshPending(ctx context.Context, structure *contract.TableStructure, key []string, identity string, pending []contract.DataEditOutcome) error {
	budget := dataBatchBytes
	for i := range pending {
		v := &pending[i]
		if v.Original != nil {
			if err := chargeDataRow(&budget, v.Original.Original); err != nil {
				return err
			}
		}
		if v.Saved != nil {
			row, err := readFullRowAt(ctx, t.conn, structure, key, v.Saved.Ref)
			if err != nil {
				return err
			}
			if err = chargeDataRow(&budget, row); err != nil {
				return err
			}
			v.Saved = t.s.fullSnapshot(structure, key, row, identity)
		} else if v.Kind == "delete" {
			_, present, err := optionalDataRow(ctx, t.conn, structure, key, v.Original.Ref)
			if err != nil {
				return err
			}
			if present {
				return contract.ErrDataConflict
			}
		}
	}
	return ctx.Err()
}

func (t *dataTransaction) receipt() contract.CommitResult {
	r := contract.CommitResult{Applied: t.applied}
	for _, v := range t.pending {
		r.Saved = append(r.Saved, savedEdit(contract.DataEdit{Kind: v.Kind, Original: v.Original}, v.Saved))
	}
	return r
}

func (t *dataTransaction) commitPreparationError(err error) error {
	if !connectionLost(err) {
		return err
	}
	receipt := t.receipt()
	receipt.Uncertain, receipt.Incomplete = true, true
	return &contract.CommitError{Result: receipt, Err: errors.Join(contract.ErrDataCommitUncertain, contract.ErrDataTransactionLost, err, t.retireLocked())}
}

func (s *Session) commitDataTransaction(ctx context.Context, token contract.DataTransaction) error {
	t, ok := token.(*dataTransaction)
	if !ok || t == nil || t.s != s {
		return connections.ErrInvalid
	}
	t.ops.Lock()
	defer t.ops.Unlock()
	ctx, done, err := t.request(ctx)
	if err != nil {
		return err
	}
	defer done()
	if err = ctx.Err(); err != nil {
		return err
	}
	structure, err := structureAt(ctx, t.conn, t.object)
	if err != nil {
		return t.commitPreparationError(err)
	}
	key, err := editableTableAt(ctx, t.conn, structure)
	if err != nil {
		return t.commitPreparationError(err)
	}
	identity, err := tableIdentityAt(ctx, t.conn, t.object)
	if err != nil {
		return t.commitPreparationError(err)
	}
	if err = t.refreshPending(ctx, structure, key, identity, t.pending); err != nil {
		return t.commitPreparationError(err)
	}
	receipt := t.receipt()
	// Once submitted, any missing native COMMIT/restoration acknowledgement is
	// uncertain. Retain complete net pending evidence; permanently retire token.
	err = t.tx.CommitContext(ctx)
	if err != nil {
		if !errors.Is(err, dpi.ErrOutcomeUnknown) && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			return err
		}
		receipt.Uncertain = true
		cleanupErr := t.retireLocked()
		return &contract.CommitError{Result: receipt, Err: errors.Join(contract.ErrDataCommitUncertain, err, cleanupErr)}
	}
	t.tx = nil
	return t.retireLocked()
}

func (s *Session) rollbackDataTransaction(ctx context.Context, token contract.DataTransaction) error {
	t, ok := token.(*dataTransaction)
	if !ok || t == nil || t.s != s {
		return connections.ErrInvalid
	}
	t.ops.Lock()
	defer t.ops.Unlock()
	if t.tx == nil {
		return nil
	}
	ctx, done, err := t.request(ctx)
	if err != nil {
		return err
	}
	defer done()
	if err = ctx.Err(); err != nil {
		return err
	}
	err = t.tx.RollbackContext(ctx)
	if err == nil {
		t.tx = nil
	}
	return errors.Join(err, t.retireLocked())
}

func (s *Session) DataTransactions() contract.DataTransactions {
	return contract.DataTransactions{Begin: s.beginDataTransaction, Commit: s.commitDataTransaction, Rollback: s.rollbackDataTransaction, Close: func(token contract.DataTransaction) {
		if t, ok := token.(*dataTransaction); ok && t != nil && t.s == s {
			t.Close()
		}
	}, Page: s.pageInDataTransaction, Save: s.saveInDataTransaction, Row: s.rowInDataTransaction, Chunk: s.chunkInDataTransaction}
}

var _ contract.DataTransactionsProvider = (*Session)(nil)

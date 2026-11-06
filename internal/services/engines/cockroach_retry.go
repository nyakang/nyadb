package engines

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func cockroachRetryable(err error) bool {
	if errors.Is(err, ErrUncertain) || errors.Is(err, contract.ErrDataCommitUncertain) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var pg *pgconn.PgError
	return errors.Is(err, contract.ErrTransactionRetry) || errors.As(err, &pg) && pg.Code == "40001"
}
func cockroachRetryError(err error) error {
	return fmt.Errorf("%w: %w", contract.ErrTransactionRetry, err)
}

// A serialization error aborts the entire CockroachDB transaction. Recovering
// an ordinary batch savepoint cannot preserve earlier reads or staged writes.
// This helper is called only while the owning transaction mutex is held.
func (t *Tx) abortCockroachRetry(err error) error {
	rollback := t.tx.Rollback()
	t.closed = true
	if t.cancel != nil {
		t.cancel()
	}
	t.remove()
	if rollback != nil && !errors.Is(rollback, sql.ErrTxDone) {
		t.uncertain = true
		return errors.Join(ErrUncertain, err, rollback)
	}
	t.applied = 0
	t.staged = make(map[contract.ObjectRef][]contract.Edit)
	return cockroachRetryError(err)
}
func ownCockroachEdits(edits []contract.DataEdit) []contract.DataEdit {
	out := make([]contract.DataEdit, len(edits))
	cloneCells := func(in []contract.Cell) []contract.Cell {
		r := make([]contract.Cell, len(in))
		for i, c := range in {
			r[i] = c.Clone()
		}
		return r
	}
	for i, e := range edits {
		r := e
		r.Values = make(map[string]contract.DataValue, len(e.Values))
		for k, v := range e.Values {
			v.Bytes = bytes.Clone(v.Bytes)
			r.Values[k] = v
		}
		if e.Original != nil {
			x := *e.Original
			x.Columns = append([]string(nil), x.Columns...)
			x.Original = cloneCells(x.Original)
			x.Ref.Columns = append([]string(nil), x.Ref.Columns...)
			x.Ref.TypedValues = cloneCells(x.Ref.TypedValues)
			x.Ref.Values = make([][]byte, len(e.Original.Ref.Values))
			for j, v := range e.Original.Ref.Values {
				x.Ref.Values[j] = bytes.Clone(v)
			}
			x.Fingerprints = make([]*string, len(e.Original.Fingerprints))
			for j, p := range e.Original.Fingerprints {
				if p != nil {
					v := *p
					x.Fingerprints[j] = &v
				}
			}
			r.Original = &x
		}
		out[i] = r
	}
	return out
}
func retryCockroachSave(ctx context.Context, attempt func() (*contract.DataEditResult, error)) (*contract.DataEditResult, error) {
	for i := 0; ; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		out, err := attempt()
		if !cockroachRetryable(err) || i >= 4 || out != nil && (out.CommitConfirmed || out.CommitUncertain || out.Incomplete) {
			return out, err
		}
		// Every preceding attempt has returned after its transaction is closed.
		// Only a server-acknowledged 40001 can reach this bounded retry loop.
		timer := time.NewTimer(time.Duration(25<<i) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
func (s *Session) saveData(ctx context.Context, id int64, schema, name string, edits []contract.DataEdit, t *dataTx) (*contract.DataEditResult, error) {
	if s.kind != connections.CockroachDB || t != nil {
		return s.saveDataOnce(ctx, id, schema, name, edits, t)
	}
	owned := ownCockroachEdits(edits)
	return retryCockroachSave(ctx, func() (*contract.DataEditResult, error) { return s.saveDataOnce(ctx, id, schema, name, owned, nil) })
}

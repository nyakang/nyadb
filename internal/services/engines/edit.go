package engines

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type Tx struct {
	s                 *Session
	tx                *sql.Tx
	cancel            context.CancelFunc
	mu                sync.Mutex
	closed, uncertain bool
	applied           int
	staged            map[contract.ObjectRef][]contract.Edit
}

func (s *Session) Begin(ctx context.Context) (contract.Transaction, error) {
	if s.isClosed() {
		return nil, ErrRevoked
	}
	txctx, cancel := context.WithCancel(s.ctx)
	stop := context.AfterFunc(ctx, cancel)
	t, e := s.db.BeginTx(txctx, nil)
	stopped := stop()
	if !stopped || ctx.Err() != nil {
		if t != nil {
			t.Rollback()
		}
		cancel()
		return nil, ctx.Err()
	}
	if e != nil {
		cancel()
		return nil, e
	}
	x := &Tx{s: s, tx: t, cancel: cancel, staged: make(map[contract.ObjectRef][]contract.Edit)}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		cancel()
		t.Rollback()
		return nil, ErrRevoked
	}
	s.transactions[x] = true
	return x, nil
}
func (t *Tx) Page(ctx context.Context, r contract.PageRequest) (contract.Page, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return contract.Page{}, ErrRevoked
	}
	return t.s.page(ctx, t.tx, r)
}
func (t *Tx) Apply(ctx context.Context, o contract.ObjectRef, edits []contract.Edit) (contract.CommitResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return contract.CommitResult{Applied: t.applied, Uncertain: t.uncertain}, ErrRevoked
	}
	meta, e := t.s.structure(ctx, t.tx, o)
	if e != nil {
		return contract.CommitResult{}, e
	}
	if !meta.Editable {
		return contract.CommitResult{}, errors.New("object has no reliable nonnullable unique key")
	}
	cols := validColumns(meta)
	var savedNow []contract.Edit
	for _, edit := range edits {
		if e = ctx.Err(); e != nil {
			return contract.CommitResult{Applied: t.applied}, e
		}
		var args []any
		var values []string
		names := make([]string, 0, len(edit.Values))
		for n, v := range edit.Values {
			f, ok := cols[n]
			if !ok || f.Generated || v.Truncated || len(v.Text) > 1<<20 || len(v.Bytes) > 1<<20 {
				return contract.CommitResult{Applied: t.applied}, errors.New("unknown/generated/truncated field cannot be edited")
			}
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			args = append(args, parameter(edit.Values[n]))
			values = append(values, quote(n)+"="+t.s.arg(len(args)))
		}
		sqlText := ""
		switch edit.Kind {
		case "insert":
			if len(names) == 0 {
				sqlText = "INSERT INTO " + qualified(o) + " DEFAULT VALUES"
			} else {
				quoted := make([]string, len(names))
				places := make([]string, len(names))
				for i, n := range names {
					quoted[i] = quote(n)
					places[i] = t.s.arg(i + 1)
				}
				sqlText = "INSERT INTO " + qualified(o) + " (" + strings.Join(quoted, ",") + ") VALUES (" + strings.Join(places, ",") + ")"
			}
		case "update", "delete":
			if len(edit.Identity.Columns) != len(meta.Key) || len(edit.Identity.Values) != len(meta.Key) || len(edit.Original) != len(meta.Columns) {
				return contract.CommitResult{Applied: t.applied}, errors.New("complete original row and key required")
			}
			for i, k := range meta.Key {
				keyOriginal := -1
				for ci, f := range meta.Columns {
					if f.Name == k {
						keyOriginal = ci
					}
				}
				if keyOriginal < 0 || !sameCell(edit.Original[keyOriginal], edit.Identity.Values[i]) || edit.Identity.Columns[i] != k || edit.Identity.Values[i].Kind == contract.Null {
					return contract.CommitResult{Applied: t.applied}, errors.New("invalid row identity")
				}
			}
			var where []string
			for i, f := range meta.Columns {
				v := edit.Original[i]
				if v.Truncated {
					return contract.CommitResult{Applied: t.applied}, errors.New("truncated original row is read-only")
				}
				args = append(args, parameter(v))
				op := " IS "
				if t.s.kind == connections.PostgreSQL {
					op = " IS NOT DISTINCT FROM "
				}
				left, right := quote(f.Name), t.s.arg(len(args))
				if t.s.kind == connections.PostgreSQL && strings.EqualFold(f.DatabaseType, "json") {
					left += "::text"
					right += "::text"
				}
				where = append(where, left+op+right)
			}
			if edit.Kind == "delete" {
				sqlText = "DELETE FROM " + qualified(o)
			} else {
				if len(values) == 0 {
					continue
				}
				sqlText = "UPDATE " + qualified(o) + " SET " + strings.Join(values, ",")
			}
			sqlText += " WHERE " + strings.Join(where, " AND ")
		default:
			return contract.CommitResult{Applied: t.applied}, errors.New("unsupported edit kind")
		}
		var affected int64
		verificationEdit := copyEdit(edit)
		if edit.Kind == "delete" {
			r, err := t.tx.ExecContext(ctx, sqlText, args...)
			if err != nil {
				return contract.CommitResult{Applied: t.applied}, err
			}
			affected, err = r.RowsAffected()
			if err != nil {
				return contract.CommitResult{Applied: t.applied}, err
			}
		} else {
			sqlText += " RETURNING "
			for i, f := range meta.Columns {
				if i > 0 {
					sqlText += ","
				}
				sqlText += quote(f.Name)
			}
			rows, err := t.tx.QueryContext(ctx, sqlText, args...)
			if err != nil {
				return contract.CommitResult{Applied: t.applied}, err
			}
			budget := 16 << 20
			returned, err := scan(rows, t.s.kind, 0, nil, &budget, 2, 1<<20)
			rows.Close()
			if err != nil {
				return contract.CommitResult{Applied: t.applied}, err
			}
			affected = int64(len(returned.Rows))
			if affected == 1 {
				verificationEdit.Identity.Columns = append([]string(nil), meta.Key...)
				verificationEdit.Identity.Values = nil
				for _, key := range meta.Key {
					for i, f := range meta.Columns {
						if f.Name == key {
							verificationEdit.Identity.Values = append(verificationEdit.Identity.Values, returned.Rows[0][i].Clone())
						}
					}
				}
			}
		}
		if affected != 1 {
			return contract.CommitResult{Applied: t.applied}, ErrConflict
		}
		t.stage(o, edit.Identity, verificationEdit)
		savedNow = append(savedNow, copyEdit(verificationEdit))
		t.applied++
	}
	return contract.CommitResult{Applied: t.applied, Saved: savedNow}, nil
}
func (t *Tx) remove() { t.s.mu.Lock(); delete(t.s.transactions, t); t.s.mu.Unlock() }
func (t *Tx) Commit(ctx context.Context) (contract.CommitResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return contract.CommitResult{Applied: t.applied, Uncertain: t.uncertain}, ErrRevoked
	}
	if e := ctx.Err(); e != nil {
		return contract.CommitResult{Applied: t.applied}, e
	}
	stop := context.AfterFunc(ctx, func() { t.tx.Rollback() })
	defer stop()
	e := t.tx.Commit()
	t.closed = true
	if t.cancel != nil {
		t.cancel()
	}
	t.remove()
	if e != nil {
		t.uncertain = true
		saved := t.saved()
		return contract.CommitResult{Applied: t.applied, Uncertain: true, Saved: saved}, fmt.Errorf("%w: %v", ErrUncertain, e)
	}
	result := contract.CommitResult{Applied: t.applied, Verified: true}
	for o, edits := range t.staged {
		result.Saved = append(result.Saved, edits...)
		v, err := t.s.Verify(ctx, o, edits)
		if err != nil || !v.Matched {
			result.Verified = false
			result.Incomplete = true
			if err == nil {
				err = errors.New("committed but write verification did not match")
			}
			return result, err
		}
	}
	return result, nil
}
func (t *Tx) Rollback(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	e := t.tx.Rollback()
	t.closed = true
	if t.cancel != nil {
		t.cancel()
	}
	t.remove()
	return e
}
func (t *Tx) Close() { _ = t.Rollback(context.Background()) }
func parameter(c contract.Cell) any {
	switch c.Kind {
	case contract.Null:
		return nil
	case contract.Binary:
		return append([]byte(nil), c.Bytes...)
	case contract.Integer:
		n, e := strconv.ParseInt(c.Text, 10, 64)
		if e == nil {
			return n
		}
	case contract.Floating:
		n, e := strconv.ParseFloat(c.Text, 64)
		if e == nil {
			return n
		}
	case contract.Boolean:
		n, e := strconv.ParseBool(c.Text)
		if e == nil {
			return n
		}
	}
	return c.Text
}

func copyEdit(e contract.Edit) contract.Edit {
	e.Identity.Columns = append([]string(nil), e.Identity.Columns...)
	e.Identity.Values = append([]contract.Cell(nil), e.Identity.Values...)
	for i := range e.Identity.Values {
		e.Identity.Values[i] = e.Identity.Values[i].Clone()
	}
	e.Original = append([]contract.Cell(nil), e.Original...)
	for i := range e.Original {
		e.Original[i] = e.Original[i].Clone()
	}
	values := make(map[string]contract.Cell)
	for k, v := range e.Values {
		values[k] = v.Clone()
	}
	e.Values = values
	return e
}

func (t *Tx) saved() []contract.Edit {
	var saved []contract.Edit
	for _, edits := range t.staged {
		for _, e := range edits {
			saved = append(saved, copyEdit(e))
		}
	}
	return saved
}
func identityEqual(a, b contract.RowIdentity) bool {
	if len(a.Columns) == 0 || len(a.Columns) != len(b.Columns) || len(a.Values) != len(b.Values) {
		return false
	}
	for i := range a.Columns {
		if a.Columns[i] != b.Columns[i] || !sameCell(a.Values[i], b.Values[i]) {
			return false
		}
	}
	return true
}
func (t *Tx) stage(o contract.ObjectRef, before contract.RowIdentity, edit contract.Edit) {
	for i, old := range t.staged[o] {
		if identityEqual(old.Identity, before) || identityEqual(old.Identity, edit.Identity) {
			if edit.Kind == "update" {
				values := make(map[string]contract.Cell)
				for k, v := range old.Values {
					values[k] = v.Clone()
				}
				for k, v := range edit.Values {
					values[k] = v.Clone()
				}
				edit.Values = values
				if old.Kind == "insert" {
					edit.Kind = "insert"
				}
			}
			t.staged[o][i] = copyEdit(edit)
			return
		}
	}
	t.staged[o] = append(t.staged[o], copyEdit(edit))
}

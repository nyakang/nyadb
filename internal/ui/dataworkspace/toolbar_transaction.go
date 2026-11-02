package dataworkspace

import (
	"context"
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
	"reflect"
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/locales"
)

// DataTransactions is injected so documents own transaction lifetime without
// depending on the session browser or the database driver's connection pool.
type DataTransactions = contract.DataTransactions

type dataTransactionResult struct {
	generation, request uint64
	profile             int64
	operation           string
	token               contract.DataTransaction
	release             func() bool
	err                 error
}

type dataTransactionState struct {
	token                 contract.DataTransaction
	runtime               DataTransactions
	generation, request   uint64
	busy, uncertain, lost bool
	commitAfterRead       bool
	cancel                context.CancelFunc
	results               <-chan dataTransactionResult
	pending               *contract.DataEditResult
	keys                  []string
	drafts                map[string]*mysqlRowDraft
	leaveOpen             bool
	leaveNext             func(ui.Services)
	after                 func(ui.Services)
	err                   string
}

func dataTransactionActive(t *Document) bool {
	return t != nil && (t.transaction.token != nil || t.transaction.uncertain || t.transaction.lost)
}
func dataTransactionBusy(t *Document) bool { return t != nil && t.transaction.busy }
func dataTransactionUnavailable(t *Document) bool {
	return t != nil && (t.transaction.busy || t.transaction.uncertain || t.transaction.lost)
}
func (s *State) dataTransactions() DataTransactions {
	if s.Transactions.Begin != nil {
		return s.Transactions
	}
	return s.runtime().Transactions
}

func (s *State) beginDataTransaction(services ui.Services, t *Document) {
	if t == nil || t.closed || t.transfer.Busy || t.transfer.Uncertain || dataTransactionActive(t) || dataTransactionBusy(t) || t.edit.Busy || t.loading || t.edit.Uncertain != nil || !s.canUseSession(t) {
		return
	}
	s.finishMySQLCellEdit(t, true)
	if mysqlDataHasDraft(t) {
		t.transaction.err = locales.Text("开始事务前请应用或放弃待处理记录")
		return
	}
	runtime := s.dataTransactions()
	if s.Transactions.Begin == nil {
		runtime = s.runtimeFor(t).Transactions
	}
	if runtime.Begin == nil || runtime.Commit == nil || runtime.Rollback == nil || runtime.Close == nil || runtime.Save == nil || runtime.Page == nil || runtime.Row == nil {
		t.transaction.err = locales.Text("数据事务服务不可用")
		return
	}
	t.transaction.runtime = runtime
	s.startDataTransactionRequest(services, t, "begin")
}

func (s *State) commitDataTransaction(services ui.Services, t *Document) {
	if t == nil || t.closed || t.transfer.Busy || t.transfer.Uncertain || t.transaction.token == nil || dataTransactionUnavailable(t) || t.edit.Busy || t.edit.Uncertain != nil || !s.canUseSession(t) {
		return
	}
	if t.loading {
		t.transaction.commitAfterRead = true
		return
	}
	t.transaction.commitAfterRead = false
	s.finishMySQLCellEdit(t, true)
	if t.edit.Editor != nil {
		return
	}
	if mysqlDataHasDraft(t) {
		t.edit.AfterSave = func(next ui.Services) { s.commitDataTransaction(next, t) }
		s.saveMySQLData(services, t)
		return
	}
	s.startDataTransactionRequest(services, t, "commit")
}

func (s *State) rollbackDataTransaction(services ui.Services, t *Document) {
	if t == nil || t.closed || t.transfer.Busy || t.transfer.Uncertain || t.transaction.token == nil || dataTransactionUnavailable(t) || t.edit.Busy || t.loading || !s.canUseSession(t) {
		return
	}
	s.startDataTransactionRequest(services, t, "rollback")
}

func (s *State) startDataTransactionRequest(services ui.Services, t *Document, operation string) {
	tx := &t.transaction
	tx.request++
	tx.busy, tx.err = true, ""
	ctx, cancel := context.WithCancel(context.Background())
	tx.cancel = cancel
	out := make(chan dataTransactionResult, 1)
	tx.results = out
	request, generation, profile, database, table := tx.request, s.sessionFor(t).Generation, t.profileID, t.database, t.table
	runtime, token := tx.runtime, tx.token
	complete := s.beginActivity(t, "transaction", operation)
	go func() {
		r := dataTransactionResult{generation: generation, request: request, profile: profile, operation: operation}
		switch operation {
		case "begin":
			r.token, r.err = runtime.Begin(ctx, profile, database, table)
			if r.token != nil {
				owned := r.token
				r.release = context.AfterFunc(ctx, func() { runtime.Close(owned) })
			}
		case "commit":
			r.err = runtime.Commit(ctx, token)
		case "rollback":
			r.err = runtime.Rollback(ctx, token)
		}
		activity := contract.ActivityOutcome(r.err)
		if r.err != nil {
			activity.Error = mysqlDataHistoryError(r.err, false, false, operation == "commit")
		}
		if operation == "commit" && r.err != nil && activity.State != "session_lost" {
			activity.State = "uncertain"
		}
		contract.CompleteActivity(complete, activity)
		out <- r
		services.Invalidate()
	}()
}

// Stop requests cancellation but leaves the operation busy until its executor
// acknowledges it; it must never enable a second commit or automatic replay.
func (s *State) stopDataTransaction(t *Document) {
	if t != nil && t.transaction.cancel != nil {
		t.transaction.cancel()
	}
}

func (s *State) consumeDataTransactions(services ui.Services) {
	for _, t := range s.documents {
		tx := &t.transaction
		if !t.closed && tx.commitAfterRead && !t.loading && t.profileID == s.sessionFor(t).ProfileID {
			tx.commitAfterRead = false
			if t.err == "" {
				s.commitDataTransaction(services, t)
			}
		}
		if t.closed || tx.results == nil || !s.consumeFor(t) {
			continue
		}
		select {
		case r := <-tx.results:
			if r.release != nil && !r.release() {
				r.err = context.Canceled
			}
			if tx.cancel != nil {
				tx.cancel()
			}
			tx.cancel, tx.results, tx.busy = nil, nil, false
			if r.request != tx.request || r.profile != t.profileID || r.generation != s.sessionFor(t).Generation || !s.sessionFor(t).Connected {
				if r.token != nil {
					go tx.runtime.Close(r.token)
				}
				if r.operation == "commit" {
					tx.uncertain = true
					s.retainDataTransactionDrafts(t)
				} else if tx.token != nil {
					tx.lost = true
				}
				tx.after = nil
				tx.err = locales.Text("原数据库会话已失效，请刷新表数据")
				continue
			}
			if r.err != nil {
				tx.err = r.err.Error()
				tx.after = nil
				var receipt *contract.CommitError
				if errors.As(r.err, &receipt) {
					tx.uncertain = true
					s.retainDataTransactionDrafts(t)
					t.edit.Uncertain.Saved = cloneEdits(receipt.Result.Saved)
					t.edit.Uncertain.Incomplete = receipt.Result.Incomplete
				}
				if errors.Is(r.err, contract.ErrDataCommitUncertain) {
					tx.uncertain = true
					s.retainDataTransactionDrafts(t)
				}
				if errors.Is(r.err, contract.ErrDataTransactionLost) {
					tx.lost = true
				}
				continue
			}
			if r.operation == "begin" {
				if r.token == nil {
					tx.err = locales.Text("数据事务服务没有返回事务")
					continue
				}
				tx.token, tx.generation = r.token, r.generation
				s.Load(services, t, true)
				continue
			}
			after := tx.after
			if r.operation == "rollback" {
				t.edit.Uncertain, t.edit.UncertainKeys = nil, nil
				s.discardMySQLData(t)
			}
			t.transaction = dataTransactionState{}
			t.after, t.cursorHistory = nil, nil
			t.useKeyset, t.offset = t.keysetEnabled(), 0
			s.Load(services, t, true)
			if after != nil {
				after(services)
			}
		default:
		}
	}
}

func (s *State) retainDataTransactionDrafts(t *Document) {
	if t.edit.Drafts == nil {
		t.edit.Drafts = map[string]*mysqlRowDraft{}
	}
	for i, key := range t.transaction.keys {
		draft := t.transaction.drafts[key]
		if draft == nil {
			continue
		}
		row := -1
		outcome := t.transaction.pending.Outcomes[i]
		for j, ref := range t.page.RowRefs {
			if ref != nil && ((outcome.Saved != nil && reflect.DeepEqual(*ref, outcome.Saved.Ref)) || (draft.Original != nil && reflect.DeepEqual(*ref, draft.Original.Ref))) {
				row = j
				break
			}
		}
		if row < 0 {
			row = len(t.page.Rows)
			preview := clonePreviews(draft.Before)
			if len(preview) != len(t.page.Columns) {
				preview = make([]contract.DataCell, len(t.page.Columns))
			}
			t.page.Rows = append(t.page.Rows, preview)
			var ref *contract.RowRef
			if draft.Original != nil {
				copy := cloneRowRef(draft.Original.Ref)
				ref = &copy
			}
			t.page.RowRefs = append(t.page.RowRefs, ref)
			t.page.RowSnapshots = append(t.page.RowSnapshots, cloneSnapshot(draft.Original))
		}
		draft.Row = row
		if t.edit.Drafts[key] == nil {
			t.edit.Drafts[key] = draft
		}
	}
	t.edit.Uncertain = cloneEditResult(t.transaction.pending)
	if t.edit.Uncertain == nil {
		t.edit.Uncertain = &contract.DataEditResult{}
	}
	t.edit.Uncertain.CommitUncertain = true
	t.edit.UncertainKeys = append([]string(nil), t.transaction.keys...)
	t.edit.Err = locales.Text("提交结果仍不确定，禁止重放；请检查数据库")
}

func (s *State) rememberDataTransactionSave(t *Document, keys []string, result *contract.DataEditResult) {
	if t.transaction.token == nil {
		return
	}
	tx := &t.transaction
	if tx.pending == nil {
		tx.pending = &contract.DataEditResult{}
	}
	if tx.drafts == nil {
		tx.drafts = map[string]*mysqlRowDraft{}
	}
	copied := cloneEditResult(result)
	for i, key := range keys {
		d := t.edit.Drafts[key]
		if d == nil {
			continue
		}
		index := slices.Index(tx.keys, key)
		if index < 0 && copied.Outcomes[i].Original != nil {
			for j, old := range tx.pending.Outcomes {
				if old.Saved != nil && reflect.DeepEqual(old.Saved.Ref, copied.Outcomes[i].Original.Ref) {
					index = j
					break
				}
			}
		}
		retainedKey := key
		draft := *d
		draft.Original, draft.Values, draft.Before = cloneSnapshot(d.Original), cloneValues(d.Values), clonePreviews(d.Before)
		if index < 0 {
			tx.keys = append(tx.keys, key)
			tx.pending.Outcomes = append(tx.pending.Outcomes, copied.Outcomes[i])
			tx.drafts[key] = &draft
		} else {
			retainedKey = tx.keys[index]
			copied.Outcomes[i].Original = tx.pending.Outcomes[index].Original
			tx.pending.Outcomes[index] = copied.Outcomes[i]
			old := tx.drafts[retainedKey]
			for name, value := range draft.Values {
				old.Values[name] = value
			}
			if draft.Kind == "delete" {
				old.Kind = "delete"
			}
		}
	}
	tx.pending.Saved = copied.Saved
}

func (s *State) closeDataTransaction(t *Document) {
	if t == nil {
		return
	}
	tx := &t.transaction
	if tx.cancel != nil {
		tx.cancel()
	}
	if tx.token != nil && tx.runtime.Close != nil {
		token, close := tx.token, tx.runtime.Close
		go close(token)
	}
	t.transaction = dataTransactionState{}
}

func (s *State) saverFor(t *Document) mysqlDataSaver {
	if t != nil && (t.transfer.Busy || t.transfer.Uncertain || dataTransactionUnavailable(t)) {
		return nil
	}
	if t != nil && t.transaction.token != nil {
		token, save := t.transaction.token, t.transaction.runtime.Save
		if save == nil {
			return nil
		}
		return func(ctx context.Context, profile int64, database, table string, edits []contract.DataEdit) (*contract.DataEditResult, error) {
			return save(ctx, token, profile, database, table, edits)
		}
	}
	if s.Saver != nil {
		return s.Saver
	}
	return s.runtimeFor(t).Saver
}
func (s *State) advancedLoaderFor(t *Document) mysqlDataAdvancedLoader {
	if t != nil && (t.transfer.Busy || t.transfer.Uncertain || dataTransactionUnavailable(t)) {
		return nil
	}
	if t != nil && t.transaction.token != nil {
		token, load := t.transaction.token, t.transaction.runtime.Page
		if load == nil {
			return nil
		}
		return func(ctx context.Context, profile int64, database, table string, o contract.DataOptions) (*contract.DataPage, error) {
			return load(ctx, token, profile, database, table, o)
		}
	}
	if s.AdvancedLoader != nil {
		return s.AdvancedLoader
	}
	if s.Loader != nil {
		return nil
	}
	return s.runtimeFor(t).AdvancedLoader
}
func (s *State) rowReaderFor(t *Document) mysqlDataRowReader {
	if t != nil && (t.transfer.Busy || t.transfer.Uncertain || dataTransactionUnavailable(t)) {
		return nil
	}
	if t != nil && t.transaction.token != nil {
		token, read := t.transaction.token, t.transaction.runtime.Row
		if read == nil {
			return nil
		}
		return func(ctx context.Context, profile int64, database, table string, ref contract.RowRef) (*contract.DataRow, error) {
			return read(ctx, token, profile, database, table, ref)
		}
	}
	if s.RowReader != nil {
		return s.RowReader
	}
	return s.runtimeFor(t).RowReader
}

// GuardTransactionLeave is for closing/disconnecting documents. Page, filter
// and tab selection changes can retain their original transaction and token.
func (s *State) GuardTransactionLeave(t *Document, next func(ui.Services)) bool {
	if t == nil || t.closed {
		return false
	}
	if dataTransactionBusy(t) || t.edit.Busy || t.transfer.Busy {
		return true
	}
	if t.transfer.Uncertain {
		t.transfer.LeaveOpen, t.transfer.LeaveNext, t.transfer.Open = true, next, false
		return true
	}
	if !dataTransactionActive(t) {
		return false
	}
	t.transaction.leaveOpen, t.transaction.leaveNext = true, next
	return true
}

func (s *State) dataTransactionModals(c *ui.Context) {
	for _, t := range s.documents {
		tx := &t.transaction
		if t.closed || !tx.leaveOpen {
			continue
		}
		next := tx.leaveNext
		if tx.uncertain || tx.lost {
			choice := ui.AlertDialog(c, &tx.leaveOpen, locales.Text("事务状态不确定"), locales.Text("请先检查数据库。关闭表不会重放更改，也无法撤回可能已经提交的事务。"), locales.Text("关闭表"), locales.Text("取消"))
			if choice == 0 {
				tx.leaveNext = nil
				s.closeDataTransaction(t)
				t.edit.Uncertain, t.edit.Drafts, t.edit.Editor = nil, nil, nil
				if next != nil {
					next(c.Services())
				}
			}
			if choice == 1 {
				tx.leaveNext = nil
			}
			continue
		}
		choice := ui.AlertDialog(c, &tx.leaveOpen, locales.Text("未提交的事务"), locales.Text("提交或回滚此表的事务后再继续？"), locales.Text("提交事务"), locales.Text("回滚事务"), locales.Text("取消"))
		if choice == 2 {
			tx.leaveNext = nil
		}
		if choice == 0 || choice == 1 {
			tx.leaveNext, tx.after = nil, next
			s.activate(t)
			if choice == 0 {
				s.commitDataTransaction(c.Services(), t)
			} else {
				s.rollbackDataTransaction(c.Services(), t)
			}
		}
	}
}

func (s *State) dataTransactionReadFailed(t *Document, err error) {
	if t != nil && !t.closed && errors.Is(err, contract.ErrDataTransactionLost) {
		t.transaction.lost = true
		t.transaction.err = err.Error()
	}
}

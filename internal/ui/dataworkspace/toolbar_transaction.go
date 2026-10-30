package dataworkspace

import (
	"context"
	"errors"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/locales"
)

// DataTransactions is injected so documents own transaction lifetime without
// depending on the session browser or the database driver's connection pool.
type DataTransactions struct {
	Chunk    func(context.Context, *mysqlsvc.MySQLDataTransaction, int64, string, string, string, mysqlsvc.MySQLRowRef, int64, int) (*mysqlsvc.MySQLFieldChunk, error)
	Begin    func(context.Context, int64, string, string) (*mysqlsvc.MySQLDataTransaction, error)
	Commit   func(context.Context, *mysqlsvc.MySQLDataTransaction) error
	Rollback func(context.Context, *mysqlsvc.MySQLDataTransaction) error
	Close    func(*mysqlsvc.MySQLDataTransaction)
	Page     func(context.Context, *mysqlsvc.MySQLDataTransaction, int64, string, string, mysqlsvc.MySQLDataOptions) (*mysqlsvc.MySQLDataPage, error)
	Save     func(context.Context, *mysqlsvc.MySQLDataTransaction, int64, string, string, []mysqlsvc.MySQLDataEdit) (*mysqlsvc.MySQLDataEditResult, error)
	Row      func(context.Context, *mysqlsvc.MySQLDataTransaction, int64, string, string, mysqlsvc.MySQLRowRef) (*mysqlsvc.MySQLDataRow, error)
}

type dataTransactionResult struct {
	generation, request uint64
	profile             int64
	operation           string
	token               *mysqlsvc.MySQLDataTransaction
	release             func() bool
	err                 error
}

type dataTransactionState struct {
	token                 *mysqlsvc.MySQLDataTransaction
	runtime               DataTransactions
	generation, request   uint64
	busy, uncertain, lost bool
	commitAfterRead       bool
	cancel                context.CancelFunc
	results               <-chan dataTransactionResult
	pending               *mysqlsvc.MySQLDataEditResult
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
	if t == nil || t.closed || t.transfer.Busy || t.transfer.Uncertain || dataTransactionActive(t) || dataTransactionBusy(t) || t.edit.Busy || t.loading || t.edit.Uncertain != nil || t.profileID != s.session().ProfileID || !s.session().Connected {
		return
	}
	s.finishMySQLCellEdit(t, true)
	if mysqlDataHasDraft(t) {
		t.transaction.err = locales.Text("开始事务前请应用或放弃待处理记录")
		return
	}
	runtime := s.dataTransactions()
	if runtime.Begin == nil || runtime.Commit == nil || runtime.Rollback == nil || runtime.Close == nil || runtime.Save == nil || runtime.Page == nil || runtime.Row == nil {
		t.transaction.err = locales.Text("数据事务服务不可用")
		return
	}
	t.transaction.runtime = runtime
	s.startDataTransactionRequest(services, t, "begin")
}

func (s *State) commitDataTransaction(services ui.Services, t *Document) {
	if t == nil || t.closed || t.transfer.Busy || t.transfer.Uncertain || t.transaction.token == nil || dataTransactionUnavailable(t) || t.edit.Busy || t.edit.Uncertain != nil || t.profileID != s.session().ProfileID || !s.session().Connected {
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
	if t == nil || t.closed || t.transfer.Busy || t.transfer.Uncertain || t.transaction.token == nil || dataTransactionUnavailable(t) || t.edit.Busy || t.loading || t.profileID != s.session().ProfileID || !s.session().Connected {
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
	request, generation, profile, database, table := tx.request, s.session().Generation, t.profileID, t.database, t.table
	runtime, token := tx.runtime, tx.token
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
		if !t.closed && tx.commitAfterRead && !t.loading && t.profileID == s.session().ProfileID {
			tx.commitAfterRead = false
			if t.err == "" {
				s.commitDataTransaction(services, t)
			}
		}
		if t.closed || tx.results == nil || t.profileID != s.session().ProfileID {
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
			if r.request != tx.request || r.profile != t.profileID || r.generation != s.session().Generation || !s.session().Connected {
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
				tx.err = locales.Text("原 MySQL 会话已失效，请刷新表数据")
				continue
			}
			if r.err != nil {
				tx.err = r.err.Error()
				tx.after = nil
				if errors.Is(r.err, mysqlsvc.ErrMySQLDataCommitUncertain) {
					tx.uncertain = true
					s.retainDataTransactionDrafts(t)
				}
				if errors.Is(r.err, mysqlsvc.ErrMySQLDataTransactionLost) {
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
			t.useKeyset, t.offset = true, 0
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
	for key, draft := range t.transaction.drafts {
		if t.edit.Drafts[key] == nil {
			t.edit.Drafts[key] = draft
		}
	}
	t.edit.Uncertain = cloneEditResult(t.transaction.pending)
	if t.edit.Uncertain == nil {
		t.edit.Uncertain = &mysqlsvc.MySQLDataEditResult{}
	}
	t.edit.Uncertain.CommitUncertain = true
	t.edit.Err = locales.Text("提交结果仍不确定，禁止重放；请检查数据库")
}

func (s *State) rememberDataTransactionSave(t *Document, keys []string, result *mysqlsvc.MySQLDataEditResult) {
	if t.transaction.token == nil {
		return
	}
	if t.transaction.pending == nil {
		t.transaction.pending = &mysqlsvc.MySQLDataEditResult{}
	}
	t.transaction.pending.Outcomes = append(t.transaction.pending.Outcomes, cloneEditResult(result).Outcomes...)
	if t.transaction.drafts == nil {
		t.transaction.drafts = map[string]*mysqlRowDraft{}
	}
	for _, key := range keys {
		d := t.edit.Drafts[key]
		if d == nil {
			continue
		}
		copy := *d
		copy.Original, copy.Values = cloneSnapshot(d.Original), cloneValues(d.Values)
		copy.Before = append([]mysqlsvc.MySQLDataCell(nil), d.Before...)
		if t.transaction.drafts[key] == nil {
			t.transaction.drafts[key] = &copy
		} else {
			for name, value := range copy.Values {
				t.transaction.drafts[key].Values[name] = value
			}
		}
	}
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
		return func(ctx context.Context, profile int64, database, table string, edits []mysqlsvc.MySQLDataEdit) (*mysqlsvc.MySQLDataEditResult, error) {
			return save(ctx, token, profile, database, table, edits)
		}
	}
	return s.saver()
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
		return func(ctx context.Context, profile int64, database, table string, o mysqlsvc.MySQLDataOptions) (*mysqlsvc.MySQLDataPage, error) {
			return load(ctx, token, profile, database, table, o)
		}
	}
	return s.advancedLoader()
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
		return func(ctx context.Context, profile int64, database, table string, ref mysqlsvc.MySQLRowRef) (*mysqlsvc.MySQLDataRow, error) {
			return read(ctx, token, profile, database, table, ref)
		}
	}
	return s.rowReader()
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
	if t != nil && !t.closed && errors.Is(err, mysqlsvc.ErrMySQLDataTransactionLost) {
		t.transaction.lost = true
		t.transaction.err = err.Error()
	}
}

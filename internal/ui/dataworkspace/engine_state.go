package dataworkspace

import (
	"context"
	"fmt"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"io"
	"sync/atomic"
)

// EngineDependencies keeps credentials, drivers, files and shell out of the grid.
type EngineDependencies struct {
	Session     func(int64) (contract.Session, error)
	Active      func(*EngineDocument) bool
	Status      func(string)
	Invalidate  func()
	SavePath    func(string) (string, error)
	WriteStream func(string, func(io.Writer) error) error
}
type EngineState struct {
	layoutRead  func(string) (string, error)
	layoutWrite func(string, string) error
	deps        EngineDependencies
	documents   map[uint64]*EngineDocument
	guardDoc    *EngineDocument
	guardNext   func(ui.Services)
	guardOpen   bool
	guardOp     string
}
type EngineDocument struct {
	layout                                                 engineLayoutState
	StructureView                                          bool
	staged                                                 []contract.Edit
	writeBlocked                                           bool
	ID                                                     uint64
	Object                                                 contract.ObjectRef
	generation                                             uint64
	Page                                                   *contract.Page
	Error                                                  string
	Busy, CancelRequested, Uncertain                       bool
	Offset                                                 int64
	Filters                                                []contract.Filter
	Sorts                                                  []contract.Sort
	grid                                                   ui.ListState
	selection                                              ui.Selection[int]
	selectedRow, selectedCol                               int
	field, text, kind, filterColumn, filterValue, filterOp string
	drafts                                                 map[int]*contract.Edit
	insert                                                 map[string]contract.Cell
	tx                                                     contract.Transaction
	closed                                                 atomic.Bool
	cancel                                                 context.CancelFunc
	results                                                <-chan engineResult
}
type engineResult struct {
	generation   uint64
	op           string
	page         contract.Page
	tx           contract.Transaction
	commit       contract.CommitResult
	edits        []contract.Edit
	verification contract.Verification
	err          error
}

func NewEngineState(deps EngineDependencies) *EngineState {
	return &EngineState{deps: deps, documents: make(map[uint64]*EngineDocument)}
}
func (s *EngineState) Open(id uint64, o contract.ObjectRef) *EngineDocument {
	if old := s.documents[id]; old != nil {
		s.Close(old)
	}
	d := &EngineDocument{ID: id, Object: o, selectedRow: -1, selectedCol: -1, kind: engineKindLabel(contract.Text), drafts: make(map[int]*contract.Edit), insert: make(map[string]contract.Cell), filterOp: "="}
	s.documents[id] = d
	return d
}
func (s *EngineState) Session(d *EngineDocument) (contract.Session, error) {
	if d == nil || d.closed.Load() {
		return nil, fmt.Errorf("document closed")
	}
	return s.deps.Session(d.Object.ProfileID)
}
func (s *EngineState) run(d *EngineDocument, op string, work func(context.Context, contract.Session) engineResult) {
	if d == nil || d.Busy || d.closed.Load() || (d.Uncertain && op != "verify") {
		return
	}
	session, e := s.Session(d)
	if e != nil {
		d.Error = e.Error()
		return
	}
	if op != "verify" && d.generation != 0 && d.generation != session.Generation() {
		d.Error = "连接已改变；请重新打开数据页"
		return
	}
	d.generation = session.Generation()
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	d.Busy = true
	d.CancelRequested = false
	ch := make(chan engineResult, 1)
	d.results = ch
	generation := d.generation
	invalidate := s.deps.Invalidate
	go func() {
		r := work(ctx, session)
		r.generation, r.op = generation, op
		ch <- r
		if invalidate != nil {
			invalidate()
		}
	}()
}
func clonePageRequest(d *EngineDocument) contract.PageRequest {
	r := contract.PageRequest{Object: d.Object, Offset: d.Offset, Limit: 100, Sorts: append([]contract.Sort(nil), d.Sorts...), Filters: append([]contract.Filter(nil), d.Filters...)}
	for i := range r.Filters {
		r.Filters[i].Value = r.Filters[i].Value.Clone()
	}
	return r
}
func (s *EngineState) Load(d *EngineDocument) {
	r := clonePageRequest(d)
	tx := d.tx
	s.run(d, "page", func(ctx context.Context, session contract.Session) engineResult {
		var p contract.Page
		var e error
		if tx != nil {
			p, e = tx.Page(ctx, r)
		} else {
			p, e = session.Page(ctx, r)
		}
		return engineResult{page: p, err: e}
	})
}
func (s *EngineState) Consume(hostServices ...ui.Services) {
	var services ui.Services
	if len(hostServices) > 0 {
		services = hostServices[0]
	}
	for _, d := range s.documents {
		if d.results == nil || s.deps.Active != nil && !s.deps.Active(d) && s.guardDoc != d {
			continue
		}
		select {
		case r := <-d.results:
			d.results = nil
			d.Busy = false
			d.CancelRequested = false
			if d.cancel != nil {
				d.cancel()
				d.cancel = nil
			}
			session, e := s.Session(d)
			if e != nil || session.Generation() != r.generation {
				if r.tx != nil {
					r.tx.Close()
				}
				d.Error = "连接已改变；原请求已失效"
				continue
			}
			if r.commit.Incomplete {
				d.writeBlocked = true
			}
			if r.op == "commit" && (r.commit.Uncertain || r.commit.Incomplete) && len(r.commit.Saved) > 0 {
				d.staged = nil
				for _, e := range r.commit.Saved {
					d.staged = append(d.staged, cloneEdit(e))
				}
			}
			if r.commit.Uncertain {
				d.Uncertain = true
			}
			if r.err != nil && r.commit.Applied > 0 {
				d.writeBlocked = true
			}
			if r.op == "save" && (r.commit.Uncertain || r.commit.Incomplete) {
				saved := r.edits
				if len(r.commit.Saved) > 0 {
					saved = r.commit.Saved
				}
				d.staged = append(d.staged, saved...)
			}
			if r.err != nil {
				d.Error = r.err.Error()
				continue
			}
			d.Error = ""
			switch r.op {
			case "page":
				d.Page = &r.page
			case "begin":
				d.tx = r.tx
			case "save":
				if d.tx != nil {
					saved := r.edits
					if len(r.commit.Saved) > 0 {
						saved = r.commit.Saved
					}
					d.staged = append(d.staged, saved...)
				}
				d.drafts = make(map[int]*contract.Edit)
				d.insert = make(map[string]contract.Cell)
				if s.guardDoc == d && s.guardOp == "commitAfterSave" {
					s.guardOp = "closeAfterCommit"
					s.Commit(d)
				} else if s.guardDoc == d && s.guardOp == "closeAfterSave" {
					s.finishGuard(services)
				} else {
					s.Load(d)
				}
			case "verify":
				if r.verification.Matched {
					d.Uncertain = false
					d.writeBlocked = false
					d.staged = nil
					d.tx = nil
					d.drafts = make(map[int]*contract.Edit)
					d.insert = make(map[string]contract.Cell)
					s.Load(d)
				} else {
					d.Error = "核验未匹配全部写入；保留修改，禁止重放"
				}
			case "commit", "rollback":
				d.tx = nil
				d.staged = nil
				d.writeBlocked = false
				d.drafts = make(map[int]*contract.Edit)
				d.insert = make(map[string]contract.Cell)
				if s.guardDoc == d && (s.guardOp == "closeAfterCommit" || s.guardOp == "closeAfterRollback") {
					s.finishGuard(services)
				} else {
					s.Load(d)
				}
			}
		default:
		}
	}
}
func (s *EngineState) Begin(d *EngineDocument) {
	if d.tx != nil || d.HasDraft() {
		return
	}
	s.run(d, "begin", func(ctx context.Context, session contract.Session) engineResult {
		tx, e := session.Begin(ctx)
		return engineResult{tx: tx, err: e}
	})
}
func cloneEdit(e contract.Edit) contract.Edit {
	e.Identity.Columns = append([]string(nil), e.Identity.Columns...)
	e.Identity.Values = append([]contract.Cell(nil), e.Identity.Values...)
	for i := range e.Identity.Values {
		e.Identity.Values[i] = e.Identity.Values[i].Clone()
	}
	e.Original = append([]contract.Cell(nil), e.Original...)
	for i := range e.Original {
		e.Original[i] = e.Original[i].Clone()
	}
	v := make(map[string]contract.Cell)
	for k, c := range e.Values {
		v[k] = c.Clone()
	}
	e.Values = v
	return e
}
func (s *EngineState) Save(d *EngineDocument) {
	if d.Page == nil || !d.Page.Table.Editable || d.writeBlocked {
		return
	}
	var edits []contract.Edit
	for i := 0; i < len(d.Page.Rows); i++ {
		if e := d.drafts[i]; e != nil {
			edits = append(edits, cloneEdit(*e))
		}
	}
	if len(d.insert) > 0 {
		edits = append(edits, cloneEdit(contract.Edit{Kind: "insert", Values: d.insert}))
	}
	if len(edits) == 0 {
		return
	}
	o := d.Object
	tx := d.tx
	s.run(d, "save", func(ctx context.Context, session contract.Session) engineResult {
		own := tx == nil
		if own {
			var e error
			tx, e = session.Begin(ctx)
			if e != nil {
				return engineResult{err: e}
			}
		}
		r, e := tx.Apply(ctx, o, edits)
		if e != nil {
			if own {
				tx.Close()
				r.Applied = 0
			}
			return engineResult{commit: r, err: e, edits: edits}
		}
		if own {
			r, e = tx.Commit(ctx)
		}
		return engineResult{commit: r, err: e, edits: edits}
	})
}
func (s *EngineState) Commit(d *EngineDocument) {
	if d.tx == nil || d.HasDraft() {
		return
	}
	tx := d.tx
	s.run(d, "commit", func(ctx context.Context, _ contract.Session) engineResult {
		r, e := tx.Commit(ctx)
		return engineResult{commit: r, err: e}
	})
}
func (s *EngineState) Rollback(d *EngineDocument) {
	if d.tx == nil {
		return
	}
	tx := d.tx
	s.run(d, "rollback", func(ctx context.Context, _ contract.Session) engineResult { return engineResult{err: tx.Rollback(ctx)} })
}
func (d *EngineDocument) HasDraft() bool       { return len(d.drafts) > 0 || len(d.insert) > 0 }
func (d *EngineDocument) HasTransaction() bool { return d.tx != nil }
func (s *EngineState) Cancel(d *EngineDocument) {
	if d != nil && d.cancel != nil {
		d.CancelRequested = true
		d.cancel()
	}
}
func (s *EngineState) Close(d *EngineDocument) {
	if d == nil || d.closed.Swap(true) {
		return
	}
	s.flushEngineLayout(d)
	s.Cancel(d)
	if pending := d.results; pending != nil {
		d.results = nil
		go func() {
			r := <-pending
			if r.tx != nil {
				r.tx.Close()
			}
		}()
	}
	if d.tx != nil {
		d.tx.Close()
		d.tx = nil
	}
	if s.guardDoc == d {
		s.guardDoc, s.guardNext, s.guardOp = nil, nil, ""
		s.guardOpen = false
	}
	delete(s.documents, d.ID)
	d.Page = nil
	d.drafts = nil
	d.grid = ui.ListState{}
}
func (s *EngineState) CloseAll() {
	s.guardDoc, s.guardNext = nil, nil
	s.guardOpen = false
	for _, d := range s.documents {
		s.Close(d)
	}
}

func (s *EngineState) SetFiles(pick func(string) (string, error), write func(string, func(io.Writer) error) error) {
	s.deps.SavePath, s.deps.WriteStream = pick, write
}
func (s *EngineState) SetInvalidate(invalidate func()) { s.deps.Invalidate = invalidate }

func (s *EngineState) Verify(d *EngineDocument) {
	edits := make([]contract.Edit, 0, len(d.staged))
	for _, e := range d.staged {
		edits = append(edits, cloneEdit(e))
	}
	if len(edits) == 0 {
		d.Error = "缺少可核验写入；请重新连接后人工核对"
		return
	}
	o := d.Object
	s.run(d, "verify", func(ctx context.Context, session contract.Session) engineResult {
		v, e := session.Verify(ctx, o, edits)
		return engineResult{verification: v, err: e}
	})
}

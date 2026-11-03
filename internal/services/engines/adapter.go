package engines

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func OpenSession(ctx context.Context, p connections.Profile, password string) (contract.Session, error) {
	m := NewManager()
	if e := m.Connect(ctx, p, password); e != nil {
		_ = m.Close()
		return nil, e
	}
	return m.Session(p.ID)
}
func (s *Session) Alive() bool { return !s.isClosed() }
func (s *Session) Namespace(schema string) contract.NamespaceRef {
	return contract.NamespaceRef{ProfileID: s.id, Catalog: s.Catalog(), Schema: schema}
}
func (s *Session) Namespaces(ctx context.Context) ([]contract.Namespace, error) {
	names, e := s.Schemas(ctx)
	if e != nil {
		return nil, e
	}
	out := make([]contract.Namespace, len(names))
	for i, n := range names {
		out[i] = contract.Namespace{Ref: s.Namespace(n), Label: n}
	}
	return out, nil
}
func (s *Session) Browse(ctx context.Context, n contract.NamespaceRef) ([]contract.DBObject, error) {
	if e := s.check(n.Object("table", "_")); e != nil {
		return nil, e
	}
	if s.kind == connections.PostgreSQL {
		return s.postgresBrowse(ctx, n)
	}
	objects, e := s.Objects(ctx, n.Schema)
	if e != nil {
		return nil, e
	}
	out := make([]contract.DBObject, len(objects))
	for i, o := range objects {
		kind := "表"
		if o.Ref.Kind == "view" {
			kind = "视图"
		}
		out[i] = contract.DBObject{Ref: o.Ref, Name: o.Ref.Name, Kind: kind}
	}
	return out, nil
}
func structureModel(t contract.Table) *contract.TableStructure {
	out := &contract.TableStructure{Object: t.Object, Database: t.Object.Schema, Name: t.Object.Name, Kind: "表", Definition: t.Definition, Definitions: append([]contract.Definition(nil), t.Definitions...), Editable: t.Editable}
	if t.Object.Kind == "view" {
		out.Kind = "视图"
	}
	for i, f := range t.Columns {
		key, extra := "", ""
		if f.Primary {
			key = "PRI"
		}
		if f.Generated {
			extra = "GENERATED"
		}
		out.Columns = append(out.Columns, contract.StructureColumn{Name: f.Name, Type: f.DatabaseType, DataType: f.DatabaseType, Nullable: f.Nullable, Default: f.Default, Position: int64(i + 1), Key: key, Extra: extra, Generated: f.Generated, ValueKind: typeKind(f.DatabaseType)})
	}
	return out
}
func (s *Session) TableMetadata(ctx context.Context, o contract.ObjectRef) (*contract.TableStructure, error) {
	ctx, done := s.requestContext(ctx)
	defer done()
	t, e := s.structure(ctx, s.db, o)
	if e != nil {
		return nil, e
	}
	out := structureModel(t)
	if s.kind == connections.SQLite {
		e = s.sqlitePresentationMetadata(ctx, s.db, out)
	} else {
		e = s.postgresPresentationMetadata(ctx, s.db, out)
	}
	if e != nil {
		return nil, e
	}
	return out, nil
}
func typeKind(t string) contract.CellKind {
	t = strings.ToLower(t)
	switch {
	case t == "bytea" || strings.Contains(t, "blob"):
		return contract.Binary
	case strings.Contains(t, "bool"):
		return contract.Boolean
	case strings.Contains(t, "json"):
		return contract.JSON
	case strings.Contains(t, "int"):
		return contract.Integer
	case strings.Contains(t, "numeric") || strings.Contains(t, "decimal"):
		return contract.Decimal
	case strings.Contains(t, "real") || strings.Contains(t, "float") || strings.Contains(t, "double"):
		return contract.Floating
	case strings.Contains(t, "date") || strings.Contains(t, "time"):
		return contract.DateTime
	default:
		return contract.Text
	}
}
func preview(c contract.Cell) contract.DataCell {
	return contract.DataCell{Value: func() *contract.Cell { v := c.Clone(); return &v }(), Text: c.Display(), Null: c.Kind == contract.Null, Truncated: c.Truncated}
}
func dataValue(c contract.Cell) contract.DataValue {
	return contract.DataValue{Kind: c.Kind, Text: c.Text, Bytes: bytes.Clone(c.Bytes), Null: c.Kind == contract.Null, Binary: c.Kind == contract.Binary}
}
func rowSnapshot(o contract.ObjectRef, t contract.Table, row []contract.Cell, identity contract.RowIdentity) *contract.RowSnapshot {
	snap := &contract.RowSnapshot{Mode: "values", Ref: contract.RowRef{Object: o, Database: o.Schema, Table: o.Name, Columns: append([]string(nil), identity.Columns...)}, Metadata: "values"}
	for _, c := range identity.Values {
		snap.Ref.TypedValues = append(snap.Ref.TypedValues, c.Clone())
		if c.Kind == contract.Binary {
			snap.Ref.Values = append(snap.Ref.Values, bytes.Clone(c.Bytes))
		} else {
			snap.Ref.Values = append(snap.Ref.Values, []byte(c.Text))
		}
	}
	for _, f := range t.Columns {
		snap.Columns = append(snap.Columns, f.Name)
	}
	for _, c := range row {
		snap.Original = append(snap.Original, c.Clone())
	}
	return snap
}
func pageModel(p contract.Page) *contract.DataPage {
	editable := p.Table.Editable
	out := &contract.DataPage{Object: p.Table.Object, Editable: &editable, Database: p.Table.Object.Schema, Table: p.Table.Object.Name, Offset: p.Offset, HasMore: p.More, StableOrder: len(p.Table.Key) > 0}
	out.ColumnMetadata = structureModel(p.Table).Columns
	for _, f := range p.Table.Columns {
		out.Columns = append(out.Columns, f.Name)
		out.ColumnKinds = append(out.ColumnKinds, f.DatabaseType)
	}
	for i, row := range p.Rows {
		cells := make([]contract.DataCell, len(row))
		truncated := false
		for j, c := range row {
			cells[j] = preview(c)
			truncated = truncated || c.Truncated
		}
		out.Rows = append(out.Rows, cells)
		var snap *contract.RowSnapshot
		if editable && !truncated && i < len(p.Identities) && len(p.Identities[i].Columns) > 0 {
			snap = rowSnapshot(p.Table.Object, p.Table, row, p.Identities[i])
		}
		out.RowSnapshots = append(out.RowSnapshots, snap)
		if i < len(p.Identities) && len(p.Identities[i].Columns) > 0 {
			identity := rowSnapshot(p.Table.Object, p.Table, nil, p.Identities[i])
			out.RowRefs = append(out.RowRefs, &identity.Ref)
		} else {
			out.RowRefs = append(out.RowRefs, nil)
		}
	}
	return out
}
func (s *Session) pageRequest(id int64, schema, name string, o contract.DataOptions) (contract.PageRequest, error) {
	ref := s.Namespace(schema).Object("table", name)
	if o.Object.ProfileID != 0 {
		if o.Object.Namespace() != ref.Namespace() || o.Object.Name != name {
			return contract.PageRequest{}, connections.ErrInvalid
		}
		ref = o.Object
	}
	if id != s.id {
		return contract.PageRequest{}, contract.ErrUnsupported
	}
	r := contract.PageRequest{Object: ref, Offset: o.Offset, Limit: o.Limit}
	filters := append([]contract.DataFilterRule(nil), o.Filters...)
	if len(filters) == 0 && o.FilterMode != "" {
		filters = append(filters, contract.DataFilterRule{Column: o.FilterColumn, Mode: o.FilterMode, Value: o.FilterValue})
	}
	ops := map[string]string{"equal": "=", "not_equal": "!=", "less": "<", "greater": ">", "less_equal": "<=", "greater_equal": ">=", "like": "LIKE", "is_null": "IS NULL", "is_not_null": "IS NOT NULL"}
	for _, f := range filters {
		op, ok := ops[f.Mode]
		if !ok {
			return r, contract.ErrUnsupported
		}
		r.Filters = append(r.Filters, contract.Filter{Column: f.Column, Operator: op, Value: contract.Cell{Kind: contract.Text, Text: f.Value}})
	}
	sorts := append([]contract.DataSortRule(nil), o.Sorts...)
	if len(sorts) == 0 && o.SortColumn != "" {
		sorts = append(sorts, contract.DataSortRule{Column: o.SortColumn, Descending: o.SortDescending})
	}
	for _, v := range sorts {
		r.Sorts = append(r.Sorts, contract.Sort{Column: v.Column, Descending: v.Descending})
	}
	return r, nil
}
func (s *Session) readData(ctx context.Context, id int64, schema, name string, o contract.DataOptions, tx contract.Transaction) (*contract.DataPage, error) {
	r, e := s.pageRequest(id, schema, name, o)
	if e != nil {
		return nil, e
	}
	var out *contract.DataPage
	e = s.dataQuery(ctx, r.Object, tx, func(ctx context.Context, q queryer) error {
		var err error
		out, err = s.advancedDataPage(ctx, q, r, o)
		return err
	})
	return out, e
}
func (s *Session) readRow(ctx context.Context, id int64, schema, name string, ref contract.RowRef, tx contract.Transaction) (*contract.DataRow, error) {
	o := s.Namespace(schema).Object("table", name)
	if id != s.id || ref.Object != o || len(ref.Columns) == 0 || len(ref.Columns) != len(ref.TypedValues) {
		return nil, connections.ErrInvalid
	}
	r := contract.PageRequest{Object: o, Limit: 2}
	for i, c := range ref.Columns {
		r.Filters = append(r.Filters, contract.Filter{Column: c, Operator: "=", Value: ref.TypedValues[i].Clone()})
	}
	var p contract.Page
	e := s.dataQuery(ctx, o, tx, func(ctx context.Context, q queryer) error {
		t, err := s.structure(ctx, q, o)
		if err != nil {
			return err
		}
		if err = validateDataRef(t, ref); err != nil {
			return err
		}
		// Complete editing remains explicit; the server also enforces its bound.
		p, err = s.page(ctx, q, r)
		return err
	})
	if e != nil {
		return nil, e
	}
	if len(p.Rows) != 1 || len(p.Identities) != 1 {
		return nil, ErrConflict
	}
	for _, c := range p.Rows[0] {
		if c.Truncated {
			return nil, errors.New("complete record exceeds the editor limit")
		}
	}
	out := &contract.DataRow{Columns: structureModel(p.Table).Columns, Snapshot: rowSnapshot(o, p.Table, p.Rows[0], p.Identities[0]), Values: map[string]contract.DataValue{}}
	for i, f := range p.Table.Columns {
		out.Values[f.Name] = dataValue(p.Rows[0][i])
	}
	return out, nil
}
func (s *Session) edits(ctx context.Context, o contract.ObjectRef, edits []contract.DataEdit) ([]contract.Edit, error) {
	meta, e := s.Structure(ctx, o)
	if e != nil {
		return nil, e
	}
	fields := map[string]contract.Field{}
	for _, f := range meta.Columns {
		fields[f.Name] = f
	}
	var out []contract.Edit
	for _, v := range edits {
		edit := contract.Edit{Kind: v.Kind, Values: map[string]contract.Cell{}}
		if v.Kind != "insert" {
			if v.Original == nil || v.Original.Mode != "values" || v.Original.Ref.Object != o {
				return nil, connections.ErrInvalid
			}
			if len(v.Original.Columns) != len(meta.Columns) {
				return nil, connections.ErrInvalid
			}
			for i, f := range meta.Columns {
				if v.Original.Columns[i] != f.Name {
					return nil, connections.ErrInvalid
				}
			}
			edit.Identity = contract.RowIdentity{Columns: append([]string(nil), v.Original.Ref.Columns...)}
			for _, c := range v.Original.Ref.TypedValues {
				edit.Identity.Values = append(edit.Identity.Values, c.Clone())
			}
			for _, c := range v.Original.Original {
				edit.Original = append(edit.Original, c.Clone())
			}
		}
		for name, value := range v.Values {
			f, ok := fields[name]
			if !ok {
				return nil, connections.ErrInvalid
			}
			if value.Default {
				return nil, contract.ErrUnsupported
			}
			kind := value.Kind
			if kind == "" {
				kind = typeKind(f.DatabaseType)
			}
			if value.Null {
				kind = contract.Null
			} else if value.Binary {
				kind = contract.Binary
			}
			edit.Values[name] = contract.Cell{Kind: kind, Text: value.Text, Bytes: bytes.Clone(value.Bytes)}
		}
		out = append(out, edit)
	}
	return out, nil
}

type dataTx struct {
	contract.Transaction
	session *Session
	object  contract.ObjectRef
	result  contract.CommitResult
}

func (s *Session) saveData(ctx context.Context, id int64, schema, name string, edits []contract.DataEdit, t *dataTx) (*contract.DataEditResult, error) {
	o := s.Namespace(schema).Object("table", name)
	if id != s.id {
		return nil, connections.ErrInvalid
	}
	values, e := s.edits(ctx, o, edits)
	if e != nil {
		return nil, e
	}
	own := t == nil
	if own {
		tx, e := s.Begin(ctx)
		if e != nil {
			return nil, e
		}
		t = &dataTx{Transaction: tx, session: s, object: o}
		defer t.Close()
	}
	if t.session != s || t.object != o {
		return nil, connections.ErrInvalid
	}
	r, e := t.Apply(ctx, o, values)
	out := &contract.DataEditResult{Applied: r.Applied, Saved: r.Saved, Incomplete: r.Incomplete, CommitUncertain: r.Uncertain}
	if e != nil {
		return out, e
	}
	for i, value := range values {
		outcome := contract.DataEditOutcome{Kind: value.Kind, Original: edits[i].Original}
		if value.Kind != "delete" && i < len(r.Saved) {
			saved := r.Saved[i]
			ref := contract.RowRef{Object: o, Columns: saved.Identity.Columns, TypedValues: saved.Identity.Values}
			row, err := s.readRow(ctx, id, schema, name, ref, t.Transaction)
			if err != nil {
				out.Incomplete = true
				return out, err
			}
			outcome.Saved = row.Snapshot
			for _, f := range row.Columns {
				outcome.PreviewColumns = append(outcome.PreviewColumns, f.Name)
				outcome.Previews = append(outcome.Previews, preview(cellValue(row.Values[f.Name])))
			}
		}
		out.Outcomes = append(out.Outcomes, outcome)
	}
	if own {
		committed, err := t.Commit(ctx)
		out.CommitConfirmed = committed.CommitConfirmed
		out.CommitUncertain = committed.Uncertain
		out.Incomplete = committed.Incomplete
		out.Verified = committed.Verified
		out.Saved = committed.Saved
		return out, err
	}
	return out, nil
}
func cellValue(v contract.DataValue) contract.Cell {
	k := v.Kind
	if v.Null {
		k = contract.Null
	}
	if v.Binary {
		k = contract.Binary
	}
	return contract.Cell{Kind: k, Text: v.Text, Bytes: append([]byte(nil), v.Bytes...)}
}
func (s *Session) DataRuntime() contract.DataRuntime {
	r := contract.DataRuntime{Count: s.countData, Chunk: func(ctx context.Context, id int64, db, t, f string, ref contract.RowRef, offset int64, units int) (*contract.FieldChunk, error) {
		return s.chunkData(ctx, id, db, t, f, ref, offset, units, nil)
	}, Analyzer: s.analyzeData, Capabilities: s.Capabilities(), FilterModes: []string{"equal", "not_equal", "less", "greater", "less_equal", "greater_equal", "like", "is_null", "is_not_null"}, AdvancedLoader: func(ctx context.Context, id int64, db, t string, o contract.DataOptions) (*contract.DataPage, error) {
		return s.readData(ctx, id, db, t, o, nil)
	}, RowReader: func(ctx context.Context, id int64, db, t string, ref contract.RowRef) (*contract.DataRow, error) {
		return s.readRow(ctx, id, db, t, ref, nil)
	}, Saver: func(ctx context.Context, id int64, db, t string, v []contract.DataEdit) (*contract.DataEditResult, error) {
		return s.saveData(ctx, id, db, t, v, nil)
	}, TableMetadata: func(ctx context.Context, id int64, db, t string) (*contract.TableStructure, error) {
		if id != s.id {
			return nil, connections.ErrInvalid
		}
		return s.TableMetadata(ctx, s.Namespace(db).Object("table", t))
	}, TableInfo: func(ctx context.Context, id int64, db, t string) (*contract.TableInfo, error) {
		if id != s.id {
			return nil, connections.ErrInvalid
		}
		meta, e := s.TableMetadata(ctx, s.Namespace(db).Object("table", t))
		if e != nil {
			return nil, e
		}
		kind := "BASE TABLE"
		if meta.Object.Kind == "view" {
			kind = "VIEW"
		}
		descriptor, _ := connections.Describe(s.kind)
		return &contract.TableInfo{Database: db, Name: t, Kind: kind, Engine: descriptor.Label}, nil
	}}
	r.Verify = func(ctx context.Context, id int64, db, t string, result *contract.DataEditResult) (*contract.DataVerification, error) {
		if id != s.id || result == nil || len(result.Saved) == 0 {
			return &contract.DataVerification{Status: "indeterminate"}, nil
		}
		v, e := s.Verify(ctx, s.Namespace(db).Object("table", t), result.Saved)
		status := "indeterminate"
		if e == nil && v.Matched {
			status = "applied"
		}
		verification := &contract.DataVerification{Status: status, States: v.States}
		if status == "applied" {
			recovered := &contract.DataEditResult{CommitConfirmed: true, Verified: true, Saved: result.Saved}
			object := s.Namespace(db).Object("table", t)
			for i, saved := range result.Saved {
				outcome := contract.DataEditOutcome{Kind: saved.Kind}
				if i < len(result.Outcomes) {
					outcome.Original = result.Outcomes[i].Original
				}
				if saved.Kind != "delete" {
					row, err := s.readRow(ctx, id, db, t, contract.RowRef{Object: object, Columns: saved.Identity.Columns, TypedValues: saved.Identity.Values}, nil)
					if err != nil {
						verification.Status = "indeterminate"
						return verification, err
					}
					outcome.Saved = row.Snapshot
					for _, field := range row.Columns {
						outcome.PreviewColumns = append(outcome.PreviewColumns, field.Name)
						outcome.Previews = append(outcome.Previews, preview(cellValue(row.Values[field.Name])))
					}
				}
				recovered.Outcomes = append(recovered.Outcomes, outcome)
			}
			verification.Recovered = recovered
		}
		return verification, e
	}
	r.Transactions = contract.DataTransactions{
		Chunk: func(ctx context.Context, t contract.DataTransaction, id int64, db, name, f string, ref contract.RowRef, offset int64, units int) (*contract.FieldChunk, error) {
			v, ok := t.(*dataTx)
			if !ok || v.session != s || v.object != s.Namespace(db).Object("table", name) {
				return nil, connections.ErrInvalid
			}
			return s.chunkData(ctx, id, db, name, f, ref, offset, units, v.Transaction)
		},
		Begin: func(ctx context.Context, id int64, db, t string) (contract.DataTransaction, error) {
			if id != s.id {
				return nil, connections.ErrInvalid
			}
			tx, e := s.Begin(ctx)
			if e != nil {
				return nil, e
			}
			return &dataTx{Transaction: tx, session: s, object: s.Namespace(db).Object("table", t)}, nil
		}, Close: func(t contract.DataTransaction) {
			if t != nil {
				t.Close()
			}
		},
		Commit: func(ctx context.Context, t contract.DataTransaction) error {
			v, ok := t.(*dataTx)
			if !ok || v.session != s {
				return connections.ErrInvalid
			}
			r, e := v.Commit(ctx)
			v.result = r
			if r.Uncertain || r.Incomplete {
				return &contract.CommitError{Result: r, Err: e}
			}
			return e
		}, Rollback: func(ctx context.Context, t contract.DataTransaction) error {
			v, ok := t.(*dataTx)
			if !ok || v.session != s {
				return connections.ErrInvalid
			}
			return v.Rollback(ctx)
		},
		Page: func(ctx context.Context, t contract.DataTransaction, id int64, db, name string, o contract.DataOptions) (*contract.DataPage, error) {
			v, ok := t.(*dataTx)
			if !ok || v.session != s || v.object != s.Namespace(db).Object("table", name) {
				return nil, connections.ErrInvalid
			}
			return s.readData(ctx, id, db, name, o, v.Transaction)
		},
		Save: func(ctx context.Context, t contract.DataTransaction, id int64, db, name string, v []contract.DataEdit) (*contract.DataEditResult, error) {
			tx, ok := t.(*dataTx)
			if !ok || tx.session != s {
				return nil, connections.ErrInvalid
			}
			return s.saveData(ctx, id, db, name, v, tx)
		},
		Row: func(ctx context.Context, t contract.DataTransaction, id int64, db, name string, ref contract.RowRef) (*contract.DataRow, error) {
			v, ok := t.(*dataTx)
			if !ok || v.session != s || v.object != s.Namespace(db).Object("table", name) {
				return nil, connections.ErrInvalid
			}
			return s.readRow(ctx, id, db, name, ref, v.Transaction)
		},
	}
	r.JSONL = func(ctx context.Context, o contract.ObjectRef, w io.Writer) error {
		if e := s.check(o); e != nil {
			return e
		}
		l, e := s.Reserve(ctx)
		if e != nil {
			return e
		}
		defer l.Close()
		_, e = l.Stream(ctx, "SELECT * FROM "+qualified(o), &jsonSink{ctx: ctx, encoder: json.NewEncoder(w)})
		return e
	}
	return r
}

type jsonSink struct {
	ctx     context.Context
	encoder *json.Encoder
}

func (s *jsonSink) BeginResult(i int, c []contract.Column) error {
	if i != 0 {
		return fmt.Errorf("table export expected one result")
	}
	return s.encoder.Encode(struct {
		Version int
		Columns []contract.Column
	}{1, c})
}
func (s *jsonSink) WriteRow(i int, c []contract.Cell) error {
	if e := s.ctx.Err(); e != nil {
		return e
	}
	return s.encoder.Encode(c)
}
func (s *jsonSink) EndResult(int, int64) error { return nil }
func (s *Session) ReserveQuery(tab uint64) (contract.QueryLease, error) {
	if tab == 0 || s.isClosed() {
		return nil, contract.ErrRevoked
	}
	return &queryLease{session: s}, nil
}

type queryLease struct {
	ops      sync.Mutex
	mu       sync.Mutex
	prepare  sync.Mutex
	session  *Session
	lease    contract.SQLLease
	released bool
}

func (l *queryLease) Preconnect(ctx context.Context) error {
	l.prepare.Lock()
	defer l.prepare.Unlock()
	l.mu.Lock()
	if l.released {
		l.mu.Unlock()
		return contract.ErrRevoked
	}
	if l.lease != nil {
		l.mu.Unlock()
		return nil
	}
	l.mu.Unlock()
	x, e := l.session.Reserve(ctx)
	if e != nil {
		return e
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		x.Close()
		return contract.ErrRevoked
	}
	l.lease = x
	return nil
}
func (l *queryLease) Execute(ctx context.Context, schema, q string) (contract.SQLResult, error) {
	return l.ExecuteStream(ctx, schema, q, nil)
}
func (l *queryLease) ExecuteStream(ctx context.Context, schema, q string, sink func(int, []contract.Column, []contract.Cell) error) (contract.SQLResult, error) {
	l.ops.Lock()
	defer l.ops.Unlock()
	if e := l.Preconnect(ctx); e != nil {
		return contract.SQLResult{}, e
	}
	l.mu.Lock()
	x := l.lease
	released := l.released
	l.mu.Unlock()
	if released {
		return contract.SQLResult{}, contract.ErrRevoked
	}
	if e := x.SetSchema(ctx, schema); e != nil {
		return contract.SQLResult{}, e
	}
	if sink == nil {
		return x.Execute(ctx, q)
	}
	return x.Stream(ctx, q, streamSink{sink})
}
func (l *queryLease) Replace() (contract.QueryLease, error) {
	l.Release()
	return &queryLease{session: l.session}, nil
}
func (l *queryLease) Release() {
	l.mu.Lock()
	if l.released {
		l.mu.Unlock()
		return
	}
	l.released = true
	x := l.lease
	l.mu.Unlock()
	if x != nil {
		_ = x.Close()
	}
}

type streamSink struct {
	sink func(int, []contract.Column, []contract.Cell) error
}

func (s streamSink) BeginResult(i int, c []contract.Column) error { return s.sink(i, c, nil) }
func (s streamSink) WriteRow(i int, c []contract.Cell) error      { return s.sink(i, nil, c) }
func (s streamSink) EndResult(int, int64) error                   { return nil }

func (s *Session) DataReads() contract.DataReads {
	r := s.DataRuntime()
	return contract.DataReads{Loader: r.Loader, AdvancedLoader: r.AdvancedLoader, RowReader: r.RowReader, TableMetadata: r.TableMetadata, TableInfo: r.TableInfo, Count: r.Count, Chunk: r.Chunk}
}

func (s *Session) DataEdits() contract.DataEdits {
	r := s.DataRuntime()
	return contract.DataEdits{Saver: r.Saver, Verify: r.Verify}
}

func (s *Session) DataExports() contract.DataExports {
	r := s.DataRuntime()
	return contract.DataExports{Exporter: r.Exporter, JSONL: r.JSONL}
}

func (s *Session) DataTransactions() contract.DataTransactions { return s.DataRuntime().Transactions }
func (s *Session) DataFilterModes() []string {
	return append([]string(nil), s.DataRuntime().FilterModes...)
}

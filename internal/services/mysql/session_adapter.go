package mysql

import (
	"context"
	"encoding/hex"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqlresult"
	"github.com/nyakang/nyadb/internal/services/connections"
	"io"
	"strings"
	"sync"
)

// OpenSession creates a private backend manager. Its methods can never route an
// old request to a replacement connection with the same numeric profile ID.
func OpenSession(ctx context.Context, p connections.Profile, password string, template *MySQLSessionManager) (contract.Session, error) {
	settings, e := connections.ResolveActiveSettings(p)
	if e != nil {
		return nil, e
	}
	m := NewMySQLSessionManager()
	if template != nil {
		m.TransportProvider = template.TransportProvider
		m.InitialSQLLoader = template.InitialSQLLoader
		m.TransportInvalidated = template.TransportInvalidated
		m.opener = template.opener
	}
	if m.TransportProvider != nil {
		e = ValidateMySQLTransportSettings(settings, password)
	} else {
		_, e = DirectMySQLConfig(settings, password)
	}
	if e != nil {
		return nil, e
	}
	if e = m.Connect(ctx, settings, password); e != nil {
		_ = m.Close()
		return nil, e
	}
	return &SessionAdapter{manager: m, id: p.ID, kind: p.Kind}, nil
}

type SessionAdapter struct {
	manager *MySQLSessionManager
	id      int64
	kind    connections.Kind
}

func (s *SessionAdapter) ProfileID() int64   { return s.id }
func (s *SessionAdapter) Generation() uint64 { return s.manager.SessionGeneration(s.id) }
func (s *SessionAdapter) Capabilities() contract.Capabilities {
	kind := s.kind
	if kind == "" {
		kind = connections.MySQL
	}
	d, _ := connections.Describe(kind)
	return d.Capabilities
}
func (s *SessionAdapter) Alive() bool {
	return s.manager.IsConnected(s.id) && !s.manager.TunnelRevoked(s.id)
}
func (s *SessionAdapter) Close() error { return s.manager.Close() }
func (s *SessionAdapter) Namespace(database string) contract.NamespaceRef {
	return contract.NamespaceRef{ProfileID: s.id, Catalog: database}
}
func (s *SessionAdapter) check(o contract.ObjectRef) error {
	if !s.Alive() {
		return contract.ErrRevoked
	}
	if o.ProfileID != s.id || o.Catalog == "" || o.Schema != "" {
		return connections.ErrInvalid
	}
	return nil
}
func (s *SessionAdapter) Namespaces(ctx context.Context) ([]contract.Namespace, error) {
	names, e := s.manager.Databases(ctx, s.id)
	if e != nil {
		return nil, e
	}
	out := make([]contract.Namespace, len(names))
	for i, n := range names {
		out[i] = contract.Namespace{Ref: s.Namespace(n), Label: n}
	}
	return out, nil
}
func (s *SessionAdapter) Browse(ctx context.Context, n contract.NamespaceRef) ([]contract.DBObject, error) {
	if e := s.check(n.Object("table", "")); e != nil {
		return nil, e
	}
	out, e := s.manager.ObjectsWithPrograms(ctx, s.id, n.Catalog)
	for i := range out {
		kind := out[i].Kind
		if kind == "表" {
			kind = "table"
		} else if kind == "视图" {
			kind = "view"
		} else if kind == "函数" {
			kind = "function"
		} else if kind == "存储过程" {
			kind = "procedure"
		} else if kind == "事件" {
			kind = "event"
		}
		out[i].Ref = n.Object(kind, out[i].Name)
	}
	return out, e
}
func (s *SessionAdapter) TableMetadata(ctx context.Context, o contract.ObjectRef) (*contract.TableStructure, error) {
	if e := s.check(o); e != nil {
		return nil, e
	}
	t, e := s.manager.TableStructure(ctx, s.id, o.Catalog, o.Name)
	if t != nil {
		t.Object = o
	}
	return t, e
}
func (s *SessionAdapter) ReserveQuery(tab uint64) (contract.QueryLease, error) {
	t, e := s.manager.ReserveSQLTabToken(s.id, tab)
	if e != nil {
		return nil, e
	}
	return &queryLease{manager: s.manager, token: t}, nil
}

type queryLease struct {
	mu      sync.Mutex
	manager *MySQLSessionManager
	token   MySQLSQLTabToken
}

func (l *queryLease) Preconnect(ctx context.Context) error {
	return l.manager.PreconnectSQLTabToken(ctx, l.token)
}
func (l *queryLease) Execute(ctx context.Context, db, q string) (contract.SQLResult, error) {
	return l.manager.ExecuteSQLTabToken(ctx, l.token, db, q)
}
func (l *queryLease) ExecuteStream(ctx context.Context, db, q string, sink func(int, []contract.Column, []contract.Cell) error) (contract.SQLResult, error) {
	return l.manager.ExecuteSQLTabTokenStream(ctx, l.token, db, q, sqlresult.Sink(sink))
}
func (l *queryLease) Replace() (contract.QueryLease, error) {
	t, e := l.manager.ReplaceSQLTabToken(l.token)
	if e != nil {
		return nil, e
	}
	return &queryLease{manager: l.manager, token: t}, nil
}
func (l *queryLease) Release() { l.manager.ReleaseSQLTabToken(l.token) }
func (s *SessionAdapter) DataRuntime() contract.DataRuntime {
	m := s.manager
	r := contract.DataRuntime{Capabilities: s.Capabilities(), FilterModes: []string{"equal", "not_equal", "contains", "not_contains", "like", "not_like", "less", "greater", "is_null", "is_not_null"},
		TableMetadata: m.TableStructure, TableInfo: m.TableInfo, Count: m.CountTableRows, Chunk: m.ReadMySQLFieldChunk,
		StreamImporter: m.ImportMySQLDataStream, Importer: m.ImportMySQLData, NewTableImporter: m.ImportMySQLDataIntoNewTable, Analyzer: m.AnalyzeMySQLData, Exporter: m.ExportMySQLData, AdvancedLoader: m.TableDataPageWithOptions, Saver: m.ApplyMySQLDataEdits, RowReader: m.ReadMySQLDataRow, Verify: m.VerifyMySQLDataEdits}
	r.Transactions = contract.DataTransactions{
		Begin: func(ctx context.Context, id int64, db, table string) (contract.DataTransaction, error) {
			return m.BeginMySQLDataTransaction(ctx, id, db, table)
		},
		Close: func(t contract.DataTransaction) {
			if t != nil {
				t.Close()
			}
		},
		Commit: func(ctx context.Context, t contract.DataTransaction) error {
			return m.CommitMySQLDataTransaction(ctx, mysqlTransaction(t))
		},
		Rollback: func(ctx context.Context, t contract.DataTransaction) error {
			token := mysqlTransaction(t)
			if token == nil {
				return connections.ErrInvalid
			}
			token.mu.Lock()
			m.mu.Lock()
			owned := m.sessions[token.profile] == token.session
			m.mu.Unlock()
			token.mu.Unlock()
			if !owned {
				return contract.ErrRevoked
			}
			return m.RollbackMySQLDataTransaction(ctx, token)
		},
		Page: func(ctx context.Context, t contract.DataTransaction, id int64, db, table string, o contract.DataOptions) (*contract.DataPage, error) {
			return m.TableDataPageInTransaction(ctx, mysqlTransaction(t), id, db, table, o)
		},
		Save: func(ctx context.Context, t contract.DataTransaction, id int64, db, table string, e []contract.DataEdit) (*contract.DataEditResult, error) {
			return m.ApplyMySQLDataEditsInTransaction(ctx, mysqlTransaction(t), id, db, table, e)
		},
		Row: func(ctx context.Context, t contract.DataTransaction, id int64, db, table string, ref contract.RowRef) (*contract.DataRow, error) {
			return m.ReadMySQLDataRowInTransaction(ctx, mysqlTransaction(t), id, db, table, ref)
		},
		Chunk: func(ctx context.Context, t contract.DataTransaction, id int64, db, table, col string, ref contract.RowRef, offset int64, units int) (*contract.FieldChunk, error) {
			return m.ReadMySQLFieldChunkInTransaction(ctx, mysqlTransaction(t), id, db, table, col, ref, offset, units)
		},
	}
	advanced := r.AdvancedLoader
	r.AdvancedLoader = func(ctx context.Context, id int64, db, table string, options contract.DataOptions) (*contract.DataPage, error) {
		object := contract.ObjectRef{ProfileID: id, Catalog: db, Kind: "table", Name: table}
		if options.Object.ProfileID != 0 {
			object = options.Object
			if object.ProfileID != id || object.Catalog != db || object.Schema != "" || object.Name != table {
				return nil, connections.ErrInvalid
			}
		}
		page, e := advanced(ctx, id, db, table, options)
		annotateMySQLPage(page, object)
		return page, e
	}
	saver := r.Saver
	r.Saver = func(ctx context.Context, id int64, db, table string, edits []contract.DataEdit) (*contract.DataEditResult, error) {
		if e := validateMySQLSnapshots(id, db, table, edits); e != nil {
			return nil, e
		}
		result, e := saver(ctx, id, db, table, edits)
		annotateMySQLResult(result, id, db, table)
		return result, e
	}
	reader := r.RowReader
	r.RowReader = func(ctx context.Context, id int64, db, table string, ref contract.RowRef) (*contract.DataRow, error) {
		if e := validateMySQLRef(id, db, table, ref); e != nil {
			return nil, e
		}
		row, e := reader(ctx, id, db, table, ref)
		annotateMySQLRow(row, id, db, table)
		return row, e
	}
	pageTx, saveTx, rowTx := r.Transactions.Page, r.Transactions.Save, r.Transactions.Row
	r.Transactions.Page = func(ctx context.Context, t contract.DataTransaction, id int64, db, table string, o contract.DataOptions) (*contract.DataPage, error) {
		object := contract.ObjectRef{ProfileID: id, Catalog: db, Kind: "table", Name: table}
		if o.Object.ProfileID != 0 {
			object = o.Object
			if e := validateMySQLRef(id, db, table, contract.RowRef{Object: object}); e != nil {
				return nil, e
			}
		}
		page, e := pageTx(ctx, t, id, db, table, o)
		annotateMySQLPage(page, object)
		return page, e
	}
	r.Transactions.Save = func(ctx context.Context, t contract.DataTransaction, id int64, db, table string, edits []contract.DataEdit) (*contract.DataEditResult, error) {
		if e := validateMySQLSnapshots(id, db, table, edits); e != nil {
			return nil, e
		}
		result, e := saveTx(ctx, t, id, db, table, edits)
		annotateMySQLResult(result, id, db, table)
		return result, e
	}
	r.Transactions.Row = func(ctx context.Context, t contract.DataTransaction, id int64, db, table string, ref contract.RowRef) (*contract.DataRow, error) {
		if e := validateMySQLRef(id, db, table, ref); e != nil {
			return nil, e
		}
		row, e := rowTx(ctx, t, id, db, table, ref)
		annotateMySQLRow(row, id, db, table)
		return row, e
	}

	return r
}

// Backend is used only by the optional MySQL administration adapter.
func (s *SessionAdapter) Administration() Administration { return s.manager }

func DataRuntime(m *MySQLSessionManager) contract.DataRuntime {
	if m == nil {
		return contract.DataRuntime{}
	}
	return (&SessionAdapter{manager: m}).DataRuntime()
}

func mysqlTransaction(t contract.DataTransaction) *MySQLDataTransaction {
	token, _ := t.(*MySQLDataTransaction)
	return token
}

func (l *queryLease) ExecuteFile(ctx context.Context, db string, r io.Reader) (contract.SQLResult, error) {
	return l.manager.ExecuteSQLTabTokenFile(ctx, l.token, db, r)
}

func mysqlTypedPreview(preview contract.DataCell, databaseType string) contract.Cell {
	value := contract.Cell{Kind: contract.Text, Text: preview.Text, Truncated: preview.Truncated}
	if preview.Null {
		value.Kind = contract.Null
		value.Text = ""
		return value
	}
	kind := strings.ToLower(databaseType)
	switch {
	case mysqlDataBinaryType(kind):
		value.Kind = contract.Binary
		value.Bytes, _ = hex.DecodeString(strings.TrimPrefix(preview.Text, "0x"))
		value.Text = ""
	case strings.Contains(kind, "int"):
		value.Kind = contract.Integer
	case kind == "decimal" || kind == "numeric":
		value.Kind = contract.Decimal
	case kind == "float" || kind == "double" || kind == "real":
		value.Kind = contract.Floating
	case kind == "bool" || kind == "boolean":
		value.Kind = contract.Boolean
	case strings.Contains(kind, "date") || strings.Contains(kind, "time"):
		value.Kind = contract.DateTime
	case kind == "json":
		value.Kind = contract.JSON
	}
	return value
}

func (s *SessionAdapter) DataReads() contract.DataReads {
	r := s.DataRuntime()
	return contract.DataReads{Loader: r.Loader, AdvancedLoader: r.AdvancedLoader, RowReader: r.RowReader, TableMetadata: r.TableMetadata, TableInfo: r.TableInfo, Count: r.Count, Chunk: r.Chunk}
}

func (s *SessionAdapter) DataEdits() contract.DataEdits {
	r := s.DataRuntime()
	return contract.DataEdits{Saver: r.Saver, Verify: r.Verify}
}

func (s *SessionAdapter) DataExports() contract.DataExports {
	r := s.DataRuntime()
	return contract.DataExports{Exporter: r.Exporter, JSONL: r.JSONL}
}

func (s *SessionAdapter) DataAnalysisOperations() contract.DataAnalysisOperations {
	r := s.DataRuntime()
	return contract.DataAnalysisOperations{Analyzer: r.Analyzer}
}

func (s *SessionAdapter) DataImports() contract.DataImports {
	r := s.DataRuntime()
	return contract.DataImports{NewTableImporter: r.NewTableImporter, Importer: r.Importer, StreamImporter: r.StreamImporter}
}
func (s *SessionAdapter) DataTransactions() contract.DataTransactions {
	return s.DataRuntime().Transactions
}
func (s *SessionAdapter) DataFilterModes() []string {
	return append([]string(nil), s.DataRuntime().FilterModes...)
}

func annotateMySQLPage(page *contract.DataPage, object contract.ObjectRef) {
	if page == nil {
		return
	}
	page.Object = object
	editable := object.Kind != "view" && page.StableOrder
	page.Editable = &editable
	for i, row := range page.Rows {
		for j := range row {
			kind := ""
			if j < len(page.ColumnMetadata) {
				kind = page.ColumnMetadata[j].DataType
			}
			v := mysqlTypedPreview(row[j], kind)
			page.Rows[i][j].Value = &v
		}
	}
	for i, ref := range page.RowRefs {
		if ref != nil {
			ref.Object = object
		}
		if i < len(page.RowSnapshots) && page.RowSnapshots[i] != nil {
			page.RowSnapshots[i].Ref.Object = object
		}
	}
}
func annotateMySQLRow(row *contract.DataRow, id int64, db, table string) {
	if row == nil {
		return
	}
	if row.Snapshot != nil {
		row.Snapshot.Ref.Object = contract.ObjectRef{ProfileID: id, Catalog: db, Kind: "table", Name: table}
	}
	for _, field := range row.Columns {
		v, ok := row.Values[field.Name]
		if !ok {
			continue
		}
		v.Kind = mysqlTypedPreview(contract.DataCell{Text: v.Text, Null: v.Null}, field.DataType).Kind
		if v.Binary {
			v.Kind = contract.Binary
		}
		row.Values[field.Name] = v
	}
}
func annotateMySQLResult(result *contract.DataEditResult, id int64, db, table string) {
	if result == nil {
		return
	}
	for i := range result.Outcomes {
		o := &result.Outcomes[i]
		if o.Saved != nil {
			o.Saved.Ref.Object = contract.ObjectRef{ProfileID: id, Catalog: db, Kind: "table", Name: table}
		}
	}
}
func validateMySQLRef(id int64, db, table string, ref contract.RowRef) error {
	o := ref.Object
	if o.ProfileID != 0 && (o.ProfileID != id || o.Catalog != db || o.Schema != "" || o.Name != table || o.Kind == "view") {
		return connections.ErrInvalid
	}
	return nil
}
func validateMySQLSnapshots(id int64, db, table string, edits []contract.DataEdit) error {
	for _, edit := range edits {
		if edit.Original != nil {
			if edit.Original.Mode != "" && edit.Original.Mode != "fingerprints" {
				return connections.ErrInvalid
			}
			if e := validateMySQLRef(id, db, table, edit.Original.Ref); e != nil {
				return e
			}
		}
	}
	return nil
}

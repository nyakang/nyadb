package engines

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/importspec"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func (s *Session) DataImports() contract.DataImports {
	return contract.DataImports{Dialect: string(s.kind), Importer: s.importData, NewTableImporter: s.importNewTable, StreamImporter: s.importStream}
}

type importRows struct {
	edits []contract.DataEdit
	index int
}

func (r *importRows) Close() error { return nil }
func (r *importRows) Next(ctx context.Context) (contract.DataEdit, error) {
	if err := ctx.Err(); err != nil {
		return contract.DataEdit{}, err
	}
	if r.index == len(r.edits) {
		return contract.DataEdit{}, io.EOF
	}
	e := r.edits[r.index]
	r.index++
	return e, nil
}
func (s *Session) importData(ctx context.Context, id int64, db, table string, r contract.DataImportRequest) (*contract.DataImportResult, error) {
	r, err := importspec.Own(r)
	if err != nil {
		return nil, err
	}
	edits := r.Edits
	r.Edits = nil
	out, err := s.importStream(ctx, id, db, table, contract.DataImportStreamRequest{Request: r, Rows: int64(len(edits)), Open: func(context.Context) (contract.DataImportRowSource, error) { return &importRows{edits: edits}, nil }})
	if out == nil {
		return nil, err
	}
	saved := &contract.DataEditResult{CommitConfirmed: out.Committed, CommitUncertain: out.CommitUncertain}
	if out.Committed {
		saved.Applied = int(out.Progress.Prepared)
	}
	return &contract.DataImportResult{Created: out.Created, CreationUncertain: out.CreationUncertain, Committed: out.Committed, CommitUncertain: out.CommitUncertain, Rows: out.Rows, Saved: saved}, err
}
func (s *Session) importNewTable(ctx context.Context, id int64, db, table string, cols []contract.DataImportColumn, edits []contract.DataEdit) (*contract.DataNewTableImportResult, error) {
	o, e := s.importData(ctx, id, db, table, contract.DataImportRequest{NewTable: true, Columns: cols, Edits: edits})
	if o == nil {
		return nil, e
	}
	return &contract.DataNewTableImportResult{Created: o.Created, CreationUncertain: o.CreationUncertain, Saved: o.Saved}, e
}

// A single owned background connection and explicit transaction keep both the
// source and diagnostic memory bounded. No session transaction is reused.
func (s *Session) importStream(ctx context.Context, id int64, db, table string, stream contract.DataImportStreamRequest) (out *contract.DataImportStreamResult, err error) {
	out = &contract.DataImportStreamResult{}
	defer func() {
		if err == nil {
			return
		}
		var pg *pgconn.PgError
		if errors.As(err, &pg) {
			err = fmt.Errorf("database rejected import (SQLSTATE %s)", pg.Code)
		}
	}()
	o := s.Namespace(db).Object("table", table)
	if id != s.id {
		return out, connections.ErrInvalid
	}
	if s.isClosed() || stream.Generation != 0 && stream.Generation != s.generation {
		return out, contract.ErrRevoked
	}
	if err = s.check(o); err != nil {
		return
	}
	if s.namespaceReadOnly(db) || !s.Capabilities().Import {
		return out, contract.ErrUnsupported
	}
	if stream.Open == nil || stream.Rows < 0 || stream.IgnoreForeignKeys || stream.CanonicalBackupValues {
		return out, contract.ErrUnsupported
	}
	r, err := importspec.Own(stream.Request)
	if err != nil {
		return out, err
	}
	if len(r.Edits) > 0 {
		return out, contract.ErrDataPage
	}
	if err = importspec.Identifier(string(s.kind), table); err != nil {
		return
	}
	ctx, done := s.requestContext(ctx)
	defer done()
	if s.kind == connections.SQLite {
		release, e := s.schema.acquire(ctx, true)
		if e != nil {
			return out, e
		}
		defer release()
	}
	c, err := s.db.Conn(ctx)
	if err != nil {
		return out, err
	}
	defer c.Close()
	begin := "BEGIN"
	if s.kind == connections.SQLite {
		begin = "BEGIN IMMEDIATE"
	}
	if _, err = c.ExecContext(ctx, begin); err != nil {
		return
	}
	settled, commitAttempted := false, false
	defer func() {
		if !settled {
			rb := rollbackDesign(c)
			if rb != nil {
				discardDesign(c)
				err = errors.Join(err, rb)
			}
			out.CommitUncertain = commitAttempted || rb != nil
			if s.kind == connections.CockroachDB && cockroachRetryable(err) {
				out.CommitUncertain = false
			}
			out.CreationUncertain = r.NewTable && out.CommitUncertain
		}
		for i := range out.Rows {
			if out.Rows[i].Status == "prepared" {
				switch {
				case out.Committed:
					out.Rows[i].Status = "committed"
				case out.CommitUncertain:
					out.Rows[i].Status = "uncertain"
				default:
					out.Rows[i].Status = "rolled_back"
				}
			}
		}
	}()
	if r.NewTable {
		ddl, e := importspec.DDL(string(s.kind), db, table, r.Columns)
		if e != nil {
			return out, e
		}
		if _, err = c.ExecContext(ctx, ddl); err != nil {
			return
		}
	} else if s.kind == connections.PostgreSQL {
		if _, err = c.ExecContext(ctx, "LOCK TABLE "+qualified(o)+" IN SHARE ROW EXCLUSIVE MODE"); err != nil {
			return
		}
	}
	meta, err := s.structure(ctx, c, o)
	if err != nil {
		return out, err
	}
	if meta.Object.Kind != "table" {
		return out, contract.ErrUnsupported
	}
	columns := structureModel(meta).Columns
	// Identity defaults are server-managed; no OVERRIDING clause is injected.
	if s.kind == connections.PostgreSQL {
		var kind string
		if err = c.QueryRowContext(ctx, `SELECT relkind FROM pg_class WHERE oid=to_regclass($1)`, qualified(o)).Scan(&kind); err != nil {
			return
		}
		if kind != "r" {
			return out, contract.ErrUnsupported
		}
		rows, e := c.QueryContext(ctx, `SELECT attname,attidentity FROM pg_attribute WHERE attrelid=to_regclass($1) AND attidentity<>''`, qualified(o))
		if e != nil {
			return out, e
		}
		for rows.Next() {
			var name, identity string
			if e = rows.Scan(&name, &identity); e != nil {
				break
			}
			for i := range columns {
				if columns[i].Name == name {
					v := "identity"
					columns[i].Default = &v
				}
			}
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		if e != nil {
			return out, e
		}
	}
	if importspec.NeedsKey(r.Mode) {
		keys, e := s.importKeys(ctx, c, meta)
		if e != nil {
			return out, e
		}
		match := slices.Clone(r.MatchColumns)
		sort.Strings(match)
		found := false
		for _, key := range keys {
			key = slices.Clone(key)
			sort.Strings(key)
			if slices.Equal(match, key) {
				found = true
				break
			}
		}
		if !found {
			return out, contract.ErrDataReadOnly
		}
	}
	if err = importspec.Validate(string(s.kind), columns, r); err != nil {
		return
	}
	if r.Mode == "replace" {
		if _, err = c.ExecContext(ctx, "DELETE FROM "+qualified(o)); err != nil {
			return
		}
	}
	source, err := stream.Open(ctx)
	if err != nil {
		return out, err
	}
	sourceClosed := false
	defer func() {
		if !sourceClosed {
			_ = source.Close()
		}
	}()
	for {
		edit, e := source.Next(ctx)
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return out, e
		}
		if out.Progress.Processed >= stream.Rows {
			return out, errors.New("source contains more rows than the reviewed plan")
		}
		row := contract.DataImportRowResult{Database: db, Table: table, Row: int(out.Progress.Processed + 1), Status: "prepared"}
		if _, err = c.ExecContext(ctx, "SAVEPOINT nyadb_import_row"); err != nil {
			return
		}
		one := r
		one.Edits = []contract.DataEdit{edit}
		one.ContinueOnError = false
		e = importspec.Validate(string(s.kind), columns, one)
		if e == nil {
			row.Kind, e = s.importRow(ctx, c, o, columns, r, edit)
		}
		out.Progress.Processed++
		if e != nil {
			row.Kind, row.Status, row.Error = "error", "failed", "row validation or database constraint failed"
			out.Progress.Failed++
			if !r.ContinueOnError || ctx.Err() != nil {
				if len(out.Rows) < 1000 {
					out.Rows = append(out.Rows, row)
				}
				if stream.Record != nil {
					if recordErr := stream.Record(row); recordErr != nil {
						return out, recordErr
					}
				}
				return out, e
			}
			if _, err = c.ExecContext(ctx, "ROLLBACK TO SAVEPOINT nyadb_import_row"); err != nil {
				return
			}
		} else if row.Kind == "skip" {
			row.Status = "skipped"
			out.Progress.Skipped++
		} else {
			out.Progress.Prepared++
		}
		if _, err = c.ExecContext(ctx, "RELEASE SAVEPOINT nyadb_import_row"); err != nil {
			return
		}
		if len(out.Rows) < 1000 {
			out.Rows = append(out.Rows, row)
		}
		if stream.Record != nil {
			if err = stream.Record(row); err != nil {
				return
			}
		}
		if stream.Progress != nil {
			stream.Progress(out.Progress)
		}
	}
	if out.Progress.Processed != stream.Rows {
		return out, errors.New("source row count differs from the reviewed plan")
	}
	sourceClosed = true
	if err = source.Close(); err != nil {
		return
	}
	commitAttempted = true
	if _, err = c.ExecContext(ctx, "COMMIT"); err != nil {
		return
	}
	settled = true
	out.Committed = true
	out.Created = r.NewTable
	return
}

func (s *Session) importRow(ctx context.Context, c *sql.Conn, o contract.ObjectRef, columns []contract.StructureColumn, r contract.DataImportRequest, edit contract.DataEdit) (string, error) {
	values := map[string]any{}
	for _, col := range columns {
		v, ok := edit.Values[col.Name]
		if !ok {
			continue
		}
		arg, err := s.importParameter(col, v)
		if err != nil {
			return "", err
		}
		values[col.Name] = arg
	}
	var names, places, where []string
	var args []any
	for n := range values {
		names = append(names, n)
	}
	sort.Strings(names)
	if importspec.NeedsKey(r.Mode) {
		for _, n := range r.MatchColumns {
			args = append(args, values[n])
			where = append(where, quote(n)+"="+s.arg(len(args)))
		}
		var exists bool
		if err := c.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM "+qualified(o)+" WHERE "+strings.Join(where, " AND ")+")", args...).Scan(&exists); err != nil {
			return "", err
		}
		if !exists && (r.Mode == "update" || r.Mode == "delete") || exists && r.Mode == "insert_missing" {
			return "skip", nil
		}
		if exists {
			q := "DELETE FROM " + qualified(o)
			kind := "delete"
			if r.Mode != "delete" {
				kind = "update"
				for _, n := range names {
					if slices.Contains(r.MatchColumns, n) {
						continue
					}
					args = append(args, values[n])
					places = append(places, quote(n)+"="+s.arg(len(args)))
				}
				if len(places) == 0 {
					return "skip", nil
				}
				q = "UPDATE " + qualified(o) + " SET " + strings.Join(places, ",")
			}
			r, e := c.ExecContext(ctx, q+" WHERE "+strings.Join(where, " AND "), args...)
			if e != nil {
				return "", e
			}
			n, e := r.RowsAffected()
			if e == nil && n != 1 {
				e = contract.ErrDataReadOnly
			}
			return kind, e
		}
	}
	args = nil
	places = nil
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = quote(n)
		args = append(args, values[n])
		places = append(places, s.arg(len(args)))
	}
	_, err := c.ExecContext(ctx, "INSERT INTO "+qualified(o)+" ("+strings.Join(quoted, ",")+") VALUES ("+strings.Join(places, ",")+")", args...)
	return "insert", err
}
func (s *Session) importParameter(col contract.StructureColumn, v contract.DataValue) (any, error) {
	cell := cellValue(v)
	// Text readers (CSV, spreadsheets, and legacy import sources) have no
	// intrinsic value kind. Keep their exact text and let the target column
	// perform conversion; explicitly typed readers retain their validation.
	if cell.Kind == "" {
		cell.Kind = contract.Text
	}
	if err := validateTypedValue(cell, s.kind); err != nil {
		return nil, contract.ErrDataPage
	}
	if s.kind == connections.SQLite && cell.Kind != contract.Null && cell.Kind != contract.Binary {
		typ := strings.ToUpper(col.Type)
		if strings.Contains(typ, "TEXT") || strings.Contains(typ, "CHAR") || strings.Contains(typ, "CLOB") {
			return v.Text, nil
		}
		if strings.Contains(typ, "INT") {
			if cell.Kind == contract.Boolean {
				if n, _ := strconv.ParseBool(v.Text); n {
					return int64(1), nil
				}
				return int64(0), nil
			}
			n, e := strconv.ParseInt(v.Text, 10, 64)
			if e != nil {
				return nil, errors.New("value cannot be represented exactly as SQLite integer")
			}
			return n, nil
		}
		if !strings.Contains(typ, "TEXT") && !strings.Contains(typ, "CHAR") && !strings.Contains(typ, "CLOB") && typ != "BLOB" && typ != "" {
			if cell.Kind == contract.Floating {
				n, _ := strconv.ParseFloat(v.Text, 64)
				if math.IsNaN(n) {
					return nil, errors.New("SQLite cannot preserve a NaN value")
				}
				return n, nil
			}
			if n, ok := new(big.Rat).SetString(v.Text); ok {
				// REAL affinity converts even a bound int64 to float64. Only
				// NUMERIC affinity can retain a large integer without rounding.
				realAffinity := strings.Contains(typ, "REAL") || strings.Contains(typ, "FLOA") || strings.Contains(typ, "DOUB")
				if !realAffinity && n.IsInt() && n.Num().IsInt64() {
					return n.Num().Int64(), nil
				}
				f, exact := n.Float64()
				if !exact {
					return nil, errors.New("use a TEXT target to preserve this exact SQLite numeric value")
				}
				return f, nil
			}
		}
	}
	return parameter(cell), nil
}
func (s *Session) importKeys(ctx context.Context, c *sql.Conn, t contract.Table) ([][]string, error) {
	keys := [][]string{}
	if len(t.Key) > 0 {
		keys = append(keys, t.Key)
	}
	if s.kind == connections.CockroachDB {
		indexes, err := cockroachIndexes(ctx, c, t.Object)
		if err != nil {
			return nil, err
		}
		keys = nil
		columns := validColumns(t)
		for _, index := range indexes {
			if !index.unique || index.unsafe {
				continue
			}
			valid := len(index.columns) > 0
			var key []string
			for _, c := range index.columns {
				f, ok := columns[c.Name]
				valid = valid && ok && !f.Nullable && !strings.Contains(f.DatabaseType, "collate")
				key = append(key, c.Name)
			}
			if valid {
				keys = append(keys, key)
			}
		}
		return keys, nil
	}
	if s.kind == connections.PostgreSQL {
		rows, e := c.QueryContext(ctx, `SELECT i.indexrelid,a.attname FROM pg_index i CROSS JOIN LATERAL unnest(i.indkey) WITH ORDINALITY k(num,ord) JOIN pg_attribute a ON a.attrelid=i.indrelid AND a.attnum=k.num WHERE i.indrelid=to_regclass($1) AND i.indisunique AND i.indisvalid AND i.indimmediate AND i.indpred IS NULL AND i.indexprs IS NULL AND k.ord<=i.indnkeyatts AND NOT EXISTS (SELECT 1 FROM unnest(i.indkey) WITH ORDINALITY kk(num,ord) JOIN pg_attribute aa ON aa.attrelid=i.indrelid AND aa.attnum=kk.num WHERE kk.ord<=i.indnkeyatts AND NOT aa.attnotnull) ORDER BY i.indexrelid,k.ord`, qualified(t.Object))
		if e != nil {
			return nil, e
		}
		defer rows.Close()
		keys = nil
		var previous int64
		for rows.Next() {
			var id int64
			var name string
			if e = rows.Scan(&id, &name); e != nil {
				return nil, e
			}
			if id != previous {
				keys = append(keys, nil)
				previous = id
			}
			keys[len(keys)-1] = append(keys[len(keys)-1], name)
		}
		return keys, rows.Err()
	}
	rows, e := c.QueryContext(ctx, "PRAGMA "+quote(t.Object.Schema)+".index_list("+quote(t.Object.Name)+")")
	if e != nil {
		return nil, e
	}
	var indexes []string
	for rows.Next() {
		var seq, unique, partial int
		var name, origin string
		if e = rows.Scan(&seq, &name, &unique, &origin, &partial); e != nil {
			break
		}
		if unique == 1 && partial == 0 {
			indexes = append(indexes, name)
		}
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return nil, e
	}
	for _, idx := range indexes {
		rows, e = c.QueryContext(ctx, "PRAGMA "+quote(t.Object.Schema)+".index_info("+quote(idx)+")")
		if e != nil {
			return nil, e
		}
		var key []string
		valid := true
		for rows.Next() {
			var seq, cid int
			var name sql.NullString
			if e = rows.Scan(&seq, &cid, &name); e != nil {
				break
			}
			found := false
			for _, f := range t.Columns {
				if f.Name == name.String && !f.Nullable && !f.Generated {
					found = true
				}
			}
			valid = valid && name.Valid && cid >= 0 && found
			key = append(key, name.String)
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		if e != nil {
			return nil, e
		}
		if valid && len(key) > 0 {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

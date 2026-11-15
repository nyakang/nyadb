package h2

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func (s *Session) DataReads() contract.DataReads {
	return contract.DataReads{Loader: s.load, AdvancedLoader: s.page, RowReader: s.readRow, TableMetadata: s.tableMetadata, TableInfo: s.tableInfo, Count: s.count, Chunk: s.chunk}
}

func (s *Session) DataFilterModes() []string {
	return []string{"equal", "not_equal", "contains", "not_contains", "like", "not_like", "less", "greater", "is_null", "is_not_null"}
}

func (s *Session) resolve(ctx context.Context, session *agentbridge.Session, id int64, schema, name string, object contract.ObjectRef) (*contract.TableStructure, error) {
	ref := contract.ObjectRef{ProfileID: id, Catalog: s.catalog, Schema: schema, Kind: "table", Name: name}
	if object != (contract.ObjectRef{}) {
		if object.Namespace() != ref.Namespace() || object.Name != name {
			return nil, connections.ErrInvalid
		}
		ref = object
	}
	if err := s.check(ref); err != nil {
		return nil, err
	}
	if object == (contract.ObjectRef{}) {
		q, err := s.query(ctx, session, "SELECT TABLE_TYPE FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA="+textLiteral(schema)+" AND TABLE_NAME="+textLiteral(name), 2)
		if err != nil {
			return nil, err
		}
		if len(q.Sets) != 1 || len(q.Sets[0].Rows) != 1 || len(q.Sets[0].Rows[0]) != 1 || q.Truncated {
			return nil, connections.ErrNotFound
		}
		switch q.Sets[0].Rows[0][0].Text {
		case "TABLE", "BASE TABLE":
		case "VIEW":
			ref.Kind = "view"
		default:
			return nil, contract.ErrUnsupported
		}
	}
	return s.structure(ctx, session, ref)
}

func (s *Session) tableMetadata(ctx context.Context, id int64, schema, name string) (out *contract.TableStructure, err error) {
	err = s.withMetadata(ctx, func(ctx context.Context, session *agentbridge.Session) error {
		out, err = s.resolve(ctx, session, id, schema, name, contract.ObjectRef{})
		return err
	})
	return
}

func (s *Session) tableInfo(ctx context.Context, id int64, schema, name string) (*contract.TableInfo, error) {
	t, err := s.tableMetadata(ctx, id, schema, name)
	if err != nil {
		return nil, err
	}
	return &contract.TableInfo{Database: schema, Name: name, Kind: t.Kind, Engine: t.Engine, Comment: t.Comment}, nil
}

func textLiteral(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }

// Native H2 character lengths use UTF-16 code units, and OCTET_LENGTH differs
// between 1.x and 2.x. Read text as bounded UTF-8 bytes and trim only complete
// code points in Go. Native field offsets consequently count UTF-8 bytes.
func valueBytes(c contract.StructureColumn) string {
	if c.ValueKind == contract.Binary {
		return quote(c.Name)
	}
	return "STRINGTOUTF8(CAST(" + quote(c.Name) + " AS VARCHAR))"
}

func valueLength(c contract.StructureColumn) string {
	return "(LENGTH(RAWTOHEX(" + valueBytes(c) + "))/2)"
}

func fingerprint(c contract.StructureColumn) string {
	return "RAWTOHEX(HASH('SHA256'," + valueBytes(c) + "))"
}

func projection(c contract.StructureColumn, limit int) string {
	if c.ValueKind == contract.Text || c.ValueKind == contract.JSON || c.ValueKind == contract.Binary {
		p := fmt.Sprintf("SUBSTRING(%s,1,%d)", valueBytes(c), limit+4)
		if c.ValueKind != contract.Binary {
			p = "CASE WHEN CAST(" + quote(c.Name) + " AS VARCHAR)=UTF8TOSTRING(" + valueBytes(c) + ") THEN " + p + " ELSE NULL END"
		}
		return p
	}
	return quote(c.Name)
}

func readable(c contract.StructureColumn) bool {
	switch strings.ToUpper(c.DataType) {
	case "TINYINT", "SMALLINT", "INT", "INTEGER", "BIGINT", "NUMERIC", "DECIMAL", "DECFLOAT", "REAL", "DOUBLE", "DOUBLE PRECISION", "FLOAT", "BOOLEAN", "BOOL", "BINARY", "BINARY VARYING", "VARBINARY", "BINARY LARGE OBJECT", "BLOB", "DATE", "TIME", "TIME WITH TIME ZONE", "TIMESTAMP", "TIMESTAMP WITH TIME ZONE", "CHAR", "CHARACTER", "VARCHAR", "CHARACTER VARYING", "VARCHAR_IGNORECASE", "CLOB", "CHARACTER LARGE OBJECT", "UUID", "JSON":
		return true
	}
	return false
}

func keyColumns(t *contract.TableStructure) []string {
	for _, primary := range []bool{true, false} {
		for _, index := range t.Indexes {
			if !index.Unique || (index.Type == "PRIMARY KEY") != primary {
				continue
			}
			var keys []string
			for _, key := range index.Columns {
				for _, c := range t.Columns {
					if c.Name == key.Name && !c.Nullable && readable(c) && c.ValueKind != contract.Floating && c.ValueKind != contract.JSON && c.Extra != "computed" {
						keys = append(keys, key.Name)
					}
				}
			}
			if len(keys) > 0 && len(keys) == len(index.Columns) {
				return keys
			}
		}
	}
	return nil
}

func dataQuery(t *contract.TableStructure, o contract.DataOptions, masked bool) (string, error) {
	return readQuery(t, o, masked, t.Columns, 1024, false)
}

func readQuery(t *contract.TableStructure, o contract.DataOptions, masked bool, columns []contract.StructureColumn, fieldLimit int, exporting bool) (string, error) {
	if !exporting && (o.Offset < 0 || o.Limit < 1 || o.Limit > contract.DataMaxPageSize) {
		return "", contract.ErrDataPage
	}
	if o.After != nil || o.UseKeyset {
		return "", contract.ErrUnsupported
	}
	known := map[string]contract.StructureColumn{}
	var fields []string
	for _, c := range t.Columns {
		known[c.Name] = c
	}
	var guards, lengths []string
	for _, c := range columns {
		if !readable(c) {
			return "", contract.ErrUnsupported
		}
		length := "COALESCE(" + valueLength(c) + ",0)"
		lengths = append(lengths, length)
		guards = append(guards, length+"<="+strconv.Itoa(fieldLimit))
	}
	guards = append(guards, "("+strings.Join(lengths, "+")+")<="+strconv.Itoa(contract.DataExportRowMaxBytes))
	guard := strings.Join(guards, " AND ")
	for _, c := range columns {
		field := projection(c, fieldLimit)
		if exporting {
			field = "CASE WHEN " + guard + " THEN " + field + " ELSE NULL END"
		}
		fields = append(fields, field+" AS "+quote(c.Name))
	}
	for _, c := range columns {
		fields = append(fields, fingerprint(c))
	}
	for _, c := range columns {
		fields = append(fields, valueLength(c))
	}
	if exporting {
		fields = append(fields, "CASE WHEN "+guard+" THEN 1 ELSE 0 END")
	}
	query := "SELECT " + strings.Join(fields, ",") + " FROM " + qualified(t.Object)
	expr, err := contract.EffectiveDataExpression(o)
	if err != nil {
		return "", err
	}
	where, err := contract.CompileFilter(expr, func(f contract.DataFilterRule) (string, error) {
		c, ok := known[f.Column]
		if !ok || !readable(c) || len(f.Value) > contract.DataEditMaxBytes || !utf8.ValidString(f.Value) || strings.ContainsRune(f.Value, 0) {
			return "", connections.ErrInvalid
		}
		op := map[string]string{"": "=", "equal": "=", "not_equal": "<>", "less": "<", "greater": ">", "like": "LIKE", "not_like": "NOT LIKE", "contains": "LIKE", "not_contains": "NOT LIKE", "is_null": "IS NULL", "is_not_null": "IS NOT NULL"}[f.Mode]
		if op == "" || c.ValueKind == contract.JSON || c.ValueKind == contract.Binary {
			return "", contract.ErrUnsupported
		}
		q := quote(c.Name) + " " + op
		if f.Mode == "is_null" || f.Mode == "is_not_null" {
			return q, nil
		}
		v := f.Value
		if strings.Contains(op, "LIKE") && c.ValueKind != contract.Text {
			return "", contract.ErrUnsupported
		}
		if f.Mode == "contains" || f.Mode == "not_contains" {
			v = "%" + strings.NewReplacer("~", "~~", "%", "~%", "_", "~_").Replace(v) + "%"
		}
		literal := textLiteral(v)
		if masked {
			literal = "?"
		}
		q += " " + literal
		if f.Mode == "contains" || f.Mode == "not_contains" {
			q += " ESCAPE '~'"
		}
		return q, nil
	})
	if err != nil {
		return "", err
	}
	if where != "" {
		query += " WHERE " + where
	}
	sorts := append([]contract.DataSortRule(nil), o.Sorts...)
	if len(sorts) == 0 && o.SortColumn != "" {
		sorts = append(sorts, contract.DataSortRule{Column: o.SortColumn, Descending: o.SortDescending})
	}
	if len(sorts) > contract.DataMaxSortRules {
		return "", connections.ErrInvalid
	}
	used := map[string]bool{}
	var order []string
	for _, sort := range sorts {
		c, ok := known[sort.Column]
		if !ok || used[sort.Column] || c.ValueKind == contract.JSON || c.ValueKind == contract.Binary {
			return "", contract.ErrUnsupported
		}
		used[sort.Column] = true
		dir := " ASC"
		if sort.Descending {
			dir = " DESC"
		}
		order = append(order, quote(sort.Column)+dir)
	}
	for _, key := range keyColumns(t) {
		if !used[key] {
			order = append(order, quote(key)+" ASC")
		}
	}
	if len(order) > 0 {
		query += " ORDER BY " + strings.Join(order, ",")
	}
	if exporting {
		return query, nil
	}
	return query + fmt.Sprintf(" LIMIT %d OFFSET %d", o.Limit+1, o.Offset), nil
}

func (s *Session) load(ctx context.Context, id int64, schema, name string, offset int64, limit int) (*contract.DataPage, error) {
	return s.page(ctx, id, schema, name, contract.DataOptions{Offset: offset, Limit: limit})
}

func (s *Session) page(ctx context.Context, id int64, schema, name string, o contract.DataOptions) (out *contract.DataPage, err error) {
	start := time.Now()
	err = s.withFeature(ctx, func(ctx context.Context, session *agentbridge.Session) (err error) {
		t, err := s.resolve(ctx, session, id, schema, name, o.Object)
		if err != nil {
			return err
		}
		query, err := dataQuery(t, o, false)
		if err != nil {
			return err
		}
		cursor, nativePage, err := session.OpenQueryCursor(ctx, agentbridge.QueryCursorOptions{SQL: query, Schema: schema, PageSize: 1, MaxRows: o.Limit + 2, TimeoutSeconds: s.profile.H2Options.QueryTimeoutSeconds, Decoder: decodeValue})
		if err != nil {
			return err
		}
		defer func() {
			if closeErr := cursor.Close(); err == nil {
				err = closeErr
			}
		}()
		template, err := dataQuery(t, o, true)
		if err != nil {
			return err
		}
		editable := t.Editable
		out = &contract.DataPage{Object: t.Object, Editable: &editable, Database: schema, Table: name, ColumnMetadata: t.Columns, Offset: o.Offset, Limit: o.Limit, StableOrder: len(keyColumns(t)) > 0, QuerySQL: template, LiveRead: true}
		for _, c := range t.Columns {
			out.Columns = append(out.Columns, c.Name)
			out.ColumnKinds = append(out.ColumnKinds, c.DataType)
		}
		budget := 16 << 20
		for {
			if len(nativePage.SQL.Sets) != 1 || nativePage.SQL.Truncated {
				return contract.ErrDataPage
			}
			for _, raw := range nativePage.SQL.Sets[0].Rows {
				if len(out.Rows) == o.Limit {
					out.HasMore = true
					break
				}
				cells, previews, fp, err := rowCells(t, raw, 1024, false)
				if err != nil {
					return err
				}
				for _, v := range cells {
					budget -= len(v.Text) + len(v.Bytes) + 128
				}
				if budget < 0 {
					return contract.ErrDataPage
				}
				out.Rows = append(out.Rows, previews)
				snap := s.snapshot(t, cells, fp, "fingerprints")
				var ref *contract.RowRef
				if snap != nil {
					ref = &snap.Ref
				}
				out.RowRefs = append(out.RowRefs, ref)
				if !editable {
					snap = nil
				}
				out.RowSnapshots = append(out.RowSnapshots, snap)
			}
			if out.HasMore || !nativePage.HasMore {
				break
			}
			nativePage, err = cursor.Fetch(ctx, 1)
			if err != nil {
				return err
			}
		}
		out.CompletedAt, out.Duration = time.Now(), time.Since(start)
		return nil
	})
	return
}

func rowCells(t *contract.TableStructure, raw []contract.Cell, limit int, full bool) ([]contract.Cell, []contract.DataCell, []*string, error) {
	n := len(t.Columns)
	if len(raw) != n*3 {
		return nil, nil, nil, contract.ErrUnsupported
	}
	cells, previews, fp := make([]contract.Cell, n), make([]contract.DataCell, n), make([]*string, n)
	budget := 0
	for i, c := range t.Columns {
		v := raw[i].Clone()
		if (v.Kind == contract.Null) != (raw[2*n+i].Kind == contract.Null) || (v.Kind == contract.Null) != (raw[n+i].Kind == contract.Null) {
			return nil, nil, nil, contract.ErrUnsupported
		}
		if v.Kind != contract.Null {
			length, err := strconv.ParseInt(raw[2*n+i].Text, 10, 64)
			if err != nil || length < 0 {
				return nil, nil, nil, contract.ErrUnsupported
			}
			if full && length > int64(limit) {
				return nil, nil, nil, contract.ErrDataEditLimit
			}
			if c.ValueKind == contract.Text || c.ValueKind == contract.JSON || c.ValueKind == contract.Binary {
				if v.Kind != contract.Binary {
					return nil, nil, nil, contract.ErrUnsupported
				}
				data := v.Bytes
				if len(data) > limit {
					data = data[:limit]
				}
				if c.ValueKind != contract.Binary {
					for len(data) > 0 && !utf8.Valid(data) {
						data = data[:len(data)-1]
					}
					v.Kind, v.Text, v.Bytes = c.ValueKind, string(data), nil
				} else {
					v.Bytes = bytes.Clone(data)
				}
				v.Truncated = int64(len(data)) < length
			}
			if full && v.Truncated {
				return nil, nil, nil, contract.ErrDataEditLimit
			}
		}
		cells[i] = v
		budget += len(v.Text) + len(v.Bytes)
		preview := v.Clone()
		if len(preview.Bytes) > 1024 {
			preview.Bytes, preview.Truncated = bytes.Clone(preview.Bytes[:1024]), true
		}
		if len(preview.Text) > 1024 {
			data := []byte(preview.Text[:1024])
			for !utf8.Valid(data) {
				data = data[:len(data)-1]
			}
			preview.Text, preview.Truncated = string(data), true
		}
		previews[i] = contract.DataCell{Value: &preview, Text: preview.Display(), Null: preview.Kind == contract.Null, Truncated: preview.Truncated}
		if raw[n+i].Kind != contract.Null {
			hash := raw[n+i].Text
			if len(hash) != 64 {
				return nil, nil, nil, contract.ErrUnsupported
			}
			if _, err := hex.DecodeString(hash); err != nil {
				return nil, nil, nil, contract.ErrUnsupported
			}
			fp[i] = &hash
		}
	}
	if full && budget > contract.DataEditMaxBytes {
		return nil, nil, nil, contract.ErrDataEditLimit
	}
	return cells, previews, fp, nil
}

func (s *Session) metadataSeal(t *contract.TableStructure) string {
	stable := *t
	// SCRIPT includes moving identity/sequence restart positions. They do not
	// invalidate an unchanged row's schema after an unrelated insertion.
	stable.Definition = ""
	raw, _ := json.Marshal(stable)
	h := sha256.Sum256(append([]byte(s.scope), raw...))
	return hex.EncodeToString(h[:])
}

func (s *Session) snapshot(t *contract.TableStructure, cells []contract.Cell, fp []*string, mode string) *contract.RowSnapshot {
	keys := keyColumns(t)
	if len(keys) == 0 {
		return nil
	}
	snap := &contract.RowSnapshot{Mode: mode, Metadata: s.metadataSeal(t), Fingerprints: fp, Ref: contract.RowRef{Object: t.Object, Database: t.Object.Schema, Table: t.Name, Columns: keys}}
	for _, c := range t.Columns {
		snap.Columns = append(snap.Columns, c.Name)
	}
	for _, key := range keys {
		for i, c := range t.Columns {
			if key != c.Name {
				continue
			}
			v := cells[i].Clone()
			if v.Truncated || v.Kind == contract.Null {
				return nil
			}
			snap.Ref.TypedValues = append(snap.Ref.TypedValues, v)
			data := []byte(v.Text)
			if v.Kind == contract.Binary {
				data = bytes.Clone(v.Bytes)
			}
			snap.Ref.Values = append(snap.Ref.Values, data)
		}
	}
	if mode == "values" {
		for _, v := range cells {
			snap.Original = append(snap.Original, v.Clone())
		}
	}
	return snap
}

func (s *Session) count(ctx context.Context, id int64, schema, name string) (out int64, err error) {
	err = s.withFeature(ctx, func(ctx context.Context, session *agentbridge.Session) error {
		t, err := s.resolve(ctx, session, id, schema, name, contract.ObjectRef{})
		if err != nil {
			return err
		}
		q, err := s.query(ctx, session, "SELECT COUNT(*) FROM "+qualified(t.Object), 1)
		if err != nil {
			return err
		}
		if len(q.Sets) != 1 || len(q.Sets[0].Rows) != 1 || len(q.Sets[0].Rows[0]) != 1 {
			return contract.ErrUnsupported
		}
		out, err = strconv.ParseInt(q.Sets[0].Rows[0][0].Text, 10, 64)
		return err
	})
	return
}

package engines

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// Every portable read uses its original physical session and one transaction.
// A page is a snapshot, not a long-lived snapshot across navigation requests.
func (s *Session) dataQuery(ctx context.Context, o contract.ObjectRef, owner contract.Transaction, read func(context.Context, queryer) error) error {
	ctx, done := s.requestContext(ctx)
	defer done()
	if err := s.check(o); err != nil {
		return err
	}
	if owner != nil {
		tx, ok := owner.(*Tx)
		if !ok || tx.s != s {
			return connections.ErrInvalid
		}
		tx.mu.Lock()
		defer tx.mu.Unlock()
		if tx.closed {
			return ErrRevoked
		}
		err := read(ctx, tx.tx)
		if s.kind == connections.CockroachDB && cockroachRetryable(err) {
			return tx.abortCockroachRetry(err)
		}
		return err
	}
	opts := &sql.TxOptions{ReadOnly: true}
	if s.kind.IsPostgreSQLFamily() {
		opts.Isolation = sql.LevelRepeatableRead
	}
	tx, err := s.db.BeginTx(ctx, opts)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Protect qualified relation identity while metadata and values are read.
	if s.kind == connections.PostgreSQL {
		if _, err = tx.ExecContext(ctx, "LOCK TABLE "+qualified(o)+" IN ACCESS SHARE MODE"); err != nil {
			return err
		}
	}
	if err = read(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func queryOne(ctx context.Context, q queryer, text string, args []any, dest ...any) error {
	rows, err := q.QueryContext(ctx, text, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	if !rows.Next() {
		if err = rows.Err(); err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	if err = rows.Scan(dest...); err != nil {
		return err
	}
	if rows.Next() {
		return errors.New("expected one aggregate row")
	}
	return rows.Err()
}

func canonicalDataType(decl string) string {
	d := strings.ToLower(decl)
	if i := strings.IndexByte(d, '('); i >= 0 {
		d = d[:i]
	}
	return strings.TrimSpace(d)
}
func pgDataScalar(decl string) bool {
	switch canonicalDataType(decl) {
	case "smallint", "integer", "bigint", "oid", "real", "double precision", "boolean", "uuid", "date", "time without time zone", "time with time zone", "timestamp without time zone", "timestamp with time zone":
		return true
	}
	return false
}
func pgDataText(decl string) bool {
	switch canonicalDataType(decl) {
	case "text", "character varying", "character", "name":
		return true
	}
	return false
}
func pgDataNumeric(decl string) bool {
	switch canonicalDataType(decl) {
	case "smallint", "integer", "bigint", "numeric", "decimal", "real", "double precision":
		return true
	}
	return false
}

// The sentinel is fetched on the server. A large field is never fully sent to
// the grid; explicit complete-row reads use the separate 1 MiB editor bound.
func (s *Session) dataProjection(t contract.Table, units int) string {
	cols := make([]string, len(t.Columns))
	for i, f := range t.Columns {
		c := quote(f.Name)
		if s.kind == connections.SQLite {
			cols[i] = fmt.Sprintf("CASE WHEN typeof(%s)='text' AND instr(%s,char(0))>0 THEN _nyadb_text_slice_v1(%s,1,%d) WHEN typeof(%s) IN ('text','blob') AND length(%s)>0 THEN substr(%s,1,%d) ELSE %s END", c, c, c, units+1, c, c, c, units+1, c)
		} else if pgDataScalar(f.DatabaseType) {
			cols[i] = c
		} else if canonicalDataType(f.DatabaseType) == "bytea" {
			cols[i] = fmt.Sprintf("substring(%s from 1 for %d)", c, units+1)
		} else {
			cols[i] = fmt.Sprintf("substring(%s::text from 1 for %d)", c, units+1)
		}
	}
	return strings.Join(cols, ",")
}
func (s *Session) dataScan(rows *sql.Rows, t contract.Table, units, maxRows int) ([][]contract.Cell, error) {
	var result [][]contract.Cell
	budget := 16 << 20
	for rows.Next() {
		if len(result) >= maxRows {
			return nil, errors.New("bounded page returned too many rows")
		}
		raw := make([]any, len(t.Columns))
		dest := make([]any, len(raw))
		for i := range dest {
			dest[i] = &raw[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		row := make([]contract.Cell, len(raw))
		size := len(raw)*64 + 24
		for i, v := range raw {
			decl := canonicalDataType(t.Columns[i].DatabaseType)
			row[i] = cell(v, decl, s.kind)
			c := &row[i]
			if !utf8.ValidString(c.Text) {
				return nil, errors.New("invalid UTF-8 grid field")
			}
			if c.Kind == contract.Binary && len(c.Bytes) > units {
				c.Bytes = c.Bytes[:units]
				c.Truncated = true
			}
			if utf8.RuneCountInString(c.Text) > units {
				at := 0
				for n := 0; n < units; n++ {
					_, w := utf8.DecodeRuneInString(c.Text[at:])
					at += w
				}
				c.Text = strings.Clone(c.Text[:at])
				c.Truncated = true
			}
			// Full editing has a byte bound even for multibyte server characters.
			if len(c.Text) > 1<<20 {
				at := 1 << 20
				for at > 0 && !utf8.RuneStart(c.Text[at]) {
					at--
				}
				c.Text = strings.Clone(c.Text[:at])
				c.Truncated = true
			}
			size += len(c.Text) + len(c.Bytes)
		}
		if size > budget {
			return nil, errors.New("page memory limit exceeded; reduce page size")
		}
		budget -= size
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Session) dataWhere(t contract.Table, r contract.PageRequest) (string, []any, error) {
	expression, err := contract.EffectivePageExpression(r)
	if err != nil {
		return "", nil, err
	}
	cols := validColumns(t)
	args := []any{}
	leaf := func(f contract.Filter) (string, error) {
		if _, ok := cols[f.Column]; !ok || len(f.Value.Text) > 4096 || len(f.Value.Bytes) > 4096 || f.Value.Truncated {
			return "", connections.ErrInvalid
		}
		op := strings.ToUpper(f.Operator)
		switch op {
		case "=", "!=", "<", ">", "<=", ">=", "LIKE":
			args = append(args, parameter(f.Value))
			return quote(f.Column) + " " + op + " " + s.arg(len(args)), nil
		case "IS NULL", "IS NOT NULL":
			return quote(f.Column) + " " + op, nil
		default:
			return "", contract.ErrUnsupported
		}
	}
	where, err := contract.CompileFilter(expression, leaf)
	if where == "" {
		where = "1=1"
	} else if r.Condition == nil {
		where = strings.TrimSuffix(strings.TrimPrefix(where, "("), ")")
	}
	return where, args, err
}

type dataOrder struct {
	Column, Expr string
	Descending   bool
}

func (s *Session) dataOrder(ctx context.Context, q queryer, t contract.Table, r contract.PageRequest) ([]dataOrder, bool, string, error) {
	cols := validColumns(t)
	stable := len(t.Key) > 0 && len(t.Key) <= 16
	var identity string
	if s.kind == connections.SQLite {
		var version int64
		if err := queryOne(ctx, q, "PRAGMA "+quote(t.Object.Schema)+".schema_version", nil, &version); err != nil {
			return nil, false, "", err
		}
		identity = fmt.Sprint(version)
	} else if s.kind == connections.CockroachDB {
		identity, err := cockroachTableIdentity(ctx, q, t.Object)
		if err != nil {
			return nil, false, "", err
		}
		return s.cockroachDataOrder(t, r, identity)
	} else {
		// A custom index operator class cannot prove uniqueness for the comparison
		// used by ORDER BY. Reject seek rather than assuming interchangeable rules.
		var oid int64
		var approved bool
		err := queryOne(ctx, q, `SELECT c.oid, EXISTS(SELECT 1 FROM pg_index i JOIN pg_class ic ON ic.oid=i.indexrelid JOIN pg_am am ON am.oid=ic.relam WHERE i.indrelid=c.oid AND i.indisunique AND i.indisvalid AND i.indpred IS NULL AND i.indexprs IS NULL AND am.amname='btree' AND ARRAY(SELECT a.attname::text FROM unnest(i.indkey) WITH ORDINALITY k(num,ord) JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum=k.num WHERE k.ord<=i.indnkeyatts ORDER BY k.ord)=$3::text[] AND NOT EXISTS(SELECT 1 FROM unnest(i.indclass) ocid JOIN pg_opclass oc ON oc.oid=ocid JOIN pg_namespace ns ON ns.oid=oc.opcnamespace WHERE NOT oc.opcdefault OR ns.nspname<>'pg_catalog')) FROM pg_class c JOIN pg_namespace ns ON ns.oid=c.relnamespace WHERE ns.nspname=$1 AND c.relname=$2`, []any{t.Object.Schema, t.Object.Name, t.Key}, &oid, &approved)
		if err != nil {
			return nil, false, "", err
		}
		stable = stable && approved
		identity = fmt.Sprint(oid)
	}
	var orders []dataOrder
	if len(r.Sorts) > 16 {
		return nil, false, "", connections.ErrInvalid
	}
	for _, sort := range r.Sorts {
		f, ok := cols[sort.Column]
		if !ok {
			return nil, false, "", connections.ErrInvalid
		}
		if f.Nullable || (s.kind == connections.PostgreSQL && !pgDataScalar(f.DatabaseType) && !pgDataNumeric(f.DatabaseType) && !pgDataText(f.DatabaseType) && canonicalDataType(f.DatabaseType) != "bytea") {
			stable = false
		}
		orders = append(orders, dataOrder{sort.Column, quote(sort.Column), sort.Descending})
	}
	for _, key := range t.Key {
		f := cols[key]
		if f.Nullable || (s.kind == connections.PostgreSQL && !pgDataScalar(f.DatabaseType) && !pgDataNumeric(f.DatabaseType) && !pgDataText(f.DatabaseType) && canonicalDataType(f.DatabaseType) != "bytea") {
			stable = false
		}
		expr := quote(key)
		if s.kind == connections.SQLite {
			expr += " COLLATE BINARY"
		} else if pgDataText(f.DatabaseType) {
			expr += ` COLLATE "C"`
		}
		// Even the same column needs a binary tie breaker if its declared collation
		// makes distinct unique-index values compare equal in the user sort.
		orders = append(orders, dataOrder{key, expr, false})
	}
	return orders, stable, identity, nil
}
func dataRuleSignature(s *Session, t contract.Table, r contract.PageRequest, order []dataOrder, identity string) string {
	v := struct {
		Engine     connections.Kind
		Scope      string
		Generation uint64
		Table      contract.Table
		Condition  *contract.FilterExpression[contract.Filter] `json:",omitempty"`
		Filters    []contract.Filter
		Order      []dataOrder
		Identity   string
	}{s.kind, s.dataScope, s.generation, t, r.Condition, r.Filters, order, identity}
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (s *Session) seekWhere(cursor *contract.DataCursor, signature string, t contract.Table, order []dataOrder, args *[]any) (string, error) {
	if cursor.RuleSignature != signature || cursor.Database != t.Object.Schema || cursor.Table != t.Object.Name || len(cursor.Values) != len(order) || len(cursor.Columns) != len(order) || len(cursor.Descending) != len(order) {
		return "", errors.New("cursor no longer matches this object, session or rules")
	}
	var terms, equal []string
	for i, term := range order {
		if cursor.Columns[i] != term.Column || cursor.Descending[i] != term.Descending || len(cursor.Values[i]) > 8192 {
			return "", connections.ErrInvalid
		}
		var c contract.Cell
		if err := json.Unmarshal(cursor.Values[i], &c); err != nil {
			return "", connections.ErrInvalid
		}
		if c.Kind == contract.Null || c.Truncated || len(c.Text) > 4096 || len(c.Bytes) > 4096 {
			return "", connections.ErrInvalid
		}
		if err := validateTypedValue(c, s.kind); err != nil {
			return "", err
		}
		*args = append(*args, parameter(c))
		arg := s.arg(len(*args))
		op := ">"
		if term.Descending {
			op = "<"
		}
		terms = append(terms, "("+strings.Join(append(append([]string(nil), equal...), term.Expr+" "+op+" "+arg), " AND ")+")")
		equal = append(equal, term.Expr+" = "+arg)
	}
	return "(" + strings.Join(terms, " OR ") + ")", nil
}
func (s *Session) advancedDataPage(ctx context.Context, q queryer, r contract.PageRequest, o contract.DataOptions) (*contract.DataPage, error) {
	started := time.Now()
	if r.Limit <= 0 {
		r.Limit = 100
	}
	if r.Limit > 1000 || r.Offset < 0 {
		return nil, connections.ErrInvalid
	}
	t, err := s.structure(ctx, q, r.Object)
	if err != nil {
		return nil, err
	}
	if len(t.Columns) > 512 {
		return nil, errors.New("table exceeds the 512-column grid limit")
	}
	where, args, err := s.dataWhere(t, r)
	if err != nil {
		return nil, err
	}
	order, stable, identity, err := s.dataOrder(ctx, q, t, r)
	if err != nil {
		return nil, err
	}
	signature := dataRuleSignature(s, t, r, order, identity)
	if o.After != nil {
		if !o.UseKeyset || !stable {
			return nil, contract.ErrUnsupported
		}
		seek, e := s.seekWhere(o.After, signature, t, order, &args)
		if e != nil {
			return nil, e
		}
		where = "(" + where + ") AND " + seek
	}
	text := "SELECT " + s.dataProjection(t, 1024) + " FROM " + qualified(t.Object) + " WHERE " + where
	if len(order) > 0 {
		var terms []string
		for _, v := range order {
			term := v.Expr + " ASC"
			if v.Descending {
				term = v.Expr + " DESC"
			}
			terms = append(terms, term)
		}
		text += " ORDER BY " + strings.Join(terms, ",")
	}
	text += fmt.Sprintf(" LIMIT %d", r.Limit+1)
	if o.After == nil {
		text += fmt.Sprintf(" OFFSET %d", r.Offset)
	}
	rows, err := q.QueryContext(ctx, text, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values, err := s.dataScan(rows, t, 1024, r.Limit+1)
	if err != nil {
		return nil, err
	}
	p := contract.Page{Table: t, Rows: values, Offset: r.Offset, More: len(values) > r.Limit}
	if p.More {
		p.Rows = p.Rows[:r.Limit]
	}
	positions := map[string]int{}
	for i, f := range t.Columns {
		positions[f.Name] = i
	}
	for _, row := range p.Rows {
		id := contract.RowIdentity{Columns: append([]string(nil), t.Key...)}
		for _, k := range t.Key {
			c := row[positions[k]]
			if c.Truncated || c.Kind == contract.Null {
				id = contract.RowIdentity{}
				break
			}
			id.Values = append(id.Values, c.Clone())
		}
		p.Identities = append(p.Identities, id)
	}
	out := pageModel(p)
	out.Limit = r.Limit
	out.QuerySQL = text
	out.StableOrder = stable
	out.KeysetAvailable = o.UseKeyset && stable
	if out.KeysetAvailable && len(p.Rows) > 0 {
		cursor := &contract.DataCursor{RuleSignature: signature, Database: t.Object.Schema, Table: t.Object.Name}
		for _, term := range order {
			c := p.Rows[len(p.Rows)-1][positions[term.Column]]
			if c.Truncated || c.Kind == contract.Null {
				out.KeysetAvailable = false
				break
			}
			b, e := json.Marshal(c)
			if e != nil {
				return nil, e
			}
			cursor.Columns = append(cursor.Columns, term.Column)
			cursor.Descending = append(cursor.Descending, term.Descending)
			cursor.Values = append(cursor.Values, b)
		}
		if out.KeysetAvailable {
			out.NextCursor = cursor
		}
	}
	out.CompletedAt = time.Now()
	out.Duration = time.Since(started)
	return out, nil
}
func validateDataRef(t contract.Table, ref contract.RowRef) error {
	if ref.Object != t.Object || len(t.Key) == 0 || !reflect.DeepEqual(ref.Columns, t.Key) || len(ref.TypedValues) != len(t.Key) {
		return connections.ErrInvalid
	}
	for _, v := range ref.TypedValues {
		if v.Kind == contract.Null || v.Truncated || len(v.Text) > 4096 || len(v.Bytes) > 4096 {
			return connections.ErrInvalid
		}
	}
	return nil
}

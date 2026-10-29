package connections

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const MySQLDataEditMaxBytes = 1 << 20

var (
	ErrMySQLDataConflict        = errors.New("record changed or disappeared; reload before saving")
	ErrMySQLDataReadOnly        = errors.New("table has no complete non-null unique key or is not transactional")
	ErrMySQLDataEditLimit       = errors.New("complete record exceeds the 1 MiB editor limit")
	ErrMySQLDataCommitUncertain = errors.New("commit acknowledgement failed; verify before retrying")
)

// Values preserve exact decimal/integer spelling. DEFAULT and NULL are separate
// from an empty string. Binary values never travel through a text conversion.
type MySQLDataValue struct {
	Text                  string
	Bytes                 []byte
	Null, Default, Binary bool
}
type MySQLRowSnapshot struct {
	Ref          MySQLRowRef
	Columns      []string
	Fingerprints []*string // nil denotes SQL NULL, otherwise SHA256 of server bytes
	Metadata     string
}
type MySQLDataRow struct {
	Columns  []MySQLStructureColumn
	Values   map[string]MySQLDataValue
	Snapshot *MySQLRowSnapshot
}
type MySQLDataEdit struct {
	Kind     string // insert | update | delete
	Original *MySQLRowSnapshot
	Values   map[string]MySQLDataValue
}
type MySQLDataEditOutcome struct {
	Kind            string
	Original, Saved *MySQLRowSnapshot
	GeneratedID     int64
	PreviewColumns  []string
	Previews        []MySQLDataCell // saved server values in ColumnMetadata order, bounded like page previews
}
type MySQLDataEditResult struct {
	Outcomes        []MySQLDataEditOutcome
	CommitUncertain bool
}
type MySQLDataVerification struct {
	Status string // applied | not_applied | indeterminate
	States []string
}

func MySQLDataColumnGenerated(col MySQLStructureColumn) bool {
	extra := strings.ToUpper(col.Extra)
	return strings.Contains(extra, "VIRTUAL GENERATED") || strings.Contains(extra, "STORED GENERATED")
}
func mysqlEditMetadata(columns []MySQLStructureColumn) string {
	raw, _ := json.Marshal(columns)
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}
func mysqlSnapshotFromRaw(ref MySQLRowRef, columns []MySQLStructureColumn, raw []any) *MySQLRowSnapshot {
	s := &MySQLRowSnapshot{Ref: ref, Metadata: mysqlEditMetadata(columns)}
	for i, col := range columns {
		s.Columns = append(s.Columns, col.Name)
		var fingerprint *string
		if raw[i] != nil {
			value := string(mysqlEditRawBytes(raw[i]))
			fingerprint = &value
		}
		s.Fingerprints = append(s.Fingerprints, fingerprint)
	}
	return s
}
func mysqlEditRawBytes(v any) []byte {
	switch value := v.(type) {
	case []byte:
		return bytes.Clone(value)
	case string:
		return []byte(value)
	default:
		return []byte(fmt.Sprint(v))
	}
}

type mysqlEditQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (m *MySQLSessionManager) mysqlEditSession(ctx context.Context, id int64) (*mysqlManagedSession, context.Context, context.CancelFunc, error) {
	m.mu.Lock()
	session := m.sessions[id]
	m.mu.Unlock()
	if session == nil {
		return nil, nil, nil, ErrMySQLNotConnected
	}
	qctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	stop := context.AfterFunc(session.ctx, cancel)
	return session, qctx, func() { stop(); cancel() }, nil
}
func mysqlEditTable(database, table string) (string, error) {
	db, e := quotedMySQLIdentifier(database)
	if e != nil {
		return "", e
	}
	tbl, e := quotedMySQLIdentifier(table)
	if e != nil {
		return "", e
	}
	return db + "." + tbl, nil
}
func mysqlEditWhere(ref MySQLRowRef, database, table string) (string, []any, error) {
	if ref.Database != database || ref.Table != table || len(ref.Columns) == 0 || len(ref.Columns) > 16 || len(ref.Columns) != len(ref.Values) {
		return "", nil, ErrMySQLDataReadOnly
	}
	parts := make([]string, len(ref.Columns))
	args := make([]any, len(parts))
	seen := map[string]bool{}
	for i, name := range ref.Columns {
		q, e := quotedMySQLIdentifier(name)
		if e != nil {
			return "", nil, e
		}
		if seen[name] || ref.Values[i] == nil || len(ref.Values[i]) > 4096 {
			return "", nil, ErrMySQLDataReadOnly
		}
		seen[name] = true
		parts[i] = q + " = ?"
		args[i] = ref.Values[i]
	}
	return strings.Join(parts, " AND "), args, nil
}
func mysqlEditUniqueKeys(ctx context.Context, q mysqlEditQueryer, database, table string) ([][]string, error) {
	rows, e := q.QueryContext(ctx, `SELECT s.INDEX_NAME,s.COLUMN_NAME,s.SUB_PART,c.IS_NULLABLE
 FROM information_schema.STATISTICS s LEFT JOIN information_schema.COLUMNS c
 ON c.TABLE_SCHEMA=s.TABLE_SCHEMA AND c.TABLE_NAME=s.TABLE_NAME AND c.COLUMN_NAME=s.COLUMN_NAME
 WHERE s.TABLE_SCHEMA=? AND s.TABLE_NAME=? AND s.NON_UNIQUE=0 ORDER BY s.INDEX_NAME,s.SEQ_IN_INDEX`, database, table)
	if e != nil {
		return nil, e
	}
	type candidate struct {
		names   []string
		invalid bool
	}
	all := map[string]*candidate{}
	for rows.Next() {
		var index string
		var name, nullable *string
		var prefix *int64
		if e = rows.Scan(&index, &name, &prefix, &nullable); e != nil {
			break
		}
		c := all[index]
		if c == nil {
			c = &candidate{}
			all[index] = c
		}
		if name == nil || nullable == nil || *nullable != "NO" || prefix != nil {
			c.invalid = true
		} else {
			c.names = append(c.names, *name)
		}
	}
	if e == nil {
		e = rows.Err()
	}
	ce := rows.Close()
	if e != nil {
		return nil, e
	}
	if ce != nil {
		return nil, ce
	}
	var keys [][]string
	for _, c := range all {
		if !c.invalid && len(c.names) > 0 {
			keys = append(keys, c.names)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return strings.Join(keys[i], "\x00") < strings.Join(keys[j], "\x00") })
	return keys, nil
}
func mysqlEditValidateRef(ctx context.Context, q mysqlEditQueryer, database, table string, ref MySQLRowRef) error {
	if _, _, e := mysqlEditWhere(ref, database, table); e != nil {
		return e
	}
	keys, e := mysqlEditUniqueKeys(ctx, q, database, table)
	if e != nil {
		return e
	}
	for _, names := range keys {
		if reflect.DeepEqual(names, ref.Columns) {
			return nil
		}
	}
	return ErrMySQLDataReadOnly
}

func mysqlEditSnapshot(ctx context.Context, q mysqlEditQueryer, database, table string, ref MySQLRowRef, columns []MySQLStructureColumn, lock bool) (*MySQLRowSnapshot, error) {
	target, e := mysqlEditTable(database, table)
	if e != nil {
		return nil, e
	}
	where, args, e := mysqlEditWhere(ref, database, table)
	if e != nil {
		return nil, e
	}
	fields := make([]string, len(columns))
	for i, col := range columns {
		quoted, _ := quotedMySQLIdentifier(col.Name)
		fields[i] = "SHA2(CAST(" + quoted + " AS BINARY),256)"
	}
	query := "SELECT " + strings.Join(fields, ",") + " FROM " + target + " WHERE " + where + " LIMIT 2"
	if lock {
		query += " FOR UPDATE"
	}
	rows, e := q.QueryContext(ctx, query, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	if !rows.Next() {
		if e = rows.Err(); e != nil {
			return nil, e
		}
		return nil, sql.ErrNoRows
	}
	raw := make([]any, len(columns))
	dest := make([]any, len(raw))
	for i := range raw {
		dest[i] = &raw[i]
	}
	if e = rows.Scan(dest...); e != nil {
		return nil, e
	}
	if rows.Next() {
		return nil, ErrMySQLDataReadOnly
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	return mysqlSnapshotFromRaw(ref, columns, raw), nil
}

// Read saved values without turning a long/blob field into an unbounded transfer.
func mysqlEditPreviews(ctx context.Context, q mysqlEditQueryer, database, table string, ref MySQLRowRef, columns []MySQLStructureColumn) ([]MySQLDataCell, error) {
	target, e := mysqlEditTable(database, table)
	if e != nil {
		return nil, e
	}
	where, args, e := mysqlEditWhere(ref, database, table)
	if e != nil {
		return nil, e
	}
	fields := make([]string, len(columns))
	for i, col := range columns {
		quoted, _ := quotedMySQLIdentifier(col.Name)
		switch {
		case mysqlDataBinaryType(strings.ToLower(col.DataType)):
			fields[i] = "SUBSTRING(" + quoted + ",1,1025)"
		case mysqlDataTextType(strings.ToLower(col.DataType)):
			fields[i] = "SUBSTRING(" + quoted + ",1,2049)"
		default:
			fields[i] = quoted
		}
	}
	rows, e := q.QueryContext(ctx, "SELECT "+strings.Join(fields, ",")+" FROM "+target+" WHERE "+where+" LIMIT 2", args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	if !rows.Next() {
		if e = rows.Err(); e != nil {
			return nil, e
		}
		return nil, sql.ErrNoRows
	}
	raw := make([]any, len(columns))
	dest := make([]any, len(raw))
	for i := range raw {
		dest[i] = &raw[i]
	}
	if e = rows.Scan(dest...); e != nil {
		return nil, e
	}
	if rows.Next() {
		return nil, ErrMySQLDataReadOnly
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	previews := make([]MySQLDataCell, len(columns))
	for i, col := range columns {
		previews[i] = mysqlDataPreviewCell(raw[i], col.DataType)
	}
	return previews, nil
}

func mysqlSnapshotsEqual(a, b *MySQLRowSnapshot) bool {
	return a != nil && b != nil && a.Metadata == b.Metadata && reflect.DeepEqual(a.Columns, b.Columns) && reflect.DeepEqual(a.Fingerprints, b.Fingerprints)
}

// A submitted key can compare equal to its stored value while using different
// bytes (for example, 00042 becomes 42 and DECIMAL gains its declared scale).
// Return the server spelling so subsequent edits and SQL clipboard predicates
// use the same identity as a freshly loaded page. Bound every key independently
// rather than relying on the shorter display preview.
func mysqlEditSavedRef(ctx context.Context, q mysqlEditQueryer, database, table string, ref MySQLRowRef) (*MySQLRowRef, error) {
	target, e := mysqlEditTable(database, table)
	if e != nil {
		return nil, e
	}
	where, args, e := mysqlEditWhere(ref, database, table)
	if e != nil {
		return nil, e
	}
	fields := make([]string, len(ref.Columns))
	for i, name := range ref.Columns {
		quoted, _ := quotedMySQLIdentifier(name)
		fields[i] = "SUBSTRING(CAST(" + quoted + " AS BINARY),1,4097)"
	}
	rows, e := q.QueryContext(ctx, "SELECT "+strings.Join(fields, ",")+" FROM "+target+" WHERE "+where+" LIMIT 2", args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	if !rows.Next() {
		if e = rows.Err(); e != nil {
			return nil, e
		}
		return nil, sql.ErrNoRows
	}
	next := &MySQLRowRef{Database: database, Table: table, Columns: append([]string(nil), ref.Columns...), Values: make([][]byte, len(ref.Columns))}
	dest := make([]any, len(next.Values))
	for i := range dest {
		dest[i] = &next.Values[i]
	}
	if e = rows.Scan(dest...); e != nil {
		return nil, e
	}
	if rows.Next() {
		return nil, ErrMySQLDataReadOnly
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	if _, _, e = mysqlEditWhere(*next, database, table); e != nil {
		return nil, e
	}
	return next, nil
}

// Explicit full read uses a transaction to keep the length check, bytes and
// fingerprints consistent. No ordinary preview can be submitted as a full value.
func (m *MySQLSessionManager) ReadMySQLDataRow(ctx context.Context, id int64, database, table string, ref MySQLRowRef) (*MySQLDataRow, error) {
	session, qctx, cancel, e := m.mysqlEditSession(ctx, id)
	if e != nil {
		return nil, e
	}
	defer cancel()
	target, e := mysqlEditTable(database, table)
	if e != nil {
		return nil, e
	}
	conn, e := session.db.Conn(qctx)
	if e != nil {
		return nil, e
	}
	defer conn.Close()
	columns, e := readMySQLColumnMetadata(qctx, conn, database, table, 128)
	if e != nil {
		return nil, e
	}
	if len(columns) == 0 {
		return nil, ErrMySQLTableNotFound
	}
	if e = mysqlEditValidateRef(qctx, conn, database, table, ref); e != nil {
		return nil, e
	}
	tx, e := conn.BeginTx(qctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	where, args, e := mysqlEditWhere(ref, database, table)
	if e != nil {
		return nil, e
	}
	names := make([]string, len(columns))
	lengths := make([]string, len(columns))
	for i, col := range columns {
		names[i], _ = quotedMySQLIdentifier(col.Name)
		lengths[i] = "COALESCE(OCTET_LENGTH(CAST(" + names[i] + " AS BINARY)),0)"
	}
	var size int64
	if e = tx.QueryRowContext(qctx, "SELECT "+strings.Join(lengths, " + ")+" FROM "+target+" WHERE "+where, args...).Scan(&size); e != nil {
		return nil, e
	}
	if size > MySQLDataEditMaxBytes {
		return nil, ErrMySQLDataEditLimit
	}
	rows, e := tx.QueryContext(qctx, "SELECT "+strings.Join(names, ",")+" FROM "+target+" WHERE "+where+" LIMIT 2", args...)
	if e != nil {
		return nil, e
	}
	raw := make([][]byte, len(columns))
	dest := make([]any, len(raw))
	for i := range raw {
		dest[i] = &raw[i]
	}
	if !rows.Next() {
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
		return nil, sql.ErrNoRows
	}
	e = rows.Scan(dest...)
	if rows.Next() {
		e = ErrMySQLDataReadOnly
	}
	if e == nil {
		e = rows.Err()
	}
	ce := rows.Close()
	if e != nil {
		return nil, e
	}
	if ce != nil {
		return nil, ce
	}
	result := &MySQLDataRow{Columns: columns, Values: map[string]MySQLDataValue{}}
	for i, col := range columns {
		v := MySQLDataValue{Null: raw[i] == nil, Binary: mysqlDataBinaryType(strings.ToLower(col.DataType))}
		if v.Binary {
			v.Bytes = bytes.Clone(raw[i])
		} else {
			if !utf8.Valid(raw[i]) {
				return nil, ErrMySQLDataPage
			}
			v.Text = string(raw[i])
		}
		result.Values[col.Name] = v
	}
	result.Snapshot, e = mysqlEditSnapshot(qctx, tx, database, table, ref, columns, false)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return result, nil
}

func mysqlEditAssignments(columns []MySQLStructureColumn, values map[string]MySQLDataValue, insert bool) ([]string, []string, []any, error) {
	allowed := map[string]MySQLStructureColumn{}
	for _, col := range columns {
		allowed[col.Name] = col
	}
	keys := make([]string, 0, len(values))
	for name := range values {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	names := make([]string, 0, len(keys))
	expressions := make([]string, 0, len(keys))
	var args []any
	total := 0
	for _, name := range keys {
		col, ok := allowed[name]
		if !ok || MySQLDataColumnGenerated(col) {
			return nil, nil, nil, ErrMySQLDataPage
		}
		v := values[name]
		if (v.Null && v.Default) || (v.Binary && v.Text != "") || (!v.Binary && len(v.Bytes) > 0) {
			return nil, nil, nil, ErrMySQLDataPage
		}
		if v.Null && !col.Nullable {
			return nil, nil, nil, ErrMySQLDataPage
		}
		if (v.Null || v.Default) && (v.Text != "" || len(v.Bytes) > 0) {
			return nil, nil, nil, ErrMySQLDataPage
		}
		total += len(v.Text) + len(v.Bytes)
		if total > MySQLDataEditMaxBytes {
			return nil, nil, nil, ErrMySQLDataEditLimit
		}
		if !v.Binary && !utf8.ValidString(v.Text) {
			return nil, nil, nil, ErrMySQLDataPage
		}
		q, _ := quotedMySQLIdentifier(name)
		names = append(names, q)
		switch {
		case v.Default:
			expressions = append(expressions, "DEFAULT")
		case v.Null:
			expressions = append(expressions, "?")
			args = append(args, nil)
		case v.Binary:
			expressions = append(expressions, "?")
			args = append(args, v.Bytes)
		default:
			expressions = append(expressions, "?")
			args = append(args, v.Text)
		}
	}
	if !insert && len(keys) == 0 {
		return nil, nil, nil, ErrMySQLDataPage
	}
	return names, expressions, args, nil
}
func mysqlEditChangedRef(ref MySQLRowRef, values map[string]MySQLDataValue) (MySQLRowRef, error) {
	next := MySQLRowRef{Database: ref.Database, Table: ref.Table, Columns: append([]string(nil), ref.Columns...), Values: make([][]byte, len(ref.Values))}
	for i, name := range ref.Columns {
		next.Values[i] = bytes.Clone(ref.Values[i])
		if v, ok := values[name]; ok {
			if v.Default || v.Null {
				return next, ErrMySQLDataPage
			}
			if v.Binary {
				next.Values[i] = bytes.Clone(v.Bytes)
			} else {
				next.Values[i] = []byte(v.Text)
			}
			if len(next.Values[i]) > 4096 {
				return next, ErrMySQLDataPage
			}
		}
	}
	return next, nil
}

// The entire batch uses a dedicated managed metadata-pool connection and one
// InnoDB transaction. sqlDB and pinned SQL-tab leases are never touched.
func (m *MySQLSessionManager) ApplyMySQLDataEdits(ctx context.Context, id int64, database, table string, edits []MySQLDataEdit) (*MySQLDataEditResult, error) {
	if len(edits) == 0 || len(edits) > MySQLDataMaxPageSize {
		return nil, ErrMySQLDataPage
	}
	session, qctx, cancel, e := m.mysqlEditSession(ctx, id)
	if e != nil {
		return nil, e
	}
	defer cancel()
	target, e := mysqlEditTable(database, table)
	if e != nil {
		return nil, e
	}
	conn, e := session.db.Conn(qctx)
	if e != nil {
		return nil, e
	}
	defer conn.Close()
	// Metadata-pool connections never inherit SQL-tab session options. Enforce
	// strict coercion checks temporarily and restore before returning to pool.
	var originalMode string
	if e = conn.QueryRowContext(qctx, "SELECT @@SESSION.sql_mode").Scan(&originalMode); e != nil {
		return nil, e
	}
	if _, e = conn.ExecContext(qctx, "SET SESSION sql_mode = ?", originalMode+",STRICT_ALL_TABLES"); e != nil {
		return nil, e
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cleanupCancel()
		if _, restoreErr := conn.ExecContext(cleanupCtx, "SET SESSION sql_mode = ?", originalMode); restoreErr != nil {
			// Mark the connection bad instead of returning altered session state.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()

	tx, e := conn.BeginTx(qctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	// Open the target first to hold its metadata lock until COMMIT/ROLLBACK.
	// Revalidate the engine and fields under that lock so a concurrent ALTER
	// cannot swap in a nontransactional engine after our safety check.
	probe, e := tx.QueryContext(qctx, "SELECT 1 FROM "+target+" LIMIT 0")
	if e != nil {
		return nil, e
	}
	if e = probe.Close(); e != nil {
		return nil, e
	}
	var engine, kind string
	if e = tx.QueryRowContext(qctx, `SELECT COALESCE(ENGINE,''),TABLE_TYPE FROM information_schema.TABLES WHERE TABLE_SCHEMA=? AND TABLE_NAME=?`, database, table).Scan(&engine, &kind); e != nil {
		return nil, e
	}
	if !strings.EqualFold(engine, "InnoDB") || kind != "BASE TABLE" {
		return nil, ErrMySQLDataReadOnly
	}
	columns, e := readMySQLColumnMetadata(qctx, tx, database, table, 128)
	if e != nil {
		return nil, e
	}
	if len(columns) == 0 {
		return nil, ErrMySQLTableNotFound
	}

	result := &MySQLDataEditResult{}
	for _, edit := range edits {
		outcome := MySQLDataEditOutcome{Kind: edit.Kind, Original: edit.Original}
		var where string
		var whereArgs []any
		if edit.Kind != "insert" {
			if edit.Original == nil {
				return nil, ErrMySQLDataReadOnly
			}
			if e = mysqlEditValidateRef(qctx, tx, database, table, edit.Original.Ref); e != nil {
				return nil, e
			}
			current, e := mysqlEditSnapshot(qctx, tx, database, table, edit.Original.Ref, columns, true)
			if errors.Is(e, sql.ErrNoRows) {
				return nil, ErrMySQLDataConflict
			}
			if e != nil {
				return nil, e
			}
			if !mysqlSnapshotsEqual(current, edit.Original) {
				return nil, ErrMySQLDataConflict
			}
			where, whereArgs, e = mysqlEditWhere(edit.Original.Ref, database, table)
			if e != nil {
				return nil, e
			}
		}
		var query string
		var args []any
		var nextRef *MySQLRowRef
		switch edit.Kind {
		case "insert", "update":
			names, expressions, bound, e := mysqlEditAssignments(columns, edit.Values, edit.Kind == "insert")
			if e != nil {
				return nil, e
			}
			args = bound
			if edit.Kind == "insert" {
				query = "INSERT INTO " + target + " (" + strings.Join(names, ",") + ") VALUES (" + strings.Join(expressions, ",") + ")"
			} else {
				assignments := make([]string, len(names))
				for i := range names {
					assignments[i] = names[i] + " = " + expressions[i]
				}
				query = "UPDATE " + target + " SET " + strings.Join(assignments, ",") + " WHERE " + where
				args = append(args, whereArgs...)
				nr, e := mysqlEditChangedRef(edit.Original.Ref, edit.Values)
				if e != nil {
					return nil, e
				}
				nextRef = &nr
			}
		case "delete":
			if len(edit.Values) != 0 {
				return nil, ErrMySQLDataPage
			}
			query = "DELETE FROM " + target + " WHERE " + where
			args = whereArgs
		default:
			return nil, ErrMySQLDataPage
		}
		applied, e := tx.ExecContext(qctx, query, args...)
		if e != nil {
			return nil, e
		}
		warnings, warningErr := tx.QueryContext(qctx, "SHOW WARNINGS LIMIT 1")
		if warningErr != nil {
			return nil, warningErr
		}
		if warnings.Next() {
			var level, message string
			var code int
			warningErr = warnings.Scan(&level, &code, &message)
			warnings.Close()
			if warningErr != nil {
				return nil, warningErr
			}
			return nil, fmt.Errorf("%w: server rejected exact value preservation (%d)", ErrMySQLDataPage, code)
		}
		warningErr = warnings.Err()
		warnings.Close()
		if warningErr != nil {
			return nil, warningErr
		}

		if edit.Kind == "insert" {
			outcome.GeneratedID, e = applied.LastInsertId()
			if e != nil {
				return nil, e
			}
			// Recover an inserted identity from a full NOT NULL primary/unique key.
			candidates, keyErr := mysqlEditUniqueKeys(qctx, tx, database, table)
			if keyErr != nil {
				return nil, keyErr
			}
			autoNames := map[string]bool{}
			for _, col := range columns {
				if strings.Contains(strings.ToLower(col.Extra), "auto_increment") {
					autoNames[col.Name] = true
				}
			}

			for _, names := range candidates {
				ref := MySQLRowRef{Database: database, Table: table, Columns: names}
				valid := true
				for _, name := range names {
					v, ok := edit.Values[name]
					if ok && !v.Default && !v.Null {
						if v.Binary {
							ref.Values = append(ref.Values, bytes.Clone(v.Bytes))
						} else {
							ref.Values = append(ref.Values, []byte(v.Text))
						}
					} else if outcome.GeneratedID > 0 && autoNames[name] {
						ref.Values = append(ref.Values, []byte(fmt.Sprint(outcome.GeneratedID)))
					} else {
						valid = false
					}
				}
				if valid && mysqlEditValidateRef(qctx, tx, database, table, ref) == nil {
					nextRef = &ref
					break
				}
			}
		}
		if nextRef != nil {
			if edit.Kind == "insert" || !reflect.DeepEqual(nextRef, &edit.Original.Ref) {
				nextRef, e = mysqlEditSavedRef(qctx, tx, database, table, *nextRef)
				if e != nil {
					return nil, e
				}
			}
			// Values such as decimal spellings are normalized by MySQL; reading the
			// committed candidate, rather than hashing user text, preserves semantics.
			outcome.Saved, e = mysqlEditSnapshot(qctx, tx, database, table, *nextRef, columns, false)
			if e != nil {
				return nil, e
			}
			outcome.PreviewColumns = make([]string, len(columns))
			for i, col := range columns {
				outcome.PreviewColumns[i] = col.Name
			}
			outcome.Previews, e = mysqlEditPreviews(qctx, tx, database, table, *nextRef, columns)
			if e != nil {
				return nil, e
			}
		}
		result.Outcomes = append(result.Outcomes, outcome)
	}
	if e = tx.Commit(); e != nil {
		result.CommitUncertain = true
		return result, fmt.Errorf("%w: %v", ErrMySQLDataCommitUncertain, e)
	}
	return result, nil
}

// Verification performs reads only. A partially matching or unidentifiable
// insert stays indeterminate; the caller must never replay it automatically.
func (m *MySQLSessionManager) VerifyMySQLDataEdits(ctx context.Context, id int64, database, table string, result *MySQLDataEditResult) (*MySQLDataVerification, error) {
	if result == nil || len(result.Outcomes) == 0 {
		return nil, ErrMySQLDataPage
	}
	session, qctx, cancel, e := m.mysqlEditSession(ctx, id)
	if e != nil {
		return nil, e
	}
	defer cancel()
	columns, e := readMySQLColumnMetadata(qctx, session.db, database, table, 128)
	if e != nil {
		return nil, e
	}
	verification := &MySQLDataVerification{Status: "indeterminate"}
	for _, outcome := range result.Outcomes {
		state := "indeterminate"
		candidate := outcome.Saved
		if candidate == nil {
			candidate = outcome.Original
		}
		if candidate != nil {
			if e = mysqlEditValidateRef(qctx, session.db, database, table, candidate.Ref); e != nil {
				return nil, e
			}
			current, e := mysqlEditSnapshot(qctx, session.db, database, table, candidate.Ref, columns, false)
			switch {
			case errors.Is(e, sql.ErrNoRows):
				if outcome.Kind == "delete" {
					state = "applied"
				} else if outcome.Kind == "insert" {
					state = "not_applied"
				}
			case e != nil:
				return nil, e
			case outcome.Kind == "delete":
				if mysqlSnapshotsEqual(current, outcome.Original) {
					state = "not_applied"
				}
			case mysqlSnapshotsEqual(current, outcome.Saved):
				state = "applied"
			case mysqlSnapshotsEqual(current, outcome.Original):
				state = "not_applied"
			}
			// Updating a unique key may leave no row under the candidate key after a
			// rollback. Check the original key before declaring the outcome unknown.
			if state == "indeterminate" && outcome.Kind == "update" && outcome.Original != nil {
				original, e := mysqlEditSnapshot(qctx, session.db, database, table, outcome.Original.Ref, columns, false)
				if e != nil && !errors.Is(e, sql.ErrNoRows) {
					return nil, e
				}
				if mysqlSnapshotsEqual(original, outcome.Original) {
					state = "not_applied"
				}
			}
		}
		verification.States = append(verification.States, state)
	}
	first := verification.States[0]
	same := true
	for _, state := range verification.States {
		if state != first {
			same = false
		}
	}
	if same {
		verification.Status = first
	}
	return verification, nil
}

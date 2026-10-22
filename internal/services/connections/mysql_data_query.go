package connections

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MySQLDataOptions controls one server-side, read-only page. Column names are
// checked against server metadata before they appear in SQL; values are bound.
type MySQLDataOptions struct {
	Offset         int64
	Limit          int
	FilterColumn   string
	FilterValue    string
	FilterMode     string // "" | "equal" | "contains"
	SortColumn     string
	SortDescending bool
}

// TableDataPageWithOptions reads bounded projections rather than SELECT *.
// Every text and binary column is truncated ON THE SERVER, before transfer.
// The legacy TableDataPage signature remains for callers without options.
func (m *MySQLSessionManager) TableDataPageWithOptions(ctx context.Context, id int64, database, table string, o MySQLDataOptions) (*MySQLDataPage, error) {
	dbName, err := quotedMySQLIdentifier(database)
	if err != nil {
		return nil, err
	}
	tableName, err := quotedMySQLIdentifier(table)
	if err != nil {
		return nil, err
	}
	if o.Offset < 0 || o.Offset > 1_000_000_000 || o.Limit < 1 ||
		o.Limit > MySQLDataMaxPageSize || len(o.FilterValue) > 4096 ||
		(o.FilterMode != "" && o.FilterMode != "equal" && o.FilterMode != "contains") ||
		(o.FilterMode != "" && o.FilterColumn == "") ||
		(o.FilterMode == "" && o.FilterColumn != "") ||
		(o.SortColumn == "" && o.SortDescending) {
		return nil, ErrMySQLDataPage
	}
	m.mu.Lock()
	session := m.sessions[id]
	m.mu.Unlock()
	if session == nil {
		return nil, ErrMySQLNotConnected
	}
	queryCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	stop := context.AfterFunc(session.ctx, cancel)
	defer func() { stop(); cancel() }()

	const metadataSQL = `SELECT COLUMN_NAME, DATA_TYPE, IS_NULLABLE FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION`
	metaRows, err := session.db.QueryContext(queryCtx, metadataSQL, database, table)
	if err != nil {
		return nil, err
	}
	type columnMeta struct {
		name, kind string
		nullable   bool
	}
	var columns []columnMeta
	for metaRows.Next() {
		var name, kind, nullable string
		if err = metaRows.Scan(&name, &kind, &nullable); err != nil {
			break
		}
		if len(columns) >= 128 {
			err = ErrMySQLMetadataLimit
			break
		}
		if _, err = quotedMySQLIdentifier(name); err != nil {
			break
		}
		columns = append(columns, columnMeta{name, strings.ToLower(kind), nullable == "YES"})
	}
	if err == nil {
		err = metaRows.Err()
	}
	closeErr := metaRows.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(columns) == 0 {
		return nil, ErrMySQLTableNotFound
	}
	allowed := make(map[string]columnMeta, len(columns))
	fields := make([]string, 0, len(columns))
	names := make([]string, 0, len(columns))
	for _, col := range columns {
		quoted, _ := quotedMySQLIdentifier(col.name)
		allowed[col.name] = col
		names = append(names, col.name)
		switch {
		case mysqlDataBinaryType(col.kind):
			fields = append(fields, "SUBSTRING("+quoted+", 1, 1025) AS "+quoted)
		case mysqlDataTextType(col.kind):
			fields = append(fields, "SUBSTRING("+quoted+", 1, 2049) AS "+quoted)
		default:
			fields = append(fields, quoted)
		}
	}
	// A full-length, non-null UNIQUE index is a deterministic fallback for a
	// table with no PK. Prefix indexes and nullable/expression indexes do not
	// uniquely identify rows for a repeatable sort.
	const uniqueSQL = `SELECT s.INDEX_NAME, s.COLUMN_NAME, s.SEQ_IN_INDEX, s.SUB_PART,
 c.IS_NULLABLE FROM information_schema.STATISTICS s
 LEFT JOIN information_schema.COLUMNS c ON c.TABLE_SCHEMA=s.TABLE_SCHEMA
 AND c.TABLE_NAME=s.TABLE_NAME AND c.COLUMN_NAME=s.COLUMN_NAME
 WHERE s.TABLE_SCHEMA=? AND s.TABLE_NAME=? AND s.NON_UNIQUE=0
 ORDER BY (s.INDEX_NAME <> 'PRIMARY'), s.INDEX_NAME, s.SEQ_IN_INDEX`
	indexRows, err := session.db.QueryContext(queryCtx, uniqueSQL, database, table)
	if err != nil {
		return nil, err
	}
	type uniqueCandidate struct {
		parts []string
		valid bool
	}
	candidates := make(map[string]*uniqueCandidate)
	var idxNames []string
	for indexRows.Next() {
		var idx string
		var name, nullable *string
		var seq int64
		var sub *int64
		if err = indexRows.Scan(&idx, &name, &seq, &sub, &nullable); err != nil {
			break
		}
		c := candidates[idx]
		if c == nil {
			c = &uniqueCandidate{valid: true}
			candidates[idx] = c
			idxNames = append(idxNames, idx)
		}
		if name == nil || nullable == nil || *nullable != "NO" || sub != nil {
			c.valid = false
			continue
		}
		quoted, quoteErr := quotedMySQLIdentifier(*name)
		if quoteErr != nil {
			err = quoteErr
			break
		}
		if _, ok := allowed[*name]; !ok {
			c.valid = false
			continue
		}
		c.parts = append(c.parts, quoted)
	}
	if err == nil {
		err = indexRows.Err()
	}
	closeErr = indexRows.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	var identity []string
	for _, idx := range idxNames {
		if c := candidates[idx]; c.valid && len(c.parts) > 0 {
			identity = c.parts
			break
		}
	}

	query := "SELECT " + strings.Join(fields, ", ") + " FROM " + dbName + "." + tableName
	var args []any
	if o.FilterMode != "" {
		col, ok := allowed[o.FilterColumn]
		if !ok {
			return nil, ErrMySQLDataPage
		}
		quoted, _ := quotedMySQLIdentifier(col.name)
		if o.FilterMode == "contains" {
			if !mysqlDataTextType(col.kind) {
				return nil, ErrMySQLDataPage
			}
			query += " WHERE LOCATE(?, " + quoted + ") > 0"
		} else {
			query += " WHERE " + quoted + " = ?"
		}
		args = append(args, o.FilterValue)
	}
	var sortParts []string
	if o.SortColumn != "" {
		col, ok := allowed[o.SortColumn]
		if !ok || mysqlDataBinaryType(col.kind) || col.kind == "json" ||
			strings.Contains(col.kind, "text") {
			return nil, ErrMySQLDataPage
		}
		quoted, _ := quotedMySQLIdentifier(col.name)
		if o.SortDescending {
			sortParts = append(sortParts, quoted+" DESC")
		} else {
			sortParts = append(sortParts, quoted+" ASC")
		}
	}
	for _, k := range identity {
		if len(sortParts) == 1 && (sortParts[0] == k+" ASC" || sortParts[0] == k+" DESC") {
			continue
		}
		sortParts = append(sortParts, k+" ASC")
	}
	if len(sortParts) > 0 {
		query += " ORDER BY " + strings.Join(sortParts, ", ")
	}
	query += " LIMIT ? OFFSET ?"
	args = append(args, o.Limit+1, o.Offset)
	rows, err := session.db.QueryContext(queryCtx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// Scan and render in verified INFORMATION_SCHEMA order, even with a
	// server-side projection; do not depend on ambiguous ColumnTypes aliases.
	page := &MySQLDataPage{Database: database, Table: table, Columns: names,
		Offset: o.Offset, Limit: o.Limit, StableOrder: len(identity) > 0}
	dest := make([]any, len(columns))
	raw := make([]any, len(columns))
	for i := range dest {
		dest[i] = &raw[i]
	}
	for rows.Next() {
		if len(page.Rows) == o.Limit {
			page.HasMore = true
			break
		}
		if err = rows.Scan(dest...); err != nil {
			return nil, err
		}
		line := make([]MySQLDataCell, len(columns))
		for i, col := range columns {
			line[i] = mysqlDataPreviewCell(raw[i], col.kind)
		}
		page.Rows = append(page.Rows, line)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = queryCtx.Err(); err != nil {
		return nil, err
	}
	if session.ctx.Err() != nil {
		return nil, context.Canceled
	}
	return page, nil
}

func mysqlDataBinaryType(kind string) bool {
	switch kind {
	case "binary", "varbinary", "tinyblob", "blob", "mediumblob", "longblob", "bit", "geometry",
		"point", "linestring", "polygon", "multipoint", "multilinestring", "multipolygon", "geometrycollection":
		return true
	}
	return false
}
func mysqlDataTextType(kind string) bool {
	switch kind {
	case "char", "varchar", "tinytext", "text", "mediumtext", "longtext", "json", "enum", "set":
		return true
	}
	return false
}

// Validate option columns without exposing arbitrary SQL. Used for UX error
// descriptions and intentionally does not silently fall back to unfiltered SQL.
func validateMySQLDataOptions(o MySQLDataOptions) error {
	if o.FilterMode != "equal" && o.FilterMode != "contains" && o.FilterMode != "" {
		return fmt.Errorf("%w: unsupported filter", ErrMySQLDataPage)
	}
	if o.FilterMode != "" && o.FilterColumn == "" {
		return errors.Join(ErrMySQLDataPage, fmt.Errorf("missing filter column"))
	}
	return nil
}

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
	UseKeyset      bool
	After          *MySQLDataCursor
	Filters        []MySQLDataFilterRule
	Sorts          []MySQLDataSortRule
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
		(o.SortColumn == "" && o.SortDescending) || (o.After != nil && !o.UseKeyset) ||
		(o.UseKeyset && o.Offset > 0 && o.After == nil) || (o.After != nil && o.Offset == 0) {
		return nil, ErrMySQLDataPage
	}
	filters, sortRules, err := mysqlDataEffectiveRules(o)
	if err != nil {
		return nil, err
	}
	signature := mysqlDataRuleSignature(filters, sortRules)
	m.mu.Lock()
	session := m.sessions[id]
	m.mu.Unlock()
	if session == nil {
		return nil, ErrMySQLNotConnected
	}
	queryCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	stop := context.AfterFunc(session.ctx, cancel)
	defer func() { stop(); cancel() }()

	columns, err := readMySQLColumnMetadata(queryCtx, session.db, database, table, 128)
	if err != nil {
		return nil, err
	}
	if len(columns) == 0 {
		return nil, ErrMySQLTableNotFound
	}
	allowed := make(map[string]MySQLStructureColumn, len(columns))
	fields := make([]string, 0, len(columns))
	names := make([]string, 0, len(columns))
	kinds := make([]string, 0, len(columns))
	for _, col := range columns {
		quoted, _ := quotedMySQLIdentifier(col.Name)
		allowed[col.Name] = col
		names = append(names, col.Name)
		kinds = append(kinds, strings.ToLower(col.DataType))
		switch {
		case mysqlDataBinaryType(strings.ToLower(col.DataType)):
			fields = append(fields, "SUBSTRING("+quoted+", 1, 1025) AS "+quoted)
		case mysqlDataTextType(strings.ToLower(col.DataType)):
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
		names []string
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
		c.names = append(c.names, *name)
	}
	if err == nil {
		err = indexRows.Err()
	}
	closeErr := indexRows.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	var identity []string
	var identityNames []string
	for _, idx := range idxNames {
		if c := candidates[idx]; c.valid && len(c.parts) > 0 {
			identity = c.parts
			identityNames = c.names
			break
		}
	}

	query := "SELECT " + strings.Join(fields, ", ") + " FROM " + dbName + "." + tableName
	var args []any
	var conditions []string
	for _, filter := range filters {
		col, ok := allowed[filter.Column]
		if !ok {
			return nil, ErrMySQLDataPage
		}
		quoted, _ := quotedMySQLIdentifier(col.Name)
		switch filter.Mode {
		case "contains":
			if !mysqlDataTextType(strings.ToLower(col.DataType)) {
				return nil, ErrMySQLDataPage
			}
			conditions = append(conditions, "LOCATE(?, "+quoted+") > 0")
			args = append(args, filter.Value)
		case "equal", "not_equal":
			op := " = "
			if filter.Mode == "not_equal" {
				op = " <> "
			}
			conditions = append(conditions, quoted+op+"?")
			args = append(args, filter.Value)
		case "is_null":
			conditions = append(conditions, quoted+" IS NULL")
		case "is_not_null":
			conditions = append(conditions, quoted+" IS NOT NULL")
		}
	}
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	var sortParts []string
	var orderKeys []mysqlOrderKey
	keysetSafe := len(identity) > 0
	for _, rule := range sortRules {
		col, ok := allowed[rule.Column]
		kind := strings.ToLower(col.DataType)
		if !ok || mysqlDataBinaryType(kind) || kind == "json" ||
			strings.Contains(kind, "text") {
			return nil, ErrMySQLDataPage
		}
		quoted, _ := quotedMySQLIdentifier(col.Name)
		if col.Nullable {
			keysetSafe = false
		}
		orderKeys = append(orderKeys, mysqlOrderKey{name: col.Name, desc: rule.Descending})
		if rule.Descending {
			sortParts = append(sortParts, quoted+" DESC")
		} else {
			sortParts = append(sortParts, quoted+" ASC")
		}
	}
	for _, k := range identity {
		name := strings.Trim(k, "`")
		name = strings.ReplaceAll(name, "``", "`")
		if containsOrderColumn(orderKeys, name) {
			continue
		}
		orderKeys = append(orderKeys, mysqlOrderKey{name: name})
		sortParts = append(sortParts, k+" ASC")
	}
	seek := o.UseKeyset && keysetSafe && len(orderKeys) > 0
	if o.After != nil {
		if !seek || o.After.Database != database || o.After.Table != table ||
			o.After.FilterColumn != o.FilterColumn || o.After.FilterMode != o.FilterMode ||
			o.After.FilterValue != o.FilterValue || o.After.SortColumn != o.SortColumn ||
			o.After.SortDescending != o.SortDescending || o.After.RuleSignature != signature {
			return nil, ErrMySQLDataPage
		}
		predicate, values, err := mysqlKeysetPredicate(orderKeys, o.After)
		if err != nil {
			return nil, err
		}
		if len(conditions) > 0 {
			query += " AND " + predicate
		} else {
			query += " WHERE " + predicate
		}
		args = append(args, values...)
	}
	if len(sortParts) > 0 {
		query += " ORDER BY " + strings.Join(sortParts, ", ")
	}
	if seek {
		marker := " FROM " + dbName + "." + tableName
		var rawCols []string
		for i, k := range orderKeys {
			q, err := quotedMySQLIdentifier(k.name)
			if err != nil {
				return nil, err
			}
			rawCols = append(rawCols, fmt.Sprintf("%s AS `__nyadb_seek_%d`", q, i))
		}
		query = strings.Replace(query, marker, ", "+strings.Join(rawCols, ", ")+marker, 1)
	}
	query += " LIMIT ? OFFSET ?"
	fetchOffset := o.Offset
	if seek {
		fetchOffset = 0
	}
	args = append(args, o.Limit+1, fetchOffset)
	started := time.Now()
	rows, err := session.db.QueryContext(queryCtx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// Scan and render in verified INFORMATION_SCHEMA order, even with a
	// server-side projection; do not depend on ambiguous ColumnTypes aliases.
	page := &MySQLDataPage{Database: database, Table: table, Columns: names, ColumnKinds: kinds, ColumnMetadata: columns,
		Offset: o.Offset, Limit: o.Limit, StableOrder: len(identity) > 0, KeysetAvailable: seek}
	count := len(columns)
	if seek {
		count += len(orderKeys)
	}
	dest := make([]any, count)
	raw := make([]any, count)
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
			line[i] = mysqlDataPreviewCell(raw[i], col.DataType)
		}
		page.Rows = append(page.Rows, line)
		if seek {
			scope := MySQLDataCursor{Database: database, Table: table, FilterColumn: o.FilterColumn,
				FilterMode: o.FilterMode, FilterValue: o.FilterValue, SortColumn: o.SortColumn, SortDescending: o.SortDescending, RuleSignature: signature}
			next, err := mysqlNextDataCursor(orderKeys, raw[len(columns):], scope)
			if err == nil {
				page.NextCursor = next
				ref := &MySQLRowRef{Database: database, Table: table, Columns: append([]string(nil), identityNames...)}
				for _, keyName := range identityNames {
					for j, k := range orderKeys {
						if k.name == keyName {
							ref.Values = append(ref.Values, append([]byte(nil), next.Values[j]...))
							break
						}
					}
				}
				if len(ref.Values) == len(ref.Columns) {
					page.RowRefs = append(page.RowRefs, ref)
				} else {
					page.RowRefs = append(page.RowRefs, nil)
				}

			} else {
				page.KeysetAvailable = false
				page.NextCursor = nil
			}
		}
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
	page.QuerySQL = query
	page.CompletedAt = time.Now()
	page.Duration = time.Since(started)
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

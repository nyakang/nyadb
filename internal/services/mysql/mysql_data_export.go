package mysql

import (
	"context"
	"fmt"
	"github.com/nyakang/nyadb/internal/db/contract"
	"strings"
	"unicode/utf8"
)

// MySQLDataExportFieldMaxBytes bounds each full field before network transfer.
// Exceeding this bound aborts the export; no truncated file is published.
const MySQLDataExportFieldMaxBytes = 1 << 20
const MySQLDataExportRowMaxBytes = 16 << 20

// ExportMySQLData streams one read-only result from the metadata pool. A zero
// Limit exports every matching row; positive Limit/Offset export a page. It
// never uses grid previews or collects the complete result in memory.
func (m *MySQLSessionManager) ExportMySQLData(ctx context.Context, id int64, database, table string, options MySQLDataOptions, fields []string, yield func([]MySQLDataValue) error) error {
	if yield == nil || len(fields) == 0 || len(fields) > 128 || options.Offset < 0 || options.Limit < 0 || options.Limit > MySQLDataMaxPageSize || options.After != nil || options.UseKeyset {
		return ErrMySQLDataPage
	}
	if (options.FilterMode == "" && options.FilterColumn != "") || (options.SortColumn == "" && options.SortDescending) {
		return ErrMySQLDataPage
	}
	filters, sorts, err := mysqlDataEffectiveRules(options)
	if err != nil {
		return err
	}
	session, qctx, cancel, err := m.requestContext(ctx, id, 0)
	if err != nil {
		return err
	}
	defer cancel()
	columns, err := readMySQLColumnMetadata(qctx, session.db, database, table, 128)
	if err != nil {
		return err
	}
	if len(columns) == 0 {
		return ErrMySQLTableNotFound
	}
	query, args, kinds, err := mysqlDataExportQuery(database, table, columns, fields, filters, sorts, options.Offset, options.Limit, options.Condition)
	if err != nil {
		return err
	}
	rows, err := session.db.QueryContext(qctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	raw := make([][]byte, len(fields))
	dest := make([]any, len(fields))
	for i := range dest {
		dest[i] = &raw[i]
	}
	for rows.Next() {
		if err = qctx.Err(); err != nil {
			return err
		}
		if err = rows.Scan(dest...); err != nil {
			return err
		}
		values := make([]MySQLDataValue, len(fields))
		total := 0
		for i, value := range raw {
			total += len(value)
			if len(value) > MySQLDataExportFieldMaxBytes || total > MySQLDataExportRowMaxBytes {
				return fmt.Errorf("%w: export field or row exceeds full-value limit", ErrMySQLDataEditLimit)
			}
			values[i].Null = value == nil
			if mysqlDataBinaryType(kinds[i]) {
				values[i].Binary = true
				values[i].Bytes = append([]byte(nil), value...)
			} else {
				if !utf8.Valid(value) {
					return fmt.Errorf("%w: exported text is not UTF-8", ErrMySQLDataPage)
				}
				values[i].Text = string(value)
			}
		}
		if err = yield(values); err != nil {
			return err
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if session.ctx.Err() != nil {
		return context.Canceled
	}
	return qctx.Err()
}

func mysqlDataExportQuery(database, table string, columns []MySQLStructureColumn, fields []string, filters []MySQLDataFilterRule, sorts []MySQLDataSortRule, offset int64, limit int, expression ...*contract.FilterExpression[contract.DataFilterRule]) (string, []any, []string, error) {
	db, e := quotedMySQLIdentifier(database)
	if e != nil {
		return "", nil, nil, e
	}
	name, e := quotedMySQLIdentifier(table)
	if e != nil {
		return "", nil, nil, e
	}
	allowed := map[string]MySQLStructureColumn{}
	for _, col := range columns {
		allowed[col.Name] = col
	}
	var projection, kinds []string
	seen := map[string]bool{}
	for _, field := range fields {
		col, ok := allowed[field]
		if !ok || seen[field] {
			return "", nil, nil, ErrMySQLDataPage
		}
		seen[field] = true
		q, quoteErr := quotedMySQLIdentifier(field)
		if quoteErr != nil {
			return "", nil, nil, quoteErr
		}
		projection = append(projection, fmt.Sprintf("SUBSTRING(CAST(%s AS BINARY),1,%d) AS %s", q, MySQLDataExportFieldMaxBytes+1, q))
		kinds = append(kinds, strings.ToLower(col.DataType))
	}
	var expr *contract.FilterExpression[contract.DataFilterRule]
	if len(expression) > 0 {
		expr = expression[0]
	}
	where, args, err := mysqlFilterWhere(columns, filters, expr)
	if err != nil {
		return "", nil, nil, err
	}
	query := "SELECT " + strings.Join(projection, ", ") + " FROM " + db + "." + name
	if where != "" {
		query += " WHERE " + where
	}
	var order []string
	for _, sort := range sorts {
		col, ok := allowed[sort.Column]
		kind := strings.ToLower(col.DataType)
		if !ok || mysqlDataBinaryType(kind) || kind == "json" || strings.Contains(kind, "text") {
			return "", nil, nil, ErrMySQLDataPage
		}
		q, quoteErr := quotedMySQLIdentifier(sort.Column)
		if quoteErr != nil {
			return "", nil, nil, quoteErr
		}
		if sort.Descending {
			q += " DESC"
		} else {
			q += " ASC"
		}
		order = append(order, q)
	}
	if len(order) > 0 {
		query += " ORDER BY " + strings.Join(order, ", ")
	}
	if limit > 0 {
		query += " LIMIT ? OFFSET ?"
		args = append(args, limit, offset)
	} else if offset != 0 {
		return "", nil, nil, ErrMySQLDataPage
	}
	return query, args, kinds, nil
}

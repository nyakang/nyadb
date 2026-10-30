package mysql

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Analysis aggregates remain on the server. Client memory and transferred
// values are bounded by 128 fields and ten 128-character previews per field.
type MySQLDataAnalysis struct {
	Rows        int64
	Columns     []MySQLDataColumnAnalysis
	CompletedAt time.Time
	Duration    time.Duration
}

type MySQLDataColumnAnalysis struct {
	Name, Kind, Collation   string
	Rows, Null, Distinct    int64
	Singletons              int64 // non-NULL value groups occurring exactly once
	Numeric, Zero           int64
	Min, Max, Mean          string
	Median, StdDeviation    string
	MaxFrequency, ModeCount int64
	Modes                   []MySQLDataAnalysisFrequency
	Distribution            []MySQLDataAnalysisFrequency
}

type MySQLDataAnalysisFrequency struct {
	Value     string
	Count     int64
	Truncated bool
	Null      bool
}

// AnalyzeMySQLData analyzes all rows matching the supplied AND filters.
// Sorting/paging/cursors cannot silently restrict an aggregate. A read-only
// repeatable-read transaction gives InnoDB fields one consistent snapshot;
// cancellation is acknowledged by the driver before the result is returned.
func (m *MySQLSessionManager) AnalyzeMySQLData(ctx context.Context, id int64, database, table string, options MySQLDataOptions, fields []string) (*MySQLDataAnalysis, error) {
	if options.Offset != 0 || options.Limit != 0 || options.UseKeyset || options.After != nil {
		return nil, ErrMySQLDataPage
	}
	filters, _, err := mysqlDataEffectiveRules(options)
	if err != nil || len(fields) == 0 || len(fields) > 128 {
		return nil, ErrMySQLDataPage
	}
	session, qctx, cancel, err := m.requestContext(ctx, id, 0)
	if err != nil {
		return nil, err
	}
	defer cancel()
	tx, err := session.db.BeginTx(qctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	columns, err := readMySQLColumnMetadata(qctx, tx, database, table, 128)
	if err != nil {
		return nil, err
	}
	if len(columns) == 0 {
		return nil, ErrMySQLTableNotFound
	}
	result, err := mysqlAnalyzeData(qctx, tx, database, table, columns, fields, filters)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func mysqlDataAnalysisFrom(database, table string, columns []MySQLStructureColumn, filters []MySQLDataFilterRule) (string, []any, error) {
	target, err := mysqlEditTable(database, table)
	if err != nil {
		return "", nil, err
	}
	allowed := make(map[string]MySQLStructureColumn, len(columns))
	for _, col := range columns {
		allowed[col.Name] = col
	}
	if len(filters) > MySQLDataMaxFilterRules {
		return "", nil, ErrMySQLDataPage
	}
	var conditions []string
	var args []any
	for _, rule := range filters {
		col, ok := allowed[rule.Column]
		if !ok || len(rule.Value) > 4096 {
			return "", nil, ErrMySQLDataPage
		}
		q, err := quotedMySQLIdentifier(rule.Column)
		if err != nil {
			return "", nil, err
		}
		switch rule.Mode {
		case "contains", "not_contains", "like", "not_like":
			if !mysqlDataTextType(strings.ToLower(col.DataType)) {
				return "", nil, ErrMySQLDataPage
			}
			switch rule.Mode {
			case "contains":
				conditions = append(conditions, "LOCATE(?, "+q+") > 0")
				args = append(args, rule.Value)
			case "not_contains":
				conditions = append(conditions, "LOCATE(?, "+q+") = 0")
				args = append(args, rule.Value)
			case "like":
				conditions = append(conditions, q+" LIKE ?")
				args = append(args, "%"+rule.Value+"%")
			case "not_like":
				conditions = append(conditions, q+" NOT LIKE ?")
				args = append(args, "%"+rule.Value+"%")
			}
		case "equal", "not_equal", "less", "greater":
			op := map[string]string{"equal": " = ", "not_equal": " <> ", "less": " < ", "greater": " > "}[rule.Mode]
			conditions = append(conditions, q+op+"?")
			args = append(args, rule.Value)
		case "is_null":
			conditions = append(conditions, q+" IS NULL")
		case "is_not_null":
			conditions = append(conditions, q+" IS NOT NULL")
		default:
			return "", nil, ErrMySQLDataPage
		}
	}
	from := " FROM " + target + " WHERE "
	if len(conditions) == 0 {
		return from + "1=1", args, nil
	}
	return from + strings.Join(conditions, " AND "), args, nil
}

func mysqlDataAnalysisQueries(from string, col MySQLStructureColumn) (aggregate, singleton, distribution string, err error) {
	q, err := quotedMySQLIdentifier(col.Name)
	if err != nil {
		return "", "", "", err
	}
	kind := strings.ToLower(col.DataType)
	group := q
	if mysqlDataBinaryType(kind) {
		group = "CAST(" + q + " AS BINARY)"
	} else if kind == "json" {
		group = "CAST(" + q + " AS CHAR CHARACTER SET utf8mb4) COLLATE utf8mb4_bin"
	}
	numeric := "NULL, NULL, NULL, 0"
	if mysqlDataAnalysisNumeric(kind) {
		numeric = "CAST(MIN(" + q + ") AS CHAR), CAST(MAX(" + q + ") AS CHAR), CAST(AVG(" + q + ") AS CHAR), COUNT(CASE WHEN " + q + "=0 THEN 1 END)"
	}
	aggregate = "SELECT COUNT(*), COUNT(*)-COUNT(" + q + "), COUNT(DISTINCT " + group + "), " + numeric + from
	grouped := "SELECT " + group + " AS `v`, COUNT(*) AS `n`" + from + " AND " + q + " IS NOT NULL GROUP BY " + group
	singleton = "SELECT COUNT(*) FROM (" + grouped + " HAVING COUNT(*)=1) AS `__nyadb_once`"
	preview := "SUBSTRING(CAST(`v` AS CHAR),1,128)"
	if mysqlDataBinaryType(kind) {
		preview = "SUBSTRING(`v`,1,128)"
	}
	distribution = "SELECT " + preview + ", `n`, OCTET_LENGTH(`v`)>OCTET_LENGTH(" + preview + ") FROM (" + grouped + ") AS `__nyadb_groups` ORDER BY `n` DESC, `v` ASC LIMIT 10"
	return aggregate, singleton, distribution, nil
}

// Median transfers only the central one or two non-NULL values. LIMIT and
// OFFSET are bound, so even a huge table never becomes a client-side sort.
// Centering exact integers/decimals before STDDEV_POP avoids losing small
// differences near a large baseline when MySQL converts variance to DOUBLE.
func mysqlDataAnalysisNumericQueries(from string, col MySQLStructureColumn) (median, deviation string, centered bool, err error) {
	q, err := quotedMySQLIdentifier(col.Name)
	kind := strings.ToLower(col.DataType)
	if err != nil || !mysqlDataAnalysisNumeric(kind) {
		return "", "", false, ErrMySQLDataPage
	}
	median = "SELECT CAST(AVG(`v`) AS CHAR) FROM (SELECT " + q + " AS `v`" + from + " AND " + q + " IS NOT NULL ORDER BY " + q + " ASC LIMIT ? OFFSET ?) AS `__nyadb_middle`"
	value := q
	switch kind {
	case "float", "double", "real":
	default:
		scale := int64(0)
		if col.NumericScale != nil {
			scale = *col.NumericScale
		}
		if scale < 0 || scale > 30 {
			return "", "", false, ErrMySQLDataPage
		}
		value = q + fmt.Sprintf(" - CAST(? AS DECIMAL(65,%d))", scale)
		centered = true
	}
	deviation = "SELECT CAST(STDDEV_POP(" + value + ") AS CHAR)" + from
	return median, deviation, centered, nil
}

func mysqlDataAnalysisModeQuery(from string, col MySQLStructureColumn) (string, error) {
	q, err := quotedMySQLIdentifier(col.Name)
	if err != nil {
		return "", err
	}
	group := q
	kind := strings.ToLower(col.DataType)
	if mysqlDataBinaryType(kind) {
		group = "CAST(" + q + " AS BINARY)"
	} else if kind == "json" {
		group = "CAST(" + q + " AS CHAR CHARACTER SET utf8mb4) COLLATE utf8mb4_bin"
	}
	return "SELECT COUNT(*) FROM (SELECT " + group + from + " AND " + q + " IS NOT NULL GROUP BY " + group + " HAVING COUNT(*)=?) AS `__nyadb_modes`", nil
}

func mysqlDataAnalysisNumeric(kind string) bool {
	switch kind {
	case "tinyint", "smallint", "mediumint", "int", "integer", "bigint", "decimal", "numeric", "float", "double", "real", "bool", "boolean":
		return true
	}
	return false
}

func mysqlAnalyzeData(ctx context.Context, q mysqlEditQueryer, database, table string, columns []MySQLStructureColumn, fields []string, filters []MySQLDataFilterRule) (*MySQLDataAnalysis, error) {
	from, args, err := mysqlDataAnalysisFrom(database, table, columns, filters)
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]MySQLStructureColumn, len(columns))
	for _, col := range columns {
		allowed[col.Name] = col
	}
	seen := map[string]bool{}
	for _, field := range fields {
		if _, ok := allowed[field]; !ok || seen[field] {
			return nil, ErrMySQLDataPage
		}
		seen[field] = true
	}
	started := time.Now()
	result := &MySQLDataAnalysis{}
	for _, field := range fields {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		col := allowed[field]
		aggregate, singleton, distribution, err := mysqlDataAnalysisQueries(from, col)
		if err != nil {
			return nil, err
		}
		stats := MySQLDataColumnAnalysis{Name: col.Name, Kind: strings.ToLower(col.DataType)}
		if col.Collation != nil {
			stats.Collation = *col.Collation
		} else if mysqlDataBinaryType(stats.Kind) {
			stats.Collation = "binary"
		} else if stats.Kind == "json" {
			stats.Collation = "utf8mb4_bin"
		}
		var minimum, maximum, mean sql.NullString
		if err := q.QueryRowContext(ctx, aggregate, args...).Scan(&stats.Rows, &stats.Null, &stats.Distinct, &minimum, &maximum, &mean, &stats.Zero); err != nil {
			return nil, err
		}
		stats.Min, stats.Max, stats.Mean = minimum.String, maximum.String, mean.String
		if mysqlDataAnalysisNumeric(stats.Kind) {
			stats.Numeric = stats.Rows - stats.Null
			if stats.Numeric > 0 {
				medianQuery, deviationQuery, centered, err := mysqlDataAnalysisNumericQueries(from, col)
				if err != nil {
					return nil, err
				}
				medianArgs := append(append([]any(nil), args...), int64(1)+int64(1-stats.Numeric%2), (stats.Numeric-1)/2)
				var median, deviation sql.NullString
				if err := q.QueryRowContext(ctx, medianQuery, medianArgs...).Scan(&median); err != nil {
					return nil, err
				}
				deviationArgs := append([]any(nil), args...)
				if centered {
					deviationArgs = append([]any{stats.Min}, deviationArgs...)
				}
				if err := q.QueryRowContext(ctx, deviationQuery, deviationArgs...).Scan(&deviation); err != nil {
					return nil, err
				}
				stats.Median, stats.StdDeviation = median.String, deviation.String
			}
		}
		if err := q.QueryRowContext(ctx, singleton, args...).Scan(&stats.Singletons); err != nil {
			return nil, err
		}
		rows, err := q.QueryContext(ctx, distribution, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var raw []byte
			var frequency MySQLDataAnalysisFrequency
			if err = rows.Scan(&raw, &frequency.Count, &frequency.Truncated); err != nil {
				break
			}
			if mysqlDataBinaryType(stats.Kind) || !utf8.Valid(raw) {
				frequency.Value = "0x" + hex.EncodeToString(raw)
			} else {
				frequency.Value = string(raw)
			}
			stats.Distribution = append(stats.Distribution, frequency)
		}
		if err == nil {
			err = rows.Err()
		}
		closeErr := rows.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if len(stats.Distribution) > 0 {
			stats.MaxFrequency = stats.Distribution[0].Count
		}
		stats.MaxFrequency = max(stats.MaxFrequency, stats.Null)
		if stats.MaxFrequency > 0 {
			modeQuery, err := mysqlDataAnalysisModeQuery(from, col)
			if err != nil {
				return nil, err
			}
			modeArgs := append(append([]any(nil), args...), stats.MaxFrequency)
			if err := q.QueryRowContext(ctx, modeQuery, modeArgs...).Scan(&stats.ModeCount); err != nil {
				return nil, err
			}
			for _, frequency := range stats.Distribution {
				if frequency.Count == stats.MaxFrequency {
					stats.Modes = append(stats.Modes, frequency)
				}
			}
			if stats.Null == stats.MaxFrequency {
				stats.ModeCount++
				stats.Modes = append(stats.Modes, MySQLDataAnalysisFrequency{Null: true, Count: stats.Null})
			}
		}
		result.Rows = stats.Rows
		result.Columns = append(result.Columns, stats)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result.CompletedAt, result.Duration = time.Now(), time.Since(started)
	return result, nil
}

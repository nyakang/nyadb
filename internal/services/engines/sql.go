package engines

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
	"github.com/nyakang/nyadb/internal/services/connections"
	"strconv"
	"strings"
	"time"
)

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func splitSQL(src string) ([]string, error) { return sqltext.SplitPortable(src) }
func execute(ctx context.Context, c queryer, kind connections.Kind, src string, sink contract.StreamSink) (contract.SQLResult, error) {
	parts, e := splitSQL(src)
	if e != nil {
		return contract.SQLResult{}, e
	}
	var result contract.SQLResult
	budget := 8 << 20
	remainingRows := 1000
	for _, q := range parts {
		if e = ctx.Err(); e != nil {
			return result, e
		}
		word := firstWord(q)
		if word == "" {
			continue
		}
		returns := word == "SELECT" || word == "WITH" || word == "SHOW" || word == "EXPLAIN" || word == "VALUES" || word == "TABLE" || word == "PRAGMA" || word == "CALL" || hasReturning(q)
		step := contract.StatementOutcome{SQL: q, State: "executed"}
		if !returns {
			r, err := c.ExecContext(ctx, q)
			if err != nil {
				step.State = failureState(kind, q, err)
				step.Error = err.Error()
				result.Steps = append(result.Steps, step)
				return result, err
			}
			step.AffectedRows, _ = r.RowsAffected()
			result.AffectedRows += step.AffectedRows
			set := contract.ResultSet{AffectedRows: step.AffectedRows}
			if sink != nil {
				if err = sink.BeginResult(len(result.Sets), nil); err == nil {
					err = sink.EndResult(len(result.Sets), step.AffectedRows)
				}
				if err != nil {
					return result, err
				}
			}
			result.Sets = append(result.Sets, set)
		} else {
			rows, err := c.QueryContext(ctx, q)
			if err != nil {
				step.State = failureState(kind, q, err)
				step.Error = err.Error()
				result.Steps = append(result.Steps, step)
				return result, err
			}
			for {
				set, err := scan(rows, kind, len(result.Sets), sink, &budget, remainingRows, 64<<10)
				remainingRows -= len(set.Rows)
				result.Sets = append(result.Sets, set)
				if err != nil {
					rows.Close()
					step.State = "uncertain"
					step.Error = err.Error()
					result.Steps = append(result.Steps, step)
					return result, err
				}
				if !rows.NextResultSet() {
					break
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return result, err
			}
		}
		result.Steps = append(result.Steps, step)
	}
	return result, nil
}
func scan(rows *sql.Rows, kind connections.Kind, index int, sink contract.StreamSink, budget *int, maxRows, maxCell int) (contract.ResultSet, error) {
	names, e := rows.Columns()
	if e != nil {
		return contract.ResultSet{}, e
	}
	cts, e := rows.ColumnTypes()
	if e != nil {
		return contract.ResultSet{}, e
	}
	if len(names) > 512 {
		return contract.ResultSet{}, fmt.Errorf("result has more than 512 columns")
	}
	set := contract.ResultSet{Columns: make([]contract.Column, len(names))}
	for i, n := range names {
		nullable, _ := cts[i].Nullable()
		set.Columns[i] = contract.Column{Name: n, DatabaseType: cts[i].DatabaseTypeName(), Nullable: nullable}
	}
	if sink != nil {
		if e = sink.BeginResult(index, set.Columns); e != nil {
			return set, e
		}
	}
	var count int64
	reasons := map[string]bool{}
	for rows.Next() {
		raw := make([]any, len(names))
		dest := make([]any, len(raw))
		for i := range dest {
			dest[i] = &raw[i]
		}
		if e = rows.Scan(dest...); e != nil {
			return set, e
		}
		cells := make([]contract.Cell, len(raw))
		if sink == nil && len(set.Rows) >= maxRows {
			count++
			reasons["rows"] = true
			continue
		}
		for i, v := range raw {
			if sink == nil {
				cells[i] = previewValue(v, set.Columns[i].DatabaseType, kind, maxCell)
			} else {
				cells[i] = cell(v, set.Columns[i].DatabaseType, kind)
			}
		}
		if sink != nil {
			if e = sink.WriteRow(index, cells); e != nil {
				return set, e
			}
		}
		size := len(raw)*64 + 24
		for i := range cells {
			cells[i] = previewCell(cells[i], maxCell)
			size += len(cells[i].Text) + len(cells[i].Bytes)
		}
		count++
		if len(set.Rows) >= maxRows {
			reasons["rows"] = true
			continue
		}
		if size > *budget {
			reasons["bytes"] = true
			continue
		}
		*budget -= size
		for i := range cells {
			if cells[i].Truncated {
				reasons["cell"] = true
			}
			if len(cells[i].Text) > maxCell {
				cells[i].Text = cells[i].Text[:maxCell]
				cells[i].Truncated = true
				reasons["cell"] = true
			}
			if len(cells[i].Bytes) > maxCell {
				cells[i].Bytes = cells[i].Bytes[:maxCell]
				cells[i].Truncated = true
				reasons["cell"] = true
			}
		}
		set.Rows = append(set.Rows, cells)
	}
	for _, r := range []string{"rows", "bytes", "cell"} {
		if reasons[r] {
			set.Truncation = append(set.Truncation, r)
		}
	}
	if e = rows.Err(); e != nil {
		return set, e
	}
	if sink != nil {
		e = sink.EndResult(index, count)
	}
	return set, e
}
func cell(v any, decl string, kind connections.Kind) contract.Cell {
	if v == nil {
		return contract.Cell{Kind: contract.Null}
	}
	typ := strings.ToUpper(decl)
	switch n := v.(type) {
	case []byte:
		if kind == connections.PostgreSQL && typ != "BYTEA" {
			return cell(string(n), decl, kind)
		}
		return contract.Cell{Kind: contract.Binary, Bytes: append([]byte(nil), n...)}
	case int64:
		return contract.Cell{Kind: contract.Integer, Text: strconv.FormatInt(n, 10)}
	case float64:
		return contract.Cell{Kind: contract.Floating, Text: strconv.FormatFloat(n, 'g', -1, 64)}
	case bool:
		return contract.Cell{Kind: contract.Boolean, Text: strconv.FormatBool(n)}
	case time.Time:
		return contract.Cell{Kind: contract.DateTime, Text: n.Format(time.RFC3339Nano)}
	case string:
		k := contract.Text
		if kind == connections.PostgreSQL {
			switch typ {
			case "NUMERIC", "DECIMAL":
				k = contract.Decimal
			case "JSON", "JSONB":
				k = contract.JSON
			case "DATE", "TIME", "TIMETZ", "TIMESTAMP", "TIMESTAMPTZ", "INTERVAL":
				k = contract.DateTime
			}
		}
		return contract.Cell{Kind: k, Text: n}
	default:
		return contract.Cell{Kind: contract.Text, Text: fmt.Sprint(v)}
	}
}

func firstWord(q string) string {
	q = strings.TrimSpace(q)
	for q != "" {
		if strings.HasPrefix(q, "--") {
			if n := strings.IndexByte(q, '\n'); n >= 0 {
				q = strings.TrimSpace(q[n+1:])
				continue
			}
			return ""
		}
		if strings.HasPrefix(q, "/*") {
			depth, i := 1, 2
			for i < len(q) && depth > 0 {
				if strings.HasPrefix(q[i:], "/*") {
					depth++
					i += 2
				} else if strings.HasPrefix(q[i:], "*/") {
					depth--
					i += 2
				} else {
					i++
				}
			}
			q = strings.TrimSpace(q[i:])
			continue
		}
		break
	}
	i := 0
	for i < len(q) && ((q[i] >= 'a' && q[i] <= 'z') || (q[i] >= 'A' && q[i] <= 'Z')) {
		i++
	}
	return strings.ToUpper(q[:i])
}

func failureState(kind connections.Kind, q string, e error) string {
	var pg *pgconn.PgError
	if kind == connections.PostgreSQL && errors.As(e, &pg) && firstWord(q) != "CALL" && firstWord(q) != "DO" {
		return "not_executed"
	}
	return "uncertain"
}

func previewCell(c contract.Cell, maxCell int) contract.Cell {
	if len(c.Text) > maxCell {
		c.Text = strings.Clone(c.Text[:maxCell])
		c.Truncated = true
	}
	if len(c.Bytes) > maxCell {
		c.Bytes = append([]byte(nil), c.Bytes[:maxCell]...)
		c.Truncated = true
	}
	return c
}

func previewValue(v any, decl string, kind connections.Kind, maxCell int) contract.Cell {
	truncated := false
	switch raw := v.(type) {
	case []byte:
		if len(raw) > maxCell {
			v = raw[:maxCell]
			truncated = true
		}
	case string:
		if len(raw) > maxCell {
			v = strings.Clone(raw[:maxCell])
			truncated = true
		}
	}
	c := cell(v, decl, kind)
	c.Truncated = truncated
	return c
}

func hasReturning(q string) bool {
	tokens, e := sqltext.Tokens(q)
	if e != nil {
		return false
	}
	for _, t := range tokens {
		if sqltext.Word(t, "RETURNING") {
			return true
		}
	}
	return false
}

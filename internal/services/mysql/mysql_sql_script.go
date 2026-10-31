package mysql

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"strings"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqlresult"
	"github.com/nyakang/nyadb/internal/db/sqltext"
)

// executeSQLScriptOnConn executes a lexically scanned file/selection in order
// on the original physical connection. It stops on the first error and never
// replays a completed or uncertain statement. Affected rows are per statement.
func executeSQLScriptOnConn(ctx context.Context, conn *sql.Conn, reader io.Reader, sink sqlresult.Sink) (out contract.SQLResult, err error) {
	var mode string
	if err = conn.QueryRowContext(ctx, "SELECT @@SESSION.sql_mode").Scan(&mode); err != nil {
		return out, err
	}
	scanner := sqltext.NewScriptScanner(reader)
	scanner.BackslashEscapes = !strings.Contains(mode, "NO_BACKSLASH_ESCAPES")
	rowsUsed, bytesUsed, streamSets := 0, 0, 0
	defer out.SetLegacyView()
	for n := 0; ; n++ {

		if err = ctx.Err(); err != nil {
			return out, err
		}
		statement, e := scanner.Next()
		if e == io.EOF {
			return out, nil
		}
		if e != nil {
			return out, e
		}

		if n >= 10000 {
			return out, errors.New("SQL script exceeds the 10000-statement safety limit")
		}
		step := contract.StatementOutcome{SQL: statement, State: "not_executed"}
		limits := sqlresult.Preview
		limits.Rows = max(0, limits.Rows-rowsUsed)
		limits.Bytes = max(1, limits.Bytes-bytesUsed)
		offset := streamSets
		var stream sqlresult.Sink
		if sink != nil {
			stream = func(i int, columns []contract.Column, row []contract.Cell) error { return sink(offset+i, columns, row) }
		}
		result, e := executeSQLStatementOnConn(ctx, conn, statement, limits, stream)
		if e != nil {
			var serverError *mysqldriver.MySQLError
			step.State = "uncertain"
			if errors.As(e, &serverError) && !sqlScriptCall(statement) {
				step.State = "not_executed"
			}
			step.Error = e.Error()
			out.Steps = append(out.Steps, step)
			return out, e
		}
		step.State = "executed"
		step.AffectedRows = result.AffectedRows
		out.Steps = append(out.Steps, step)
		out.AffectedRows += result.AffectedRows
		if len(result.Sets) == 0 {
			result.Sets = []contract.ResultSet{{AffectedRows: result.AffectedRows}}
			if stream != nil {
				if e = stream(0, []contract.Column{}, nil); e != nil {
					return out, e
				}
			}
		}
		for _, set := range result.Sets {
			bytesUsed += 256 + 128*len(set.Columns)
			for _, c := range set.Columns {
				bytesUsed += len(c.Name) + len(c.DatabaseType)
			}
			for _, row := range set.Rows {
				rowsUsed++
				bytesUsed += 96 + 96*len(row)
				for _, cell := range row {
					bytesUsed += len(cell.Text) + 3*len(cell.Bytes) + 2
				}
			}
		}
		streamSets += len(result.Sets)
		remaining := max(0, 128-len(out.Sets))
		retained := min(remaining, len(result.Sets))
		out.Sets = append(out.Sets, result.Sets[:retained]...)
		if retained < len(result.Sets) {
			out.Truncated = true
			if len(out.Sets) > 0 {
				out.Sets[len(out.Sets)-1].Truncation = append(out.Sets[len(out.Sets)-1].Truncation, "sets")
			}
		}
		if strings.Contains(strings.ToUpper(statement), "SQL_MODE") {
			if err = conn.QueryRowContext(ctx, "SELECT @@SESSION.sql_mode").Scan(&mode); err != nil {
				return out, err
			}
			scanner.BackslashEscapes = !strings.Contains(mode, "NO_BACKSLASH_ESCAPES")
		}
	}
}

func sqlScriptCall(statement string) bool {
	tokens, e := sqltext.Tokens(statement)
	if e != nil {
		return false
	}
	for _, t := range tokens {
		if !t.Comment {
			return sqltext.Word(t, "CALL")
		}
	}
	return false
}

package engines

import (
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func queryDiagnostic(err error, sql string, statement int) error {
	if err == nil {
		return nil
	}
	d := contract.QueryDiagnostic{SQL: sql, Statement: statement}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		d.Code, d.Detail, d.Hint = pg.Code, pg.Detail, pg.Hint
		if pg.Code == "40001" && d.Hint == "" {
			d.Hint = "Retry the complete transaction after refreshing its reads."
		}
		d.Position = contract.QueryCharacterPosition(sql, int(pg.Position))
	} else if code, ok := err.(interface{ Code() int }); ok {
		d.Code = fmt.Sprint(code.Code())
	}
	return &contract.QueryError{Cause: err, Diagnostic: d}
}

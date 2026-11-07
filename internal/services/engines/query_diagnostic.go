package engines

import (
	"errors"
	"fmt"
	"strconv"

	gausspq "gitcode.com/opengauss/openGauss-connector-go-pq"
	kingbasepq "gitea.com/kingbase/gokb"
	"github.com/HuaweiCloudDeveloper/gaussdb-go/gaussdbconn"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func postgresServerError(err error) (*pgconn.PgError, bool) {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg, true
	}
	var native *gaussdbconn.GaussdbError
	if errors.As(err, &native) {
		return &pgconn.PgError{Severity: native.Severity, Code: native.Code, Message: native.Message, Detail: native.Detail, Hint: native.Hint, Position: native.Position}, true
	}
	var gauss *gausspq.Error
	if errors.As(err, &gauss) {
		position, _ := strconv.ParseInt(gauss.Position, 10, 32)
		return &pgconn.PgError{Severity: gauss.Severity, Code: string(gauss.Code), Message: gauss.Message, Detail: gauss.Detail, Hint: gauss.Hint, Position: int32(position)}, true
	}
	var kb *kingbasepq.Error
	if errors.As(err, &kb) {
		position, _ := strconv.ParseInt(kb.Position, 10, 32)
		return &pgconn.PgError{Severity: kb.Severity, Code: string(kb.Code), Message: kb.Message, Detail: kb.Detail, Hint: kb.Hint, Position: int32(position)}, true
	}
	return nil, false
}

func queryDiagnostic(err error, sql string, statement int) error {
	if err == nil {
		return nil
	}
	d := contract.QueryDiagnostic{SQL: sql, Statement: statement}
	if pg, ok := postgresServerError(err); ok {
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

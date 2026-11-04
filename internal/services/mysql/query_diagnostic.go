package mysql

import (
	"errors"
	"fmt"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func mysqlQueryDiagnostic(err error, sql string, statement int) error {
	if err == nil {
		return nil
	}
	d := contract.QueryDiagnostic{SQL: sql, Statement: statement}
	var driverError *mysqldriver.MySQLError
	if errors.As(err, &driverError) {
		d.Code = fmt.Sprint(driverError.Number)
		d.Detail = driverError.Message
	}
	return &contract.QueryError{Cause: err, Diagnostic: d}
}

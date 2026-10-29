package mysql

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"testing"

	mysqldriver "github.com/go-sql-driver/mysql"
)

func TestMySQLSQLSessionBrokenRecognizesTransportAndTimeout(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		lost bool
	}{
		{"none", nil, false},
		{"server-idle-timeout-4031", fmt.Errorf("select schema: %w", &mysqldriver.MySQLError{Number: 4031}), true},
		{"server-connection-gone-2006", &mysqldriver.MySQLError{Number: 2006}, true},
		{"server-lost-connection-2013", &mysqldriver.MySQLError{Number: 2013}, true},
		{"server-client-connection-lost-2055", &mysqldriver.MySQLError{Number: 2055}, true},
		{"sql-syntax-1064", &mysqldriver.MySQLError{Number: 1064}, false},
		{"duplicate-key-1062", &mysqldriver.MySQLError{Number: 1062}, false},
		{"driver-bad-connection", driver.ErrBadConn, true},
		{"driver-invalid-connection", mysqldriver.ErrInvalidConn, true},
		{"eof", io.EOF, true},
		{"unexpected-eof", io.ErrUnexpectedEOF, true},
		{"unrelated", errors.New("query error"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mysqlSQLSessionBroken(tc.err); got != tc.lost {
				t.Fatalf("got lost=%t, want %t, err=%v", got, tc.lost, tc.err)
			}
		})
	}
}

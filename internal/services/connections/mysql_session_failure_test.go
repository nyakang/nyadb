package connections

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"testing"

	mysql "github.com/go-sql-driver/mysql"
)

func TestMySQLSQLSessionBrokenRecognizesTransportAndTimeout(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		lost bool
	}{
		{"none", nil, false},
		{"server-idle-timeout-4031", fmt.Errorf("select schema: %w", &mysql.MySQLError{Number: 4031}), true},
		{"server-connection-gone-2006", &mysql.MySQLError{Number: 2006}, true},
		{"server-lost-connection-2013", &mysql.MySQLError{Number: 2013}, true},
		{"server-client-connection-lost-2055", &mysql.MySQLError{Number: 2055}, true},
		{"sql-syntax-1064", &mysql.MySQLError{Number: 1064}, false},
		{"duplicate-key-1062", &mysql.MySQLError{Number: 1062}, false},
		{"driver-bad-connection", driver.ErrBadConn, true},
		{"driver-invalid-connection", mysql.ErrInvalidConn, true},
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

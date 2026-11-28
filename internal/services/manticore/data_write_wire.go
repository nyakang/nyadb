package manticore

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/nyakang/nyadb/internal/db/contract"
)

// Native receipt retains the original driver's decoded result, never a later
// SELECT or a synthesized transaction acknowledgement. Dispatched is deliberately
// conservative: a transport failure inside Exec may have reached the server.
type nativeWriteReceipt struct {
	Dispatched, Acknowledged   bool
	AffectedRows, LastInsertID int64
	ErrorCode                  uint16
	SQLState                   string
	Error                      string
}

func (l *queryLease) write(ctx context.Context, source string) (r nativeWriteReceipt, err error) {
	l.ops.Lock()
	defer l.ops.Unlock()
	child, done, e := l.request(ctx)
	if e != nil {
		return r, e
	}
	defer done()
	if e = l.connect(child); e != nil {
		return r, e
	}
	if e = child.Err(); e != nil {
		return r, e
	}
	r.Dispatched = true
	result, e := l.conn.(driver.ExecerContext).ExecContext(child, source, nil)
	if e == nil {
		var a, b error
		r.AffectedRows, a = result.RowsAffected()
		r.LastInsertID, b = result.LastInsertId()
		e = errors.Join(a, b)
		if e == nil && r.AffectedRows >= 0 {
			r.Acknowledged = true
			return r, nil
		}
		if e == nil {
			e = contract.ErrDataPage
		}
	}
	var server *mysqldriver.MySQLError
	if errors.As(e, &server) && l.conn.(driver.Validator).IsValid() {
		r.Acknowledged = true
		r.ErrorCode = server.Number
		r.SQLState = string(server.SQLState[:])
	} else {
		l.mu.Lock()
		l.lost = true
		l.mu.Unlock()
		closeConnection(l.conn)
		e = errors.Join(contract.ErrSQLSessionLost, e)
	}
	err = safeError(e, l.s.profileSecret())
	r.Error = err.Error()
	if r.ErrorCode != 0 {
		r.Error = server.Message
		if password := l.s.profileSecret(); password != "" {
			r.Error = strings.ReplaceAll(r.Error, password, "[redacted]")
		}
	}
	return r, err
}

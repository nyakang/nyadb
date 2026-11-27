package prestosql

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/jdbcplugin"
)

type cursorPage struct {
	result queryResult
	id     string
	more   bool
}

func cursorID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, ch := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if ch != '-' {
				return false
			}
			continue
		}
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return false
		}
	}
	return true
}

// Only the original eight-field SELECT envelope is admitted. Cursor control
// calls have no typed diagnostics in DBX 0.1.57; their errors stay private.
func decodeCursor(receipt jdbcplugin.Receipt) (cursorPage, error) {
	var page cursorPage
	if !receipt.Dispatched || !receipt.ExecutorAcknowledged || receipt.RemoteFailure || receipt.Diagnostic != nil {
		return page, ErrResponse
	}
	fields, err := resultFields(receipt.Result)
	if err != nil || len(fields) != 8 {
		return page, ErrResponse
	}
	if string(fields["has_more"]) != "true" && string(fields["has_more"]) != "false" {
		return page, ErrResponse
	}
	_ = json.Unmarshal(fields["has_more"], &page.more)
	if page.more {
		if json.Unmarshal(fields["session_id"], &page.id) != nil || !cursorID(page.id) {
			return cursorPage{}, ErrResponse
		}
	} else if string(fields["session_id"]) != "null" {
		return cursorPage{}, ErrResponse
	}
	delete(fields, "has_more")
	delete(fields, "session_id")
	receipt.Result, _ = json.Marshal(fields)
	page.result, err = decodeResult(receipt)
	if err != nil || len(page.result.columns) == 0 || len(page.result.rows) > 1 || page.more && (len(page.result.rows) != 1 || page.result.truncated) {
		return cursorPage{}, ErrResponse
	}
	return page, nil
}
func (p *process) cursorCall(ctx context.Context, method string, params any) (jdbcplugin.Receipt, error) {
	receipt, err := p.runtime.Client.Call(ctx, method, params)
	if err != nil || !receipt.ExecutorAcknowledged || receipt.RemoteFailure {
		if err == nil {
			err = ErrResponse
		}
		if receipt.Dispatched || !p.runtime.Client.Alive() {
			_ = p.runtime.Close()
			receipt.ProcessExitConfirmed = true
			err = errors.Join(contract.ErrSQLSessionLost, err)
		}
	}
	p.observed(method, receipt)
	return receipt, err
}

// streamNative retains one original JDBC cursor; pages never re-execute SQL.
// It is used only for generated SELECTs on a separately owned read process.
func (q *queryLease) streamNative(ctx context.Context, source string, maxRows int, consume func(queryResult) error) (err error) {
	if strings.TrimSpace(source) == "" || maxRows < 1 || maxRows > math.MaxInt32 || consume == nil {
		return jdbcplugin.ErrRequest
	}
	q.ops.Lock()
	defer q.ops.Unlock()
	child, done, err := q.request(ctx)
	if err != nil {
		return err
	}
	defer done()
	original, err := q.connect(child)
	if err != nil {
		return err
	}
	id := ""
	defer func() {
		if id != "" && original.runtime.Client.Alive() {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			receipt, e := original.cursorCall(cleanup, "closeQuerySession", map[string]any{"sessionId": id})
			cancel()
			if e == nil {
				fields, parseErr := resultFields(receipt.Result)
				if parseErr != nil || len(fields) != 1 || string(fields["ok"]) != "true" {
					e = ErrResponse
					_ = original.runtime.Close()
				}
			}
			err = errors.Join(err, e)
		}
		if !original.runtime.Client.Alive() {
			q.mu.Lock()
			q.lost = true
			q.mu.Unlock()
		}
	}()
	method := "executeQueryPage"
	params := map[string]any{"sql": source, "pageSize": 1, "maxRows": maxRows, "timeoutSecs": q.session.config.QueryTimeoutSeconds}
	var columns []string
	total := 0
	for {
		receipt, e := original.cursorCall(child, method, params)
		if e != nil {
			return e
		}
		page, e := decodeCursor(receipt)
		if e != nil || id != "" && page.more && page.id != id || columns != nil && !slices.Equal(columns, page.result.columns) {
			_ = original.runtime.Close()
			return errors.Join(contract.ErrSQLSessionLost, ErrResponse)
		}
		if columns == nil {
			columns = slices.Clone(page.result.columns)
		}
		id = page.id
		total += len(page.result.rows)
		if total > maxRows || page.result.truncated {
			return contract.ErrDataPage
		}
		if e = child.Err(); e != nil {
			return e
		}
		if e = consume(page.result); e != nil {
			return e
		}
		if !page.more {
			return child.Err()
		}
		method = "fetchQueryPage"
		params = map[string]any{"sessionId": id, "pageSize": 1}
	}
}

package agentbridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
)

type QueryCursorOptions struct {
	SQL, Schema                       string
	PageSize, MaxRows, TimeoutSeconds int
	Decoder                           ValueDecoder
}

// QueryPageSnapshot owns the complete original page and its executor receipt.
// HasMore describes only that captured cursor; it never authorizes reexecution.
type QueryPageSnapshot struct {
	QuerySnapshot
	Receipt Receipt
	HasMore bool
}

// QueryCursor never accepts a caller-supplied cursor ID or a replacement
// Session. Its close revokes queued reads, waits for the original read's
// acknowledgement and then detaches only this captured original cursor.
type QueryCursor struct {
	mu, ops      sync.Mutex
	session      *Session
	id           string
	columns      []contract.Column
	decoder      ValueDecoder
	ctx          context.Context
	cancel       context.CancelFunc
	more, closed bool
	closeOnce    sync.Once
	closeError   error
}

func (s *Session) OpenQueryCursor(ctx context.Context, options QueryCursorOptions) (*QueryCursor, QueryPageSnapshot, error) {
	var page QueryPageSnapshot
	if ctx == nil || ctx.Err() != nil {
		if ctx != nil {
			return nil, page, ctx.Err()
		}
		return nil, page, ErrRequest
	}
	if strings.TrimSpace(options.SQL) == "" || options.Schema != "" && !sqltext.ValidName(options.Schema) || options.PageSize < 1 || options.PageSize > 1000 || options.MaxRows < options.PageSize || options.MaxRows > math.MaxInt32 || options.TimeoutSeconds < 0 || options.TimeoutSeconds > 86400 {
		return nil, page, ErrRequest
	}
	r, err := s.Call(ctx, "execute_query_page", map[string]any{"sql": options.SQL, "schema": options.Schema, "pageSize": options.PageSize, "maxRows": options.MaxRows, "timeoutSecs": options.TimeoutSeconds})
	page.Receipt = cloneReceipts([]Receipt{r})[0]
	page.Native = append(json.RawMessage(nil), r.Result...)
	if err != nil {
		return nil, page, err
	}
	id, more, err := pageIdentity(r.Result)
	if err != nil {
		s.client.stop(ErrProtocol)
		<-s.client.processDone
		return nil, page, err
	}
	child, cancel := context.WithCancel(context.Background())
	c := &QueryCursor{session: s, id: id, decoder: options.Decoder, ctx: child, cancel: cancel, more: more}
	page.QuerySnapshot, err = DecodeQuery(r, options.Decoder)
	if err == nil {
		err = validateQueryPage(page.QuerySnapshot, options.PageSize, nil)
	}
	if err != nil {
		return nil, page, errors.Join(err, c.Close())
	}
	page.HasMore = more
	c.columns = append([]contract.Column(nil), page.SQL.Sets[0].Columns...)
	return c, page, nil
}

func pageIdentity(raw json.RawMessage) (string, bool, error) {
	fields, err := objectFields(raw)
	if err != nil {
		return "", false, ErrProtocol
	}
	var more bool
	if v, ok := fields["has_more"]; !ok || string(v) == "null" || json.Unmarshal(v, &more) != nil {
		return "", false, ErrProtocol
	}
	var id string
	if v, ok := fields["session_id"]; ok && string(v) != "null" {
		if json.Unmarshal(v, &id) != nil || len(id) > 4096 || strings.ContainsRune(id, 0) {
			return "", false, ErrProtocol
		}
	}
	if more && id == "" {
		return "", false, ErrProtocol
	}
	return id, more, nil
}

func validateQueryPage(snapshot QuerySnapshot, size int, columns []contract.Column) error {
	if len(snapshot.SQL.Sets) != 1 || len(snapshot.SQL.Sets[0].Rows) > size {
		return ErrProtocol
	}
	set := snapshot.SQL.Sets[0]
	if columns != nil {
		if len(columns) != len(set.Columns) {
			return ErrProtocol
		}
		for i, v := range columns {
			if v != set.Columns[i] {
				return ErrProtocol
			}
		}
	}
	return nil
}

func (c *QueryCursor) HasMore() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.more && !c.closed && c.session.Alive()
}

func (c *QueryCursor) Fetch(ctx context.Context, size int) (QueryPageSnapshot, error) {
	c.ops.Lock()
	defer c.ops.Unlock()
	var page QueryPageSnapshot
	if ctx == nil {
		return page, ErrRequest
	}
	if err := ctx.Err(); err != nil {
		return page, err
	}
	if size < 1 || size > 1000 {
		return page, ErrRequest
	}
	c.mu.Lock()
	if c.closed || !c.session.Alive() {
		c.mu.Unlock()
		return page, contract.ErrRevoked
	}
	if !c.more {
		c.mu.Unlock()
		return page, io.EOF
	}
	id := c.id
	columns := append([]contract.Column(nil), c.columns...)
	c.mu.Unlock()
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.ctx, cancel)
	defer func() { stop(); cancel() }()
	r, err := c.session.Call(child, "fetch_query_page", map[string]any{"sessionId": id, "pageSize": size})
	page.Receipt = cloneReceipts([]Receipt{r})[0]
	page.Native = append(json.RawMessage(nil), r.Result...)
	if err != nil {
		c.mu.Lock()
		c.more = false
		c.mu.Unlock()
		return page, err
	}
	nextID, more, err := pageIdentity(r.Result)
	if err == nil && nextID != "" && nextID != id {
		err = ErrProtocol
	}
	if err == nil {
		page.QuerySnapshot, err = DecodeQuery(r, c.decoder)
		if err == nil {
			err = validateQueryPage(page.QuerySnapshot, size, columns)
		}
	}
	if err != nil {
		c.mu.Lock()
		c.more = false
		c.mu.Unlock()
		// A contradictory ID/shape is a fault of the captured runtime. Wait
		// for that process; never detach an ID supplied by the bad response.
		if errors.Is(err, ErrProtocol) {
			c.session.client.stop(ErrProtocol)
			<-c.session.client.processDone
		}
		return page, err
	}
	page.HasMore = more
	c.mu.Lock()
	c.more = more
	c.mu.Unlock()
	return page, nil
}

func (c *QueryCursor) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		c.more = false
		c.cancel()
		id := c.id
		c.mu.Unlock()
		c.ops.Lock()
		defer c.ops.Unlock()
		if id != "" && c.session.Alive() {
			ctx, cancel := context.WithTimeout(context.Background(), c.session.client.options.CancelWait)
			r, err := c.session.Call(ctx, "close_query_session", map[string]string{"sessionId": id})
			cancel()
			if err == nil {
				var detached bool
				if json.Unmarshal(r.Result, &detached) != nil || !r.ExecutorAcknowledged {
					err = ErrProtocol
					c.session.client.stop(ErrProtocol)
					<-c.session.client.processDone
				}
			}
			c.closeError = err
		}
		c.mu.Lock()
		c.columns = nil
		c.id = ""
		c.mu.Unlock()
	})
	return c.closeError
}

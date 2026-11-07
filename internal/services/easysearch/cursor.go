package easysearch

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// Cursor is a bounded native PIT offset cursor, limited to 10000 hits.
// It is not an Elasticsearch search_after cursor. The native _doc sort may
// repeat across shards; offsets preserve all frozen hits, including IDs that
// are equal on different routing shards. Full streaming uses a separate
// native scroll resource.
// Cursor belongs to its original client and PIT. It cannot be rebound to a
// replacement connection. The latest native PIT ID and sort bytes stay private.
type Cursor struct {
	mu        sync.Mutex
	client    *Client
	query     json.RawMessage
	pageSize  int
	after     []json.RawMessage
	exhausted bool
	closed    bool
	total     int64
	started   bool
}

type Page struct {
	Documents []Snapshot
	Total     int64
	HasMore   bool
	After     []json.RawMessage
}

func (c *Client) OpenCursor(ctx context.Context, index string, query json.RawMessage, pageSize int) (*Cursor, error) {
	if !validIndex(index) || pageSize < 1 || pageSize > 500 || !validSource(query) {
		return nil, ErrInvalid
	}
	if !c.operation.TryLock() {
		return nil, ErrBusy
	}
	defer c.operation.Unlock()
	if err := c.available(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	full := len(c.pits) >= 32
	c.mu.Unlock()
	if full {
		return nil, ErrBounds
	}
	r, err := c.perform(ctx, Request{Method: "POST", Path: "/" + url.PathEscape(index) + "/_pit?keep_alive=1m&allow_partial_pit_creation=false"})
	if err != nil {
		if interrupted, ok := err.(*Interrupted); ok {
			interrupted.WriteOutcomeUnknown = false
		}
		return nil, err
	}
	var opened struct {
		ID     string `json:"pit_id"`
		Shards struct {
			Total      *int `json:"total"`
			Successful *int `json:"successful"`
			Failed     *int `json:"failed"`
		} `json:"_shards"`
	}
	if decodeJSON(r.Body, &opened) != nil || !validPIT(opened.ID) {
		c.mu.Lock()
		c.retired = true
		c.mu.Unlock()
		return nil, &Interrupted{Cause: ErrProtocol}
	}
	cursor := &Cursor{client: c, query: bytes.Clone(query), pageSize: pageSize}
	c.mu.Lock()
	c.pits[cursor] = opened.ID
	c.mu.Unlock()
	if opened.Shards.Total == nil || *opened.Shards.Total <= 0 || opened.Shards.Successful == nil || *opened.Shards.Successful != *opened.Shards.Total || opened.Shards.Failed == nil || *opened.Shards.Failed != 0 {
		c.mu.Lock()
		c.retired = true
		c.mu.Unlock()
		return nil, &Interrupted{Cause: ErrProtocol}
	}
	return cursor, nil
}

func validPIT(id string) bool {
	return len(id) > 0 && len(id) <= 64<<10 && utf8.ValidString(id) && strings.IndexFunc(id, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}

func (cursor *Cursor) Next(ctx context.Context) (Page, error) {
	cursor.mu.Lock()
	defer cursor.mu.Unlock()
	if cursor.exhausted && !cursor.closed {
		return Page{Total: cursor.total}, nil
	}
	page, err := cursor.readPage(ctx, cursor.after, cursor.pageSize)
	if err == nil {
		cursor.after = cloneBoundary(page.After)
		cursor.exhausted = !page.HasMore
	}
	return page, err
}

// ReadAfter rereads an explicit original boundary without advancing the
// sequential iterator. The server PIT ID is still updated after every response.
func (cursor *Cursor) ReadAfter(ctx context.Context, after []json.RawMessage, limit int) (Page, error) {
	cursor.mu.Lock()
	defer cursor.mu.Unlock()
	if limit < 1 || limit > 500 || len(after) > 1 {
		return Page{}, ErrInvalid
	}
	if len(after) == 1 {
		var n int64
		if json.Unmarshal(after[0], &n) != nil || n < 0 || n > 10000 {
			return Page{}, ErrInvalid
		}
	}
	return cursor.readPage(ctx, cloneBoundary(after), limit)
}
func cloneBoundary(after []json.RawMessage) []json.RawMessage {
	out := make([]json.RawMessage, len(after))
	for i, v := range after {
		out[i] = bytes.Clone(v)
	}
	return out
}
func (cursor *Cursor) readPage(ctx context.Context, boundary []json.RawMessage, limit int) (Page, error) {
	c := cursor.client
	if cursor.closed {
		return Page{}, ErrRetired
	}
	if !c.operation.TryLock() {
		return Page{}, ErrBusy
	}
	defer c.operation.Unlock()
	if err := c.available(); err != nil {
		return Page{}, err
	}
	c.mu.Lock()
	id, exists := c.pits[cursor]
	c.mu.Unlock()
	if !exists {
		return Page{}, ErrRetired
	}
	var offset int64
	if len(boundary) == 1 {
		if json.Unmarshal(boundary[0], &offset) != nil || offset < 0 {
			return Page{}, ErrInvalid
		}
	}
	if offset >= 10000 {
		return Page{}, ErrBounds
	}
	// At the native window boundary, use the exact count to indicate more
	// results instead of requesting an extra hit outside that window.
	size := min(int64(limit+1), 10000-offset)
	body := map[string]any{"query": json.RawMessage(bytes.Clone(cursor.query)), "size": size, "track_total_hits": true, "seq_no_primary_term": true, "_source": true, "stored_fields": []string{"_routing"}, "sort": []map[string]string{{"_doc": "asc"}}, "pit": map[string]string{"id": id, "keep_alive": "1m"}}
	if len(boundary) != 0 {
		body["from"] = offset
	}
	encoded, _ := json.Marshal(body)
	r, err := c.perform(ctx, Request{Method: "POST", Path: "/_search?allow_partial_search_results=false", Body: encoded})
	if err != nil {
		if interrupted, ok := err.(*Interrupted); ok {
			interrupted.WriteOutcomeUnknown = false
		}
		return Page{}, err
	}
	var native struct {
		PIT      string `json:"pit_id"`
		TimedOut *bool  `json:"timed_out"`
		Shards   struct {
			Total      int `json:"total"`
			Successful int `json:"successful"`
			Failed     int `json:"failed"`
		} `json:"_shards"`
		Hits struct {
			Total struct {
				Value    *int64 `json:"value"`
				Relation string `json:"relation"`
			} `json:"total"`
			Hits []struct {
				nativeDocument
				Sort   []json.RawMessage `json:"sort"`
				Fields struct {
					Routing []string `json:"_routing"`
				} `json:"fields"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if decodeJSON(r.Body, &native) != nil {
		return Page{}, ErrProtocol
	}
	// Capture the latest valid ID even if the page is incomplete, so Close
	// can release the actual server resource rather than an outdated token.
	if validPIT(native.PIT) {
		c.mu.Lock()
		c.pits[cursor] = native.PIT
		c.mu.Unlock()
	}
	if !validPIT(native.PIT) || native.TimedOut == nil || *native.TimedOut || native.Shards.Total <= 0 || native.Shards.Failed != 0 || native.Shards.Successful != native.Shards.Total || native.Hits.Total.Value == nil || *native.Hits.Total.Value < 0 || native.Hits.Total.Relation != "eq" {
		return Page{}, ErrProtocol
	}
	total := *native.Hits.Total.Value
	if int64(len(native.Hits.Hits)) != min(size, max(int64(0), total-offset)) {
		return Page{}, ErrProtocol
	}
	page := Page{Total: total, HasMore: total > offset+int64(min(limit, len(native.Hits.Hits)))}
	if cursor.started && cursor.total != page.Total {
		return Page{}, ErrProtocol
	}
	var after []json.RawMessage
	seen := map[DocumentID]bool{}
	for i, hit := range native.Hits.Hits {
		id := DocumentID{Index: hit.Index, ID: hit.ID, Routing: hit.Routing}
		if len(hit.Fields.Routing) > 1 {
			return Page{}, ErrProtocol
		}
		if len(hit.Fields.Routing) == 1 {
			if id.Routing != "" && id.Routing != hit.Fields.Routing[0] {
				return Page{}, ErrProtocol
			}
			id.Routing = hit.Fields.Routing[0]
		}
		if !validDocumentID(id) || !validSource(hit.Source) || len(hit.Sort) != 1 {
			return Page{}, ErrProtocol
		}
		var position int64
		if json.Unmarshal(hit.Sort[0], &position) != nil || position < 0 || seen[id] {
			return Page{}, ErrProtocol
		}
		seen[id] = true
		snapshot, e := hit.nativeDocument.snapshot(id)
		if e != nil {
			return Page{}, e
		}
		if i < limit {
			page.Documents = append(page.Documents, snapshot)
			next, _ := json.Marshal(offset + int64(i) + 1)
			after = []json.RawMessage{next}
		}
	}
	page.After = after
	cursor.total = page.Total
	cursor.started = true
	return page, nil
}

// pitClosed verifies acknowledgement for the exact original Easysearch PIT.
func pitClosed(raw []byte, id string) error {
	var reply struct {
		Pits []struct {
			ID         string `json:"pit_id"`
			Successful *bool  `json:"successful"`
		} `json:"pits"`
	}
	if decodeJSON(raw, &reply) != nil || len(reply.Pits) != 1 || reply.Pits[0].ID != id || reply.Pits[0].Successful == nil || !*reply.Pits[0].Successful {
		return ErrProtocol
	}
	return nil
}

func (cursor *Cursor) Close(ctx context.Context) error {
	cursor.mu.Lock()
	defer cursor.mu.Unlock()
	if cursor.closed {
		return nil
	}
	c := cursor.client
	if !c.operation.TryLock() {
		return ErrBusy
	}
	defer c.operation.Unlock()
	c.mu.Lock()
	id, exists := c.pits[cursor]
	closed := c.closed
	c.mu.Unlock()
	if closed || !exists {
		cursor.closed = true
		cursor.client, cursor.query, cursor.after = nil, nil, nil
		return nil
	}
	body, _ := json.Marshal(map[string][]string{"pit_id": {id}})
	r, err := c.exchange(ctx, Request{Method: "DELETE", Path: "/_pit", Body: body})
	if err == nil {
		err = pitClosed(r.Body, id)
	}
	if err != nil {
		return err
	}
	c.mu.Lock()
	delete(c.pits, cursor)
	c.mu.Unlock()
	cursor.closed = true
	cursor.client, cursor.query, cursor.after = nil, nil, nil
	return nil
}

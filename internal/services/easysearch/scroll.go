package easysearch

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"sync"
)

// ScrollCursor owns an independent original native search snapshot. Next
// advances that original resource once; interruption retires the client rather
// than retrying a possibly advanced scroll. PIT offsets use separate resources.
type ScrollCursor struct {
	mu                         sync.Mutex
	client                     *Client
	first                      Page
	started, closed, exhausted bool
	total, read                int64
	pageSize                   int
}

func (c *Client) OpenScroll(ctx context.Context, index string, query json.RawMessage, pageSize int) (*ScrollCursor, error) {
	if !validIndex(index) || !validSource(query) || pageSize < 1 || pageSize > 500 {
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
	full := len(c.scrolls) >= 32
	c.mu.Unlock()
	if full {
		return nil, ErrBounds
	}
	body, _ := json.Marshal(map[string]any{"query": json.RawMessage(bytes.Clone(query)), "size": pageSize, "sort": []string{"_doc"}, "track_total_hits": true, "seq_no_primary_term": true, "stored_fields": []string{"_routing"}, "_source": true})
	response, err := c.perform(ctx, Request{Method: "POST", Path: "/" + url.PathEscape(index) + "/_search?scroll=1m&allow_partial_search_results=false", Body: body})
	if err != nil {
		if interrupted, ok := err.(*Interrupted); ok {
			interrupted.WriteOutcomeUnknown = false
		}
		return nil, err
	}
	scroll := &ScrollCursor{client: c, pageSize: pageSize}
	page, err := scroll.decodePage(response.Body)
	if err != nil {
		c.mu.Lock()
		c.retired = true
		c.mu.Unlock()
		return nil, &Interrupted{Cause: err}
	}
	scroll.first = page
	return scroll, nil
}

func (scroll *ScrollCursor) Next(ctx context.Context) (Page, error) {
	scroll.mu.Lock()
	defer scroll.mu.Unlock()
	if scroll.closed {
		return Page{}, ErrRetired
	}
	c := scroll.client
	if err := c.available(); err != nil {
		return Page{}, err
	}
	if !scroll.started {
		scroll.started = true
		page := scroll.first
		scroll.first = Page{}
		return page, nil
	}
	if scroll.exhausted {
		return Page{Total: scroll.total}, nil
	}
	if !c.operation.TryLock() {
		return Page{}, ErrBusy
	}
	defer c.operation.Unlock()
	if err := c.available(); err != nil {
		return Page{}, err
	}
	c.mu.Lock()
	id, exists := c.scrolls[scroll]
	c.mu.Unlock()
	if !exists {
		return Page{}, ErrRetired
	}
	body, _ := json.Marshal(map[string]string{"scroll_id": id, "scroll": "1m"})
	response, err := c.perform(ctx, Request{Method: "POST", Path: "/_search/scroll", Body: body})
	if err != nil {
		if interrupted, ok := err.(*Interrupted); ok {
			interrupted.WriteOutcomeUnknown = false
		}
		return Page{}, err
	}
	page, err := scroll.decodePage(response.Body)
	if err != nil {
		c.mu.Lock()
		c.retired = true
		c.mu.Unlock()
		return Page{}, &Interrupted{Cause: err}
	}
	return page, nil
}

func (scroll *ScrollCursor) decodePage(raw []byte) (Page, error) {
	var native struct {
		ID       string                                  `json:"_scroll_id"`
		TimedOut *bool                                   `json:"timed_out"`
		Shards   struct{ Total, Successful, Failed int } `json:"_shards"`
		Hits     struct {
			Total struct {
				Value    *int64 `json:"value"`
				Relation string `json:"relation"`
			} `json:"total"`
			Hits []struct {
				nativeDocument
				Fields struct {
					Routing []string `json:"_routing"`
				} `json:"fields"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if decodeJSON(raw, &native) != nil {
		return Page{}, ErrProtocol
	}
	if validPIT(native.ID) {
		scroll.client.mu.Lock()
		scroll.client.scrolls[scroll] = native.ID
		scroll.client.mu.Unlock()
	}
	if !validPIT(native.ID) || native.TimedOut == nil || *native.TimedOut || native.Shards.Total <= 0 || native.Shards.Successful != native.Shards.Total || native.Shards.Failed != 0 || native.Hits.Total.Value == nil || *native.Hits.Total.Value < 0 || native.Hits.Total.Relation != "eq" || len(native.Hits.Hits) > scroll.pageSize {
		return Page{}, ErrProtocol
	}
	total := *native.Hits.Total.Value
	if scroll.read > 0 && scroll.total != total {
		return Page{}, ErrProtocol
	}
	if scroll.read+int64(len(native.Hits.Hits)) > total || len(native.Hits.Hits) == 0 && scroll.read != total {
		return Page{}, ErrProtocol
	}
	page := Page{Total: total, HasMore: scroll.read+int64(len(native.Hits.Hits)) < total}
	seen := map[DocumentID]bool{}
	for _, hit := range native.Hits.Hits {
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
		if !validDocumentID(id) || !validSource(hit.Source) || seen[id] {
			return Page{}, ErrProtocol
		}
		seen[id] = true
		snapshot, err := hit.nativeDocument.snapshot(id)
		if err != nil {
			return Page{}, err
		}
		page.Documents = append(page.Documents, snapshot)
	}
	scroll.total, scroll.read = total, scroll.read+int64(len(page.Documents))
	scroll.exhausted = !page.HasMore
	return page, nil
}

func (scroll *ScrollCursor) Close(ctx context.Context) error {
	scroll.mu.Lock()
	defer scroll.mu.Unlock()
	if scroll.closed {
		return nil
	}
	c := scroll.client
	if !c.operation.TryLock() {
		return ErrBusy
	}
	defer c.operation.Unlock()
	c.mu.Lock()
	id, exists := c.scrolls[scroll]
	closed := c.closed
	c.mu.Unlock()
	if !closed && exists {
		body, _ := json.Marshal(map[string][]string{"scroll_id": {id}})
		response, err := c.exchange(ctx, Request{Method: "DELETE", Path: "/_search/scroll", Body: body})
		if err != nil {
			return err
		}
		if err = scrollClosed(response.Body); err != nil {
			return err
		}
	}
	c.mu.Lock()
	delete(c.scrolls, scroll)
	c.mu.Unlock()
	scroll.closed, scroll.client, scroll.first = true, nil, Page{}
	return nil
}

func scrollClosed(raw []byte) error {
	var reply struct {
		Succeeded *bool `json:"succeeded"`
		Freed     *int  `json:"num_freed"`
	}
	if decodeJSON(raw, &reply) != nil || reply.Succeeded == nil || !*reply.Succeeded || reply.Freed == nil || *reply.Freed < 1 {
		return ErrProtocol
	}
	return nil
}

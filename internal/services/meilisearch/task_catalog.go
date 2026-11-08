package meilisearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

var ErrTaskUnavailable = errors.New("Meilisearch task unavailable in the original index scope")

// TaskQuery is a bounded native /tasks query. From is inclusive, and Next from
// the response is the next matching UID, not an offset. Pages are live views;
// total and task statuses can change between requests.
type TaskQuery struct {
	Limit                             int
	From                              *uint32
	Reverse                           bool
	UIDs, BatchUIDs, CanceledBy       []uint32
	IndexUIDs, Statuses, Types        []string
	BeforeEnqueuedAt, AfterEnqueuedAt string
	BeforeStartedAt, AfterStartedAt   string
	BeforeFinishedAt, AfterFinishedAt string
}

// TaskRecord retains the complete native task, including error, details,
// duration, timestamps and future fields. The private origin binds subsequent
// observation to this physical client and original tuple, not merely its UID.
// This tuple is not an atomic server-incarnation or mutation guard.
type TaskRecord struct {
	UID                      uint32
	IndexUID                 *string
	Type, Status, EnqueuedAt string
	Raw                      json.RawMessage
	origin                   *taskOrigin
}
type taskOrigin struct {
	client  *Client
	receipt Receipt
	raw     []byte
	status  string
}
type TaskPage struct {
	Results    []TaskRecord
	Total      uint64
	Limit      int
	From, Next *uint32
	Raw        json.RawMessage
}

func validTaskStatus(status string) bool {
	switch status {
	case "enqueued", "processing", "succeeded", "failed", "canceled":
		return true
	}
	return false
}
func validTaskType(kind string) bool {
	if len(kind) == 0 || len(kind) > 128 {
		return false
	}
	for _, ch := range kind {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9') {
			return false
		}
	}
	return true
}
func taskUIDs(values url.Values, key string, ids []uint32) error {
	if len(ids) > 256 {
		return ErrBounds
	}
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[uint32]bool, len(ids))
	parts := make([]string, len(ids))
	for i, id := range ids {
		if seen[id] {
			return ErrInvalid
		}
		seen[id] = true
		parts[i] = strconv.FormatUint(uint64(id), 10)
	}
	values.Set(key, strings.Join(parts, ","))
	return nil
}
func taskStrings(values url.Values, key string, items []string, valid func(string) bool) error {
	if len(items) > 256 {
		return ErrBounds
	}
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		if !valid(item) || seen[item] {
			return ErrInvalid
		}
		seen[item] = true
	}
	values.Set(key, strings.Join(items, ","))
	return nil
}
func (c *Client) taskQuery(q TaskQuery) (string, error) {
	if q.Limit < 1 || q.Limit > 1000 {
		return "", ErrInvalid
	}
	v := url.Values{"limit": {strconv.Itoa(q.Limit)}, "reverse": {strconv.FormatBool(q.Reverse)}}
	if q.From != nil {
		v.Set("from", strconv.FormatUint(uint64(*q.From), 10))
	}
	for _, entry := range []struct {
		key string
		ids []uint32
	}{{"uids", q.UIDs}, {"batchUids", q.BatchUIDs}, {"canceledBy", q.CanceledBy}} {
		if err := taskUIDs(v, entry.key, entry.ids); err != nil {
			return "", err
		}
	}
	indexes := q.IndexUIDs
	if c.config.IndexUID != "" {
		if len(indexes) > 0 && (len(indexes) != 1 || indexes[0] != c.config.IndexUID) {
			return "", ErrInvalid
		}
		indexes = []string{c.config.IndexUID}
	}
	for _, entry := range []struct {
		key   string
		items []string
		valid func(string) bool
	}{{"indexUids", indexes, validIndex}, {"statuses", q.Statuses, validTaskStatus}, {"types", q.Types, validTaskType}} {
		if err := taskStrings(v, entry.key, entry.items, entry.valid); err != nil {
			return "", err
		}
	}
	for _, entry := range []struct{ key, value string }{{"beforeEnqueuedAt", q.BeforeEnqueuedAt}, {"afterEnqueuedAt", q.AfterEnqueuedAt}, {"beforeStartedAt", q.BeforeStartedAt}, {"afterStartedAt", q.AfterStartedAt}, {"beforeFinishedAt", q.BeforeFinishedAt}, {"afterFinishedAt", q.AfterFinishedAt}} {
		if entry.value != "" {
			if len(entry.value) > 64 {
				return "", ErrInvalid
			}
			if _, err := time.Parse(time.RFC3339Nano, entry.value); err != nil {
				return "", ErrInvalid
			}
			v.Set(entry.key, entry.value)
		}
	}
	path := "/tasks?" + v.Encode()
	if err := validateRequest(Request{Method: "GET", Path: path}); err != nil {
		return "", err
	}
	return path, nil
}
func optionalTaskUID(raw json.RawMessage) (*uint32, error) {
	if bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	n, err := nativeUint(raw, 32)
	if err != nil {
		return nil, err
	}
	id := uint32(n)
	return &id, nil
}
func (c *Client) taskRecord(raw []byte) (TaskRecord, error) {
	r, status, err := receiptFields(raw, "uid")
	if err != nil || !validTaskStatus(status) {
		return TaskRecord{}, ErrProtocol
	}
	if c.config.IndexUID != "" && (r.IndexUID == nil || *r.IndexUID != c.config.IndexUID) {
		return TaskRecord{}, ErrProtocol
	}
	// Public fields and private evidence never share writable byte slices/pointers.
	var index *string
	if r.IndexUID != nil {
		value := *r.IndexUID
		index = &value
	}
	return TaskRecord{UID: r.UID, IndexUID: index, Type: r.Type, Status: status, EnqueuedAt: r.EnqueuedAt, Raw: bytes.Clone(raw), origin: &taskOrigin{client: c, receipt: r, raw: bytes.Clone(raw), status: status}}, nil
}

// ListTasks performs one original-client readonly request. Response preserves
// complete native evidence even when parsing fails or the server rejects it.
func (c *Client) ListTasks(ctx context.Context, q TaskQuery) (TaskPage, Response, error) {
	q.UIDs = slices.Clone(q.UIDs)
	q.BatchUIDs = slices.Clone(q.BatchUIDs)
	q.CanceledBy = slices.Clone(q.CanceledBy)
	q.IndexUIDs = slices.Clone(q.IndexUIDs)
	q.Statuses = slices.Clone(q.Statuses)
	q.Types = slices.Clone(q.Types)
	if q.From != nil {
		id := *q.From
		q.From = &id
	}
	path, err := c.taskQuery(q)
	if err != nil {
		return TaskPage{}, Response{}, err
	}
	response, err := c.Do(ctx, Request{Method: "GET", Path: path})
	page := TaskPage{Raw: bytes.Clone(response.Body)}
	if err != nil {
		return page, response, err
	}
	f, err := nativeObject(response.Body)
	if err != nil {
		return page, response, err
	}
	var results []json.RawMessage
	if decodeJSON(f["results"], &results) != nil || results == nil {
		return page, response, ErrProtocol
	}
	total, e1 := nativeUint(f["total"], 64)
	limit, e2 := nativeUint(f["limit"], 32)
	from, e3 := optionalTaskUID(f["from"])
	next, e4 := optionalTaskUID(f["next"])
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || limit != uint64(q.Limit) || len(results) > q.Limit || total < uint64(len(results)) {
		return page, response, ErrProtocol
	}
	if len(results) == 0 && (from != nil || next != nil) || len(results) > 0 && from == nil {
		return page, response, ErrProtocol
	}
	parsed := make([]TaskRecord, 0, len(results))
	for _, raw := range results {
		record, e := c.taskRecord(raw)
		if e != nil {
			return page, response, e
		}
		if len(q.UIDs) > 0 && !slices.Contains(q.UIDs, record.UID) || len(q.IndexUIDs) > 0 && (record.IndexUID == nil || !slices.Contains(q.IndexUIDs, *record.IndexUID)) || len(q.Statuses) > 0 && !slices.Contains(q.Statuses, record.Status) || len(q.Types) > 0 && !slices.Contains(q.Types, record.Type) {
			return page, response, ErrProtocol
		}
		if q.From != nil && (q.Reverse && record.UID < *q.From || !q.Reverse && record.UID > *q.From) {
			return page, response, ErrProtocol
		}
		if len(parsed) > 0 {
			previous := parsed[len(parsed)-1].UID
			if q.Reverse && record.UID <= previous || !q.Reverse && record.UID >= previous {
				return page, response, ErrProtocol
			}
		}
		parsed = append(parsed, record)
	}
	if len(parsed) > 0 {
		if *from != parsed[0].UID {
			return page, response, ErrProtocol
		}
		if next != nil {
			last := parsed[len(parsed)-1].UID
			if len(parsed) != q.Limit || q.Reverse && *next <= last || !q.Reverse && *next >= last {
				return page, response, ErrProtocol
			}
		}
	}
	page.Results, page.Total, page.Limit, page.From, page.Next = parsed, total, int(limit), from, next
	return page, response, nil
}

// LookupTask reads a UID explicitly; it does not invent an admission receipt.
func (c *Client) LookupTask(ctx context.Context, uid uint32) (TaskRecord, Response, error) {
	// Scoped clients use the index-filtered list so global tasks cannot leak
	// through an unrestricted /tasks/<uid> response.
	if c.config.IndexUID != "" {
		page, response, err := c.ListTasks(ctx, TaskQuery{Limit: 1, UIDs: []uint32{uid}})
		if err != nil {
			return TaskRecord{}, response, err
		}
		if len(page.Results) != 1 {
			return TaskRecord{}, response, ErrTaskUnavailable
		}
		return page.Results[0], response, nil
	}
	response, err := c.Do(ctx, Request{Method: "GET", Path: "/tasks/" + strconv.FormatUint(uint64(uid), 10)})
	if err != nil {
		return TaskRecord{}, response, err
	}
	task, err := c.taskRecord(response.Body)
	if err == nil && task.UID != uid {
		return TaskRecord{}, response, ErrProtocol
	}
	return task, response, err
}

// ReadObservedTask refreshes an original observation, refusing another client
// or caller-edited evidence. Missing records remain missing; nothing is replayed.
func (c *Client) ReadObservedTask(ctx context.Context, original TaskRecord) (TaskRecord, Response, error) {
	origin := original.origin
	if origin == nil || origin.client != c || !bytes.Equal(original.Raw, origin.raw) || original.Status != origin.status {
		return TaskRecord{}, Response{}, ErrInvalid
	}
	input := Receipt{UID: original.UID, IndexUID: original.IndexUID, Type: original.Type, EnqueuedAt: original.EnqueuedAt}
	if !sameReceipt(input, origin.receipt) {
		return TaskRecord{}, Response{}, ErrInvalid
	}
	task, response, err := c.LookupTask(ctx, origin.receipt.UID)
	if err != nil {
		return task, response, err
	}
	if !sameReceipt(task.origin.receipt, origin.receipt) {
		c.retire()
		return task, response, ErrTaskChanged
	}
	return task, response, nil
}

// ChangeTasks admits exactly the specified native UIDs (never all tasks or a
// dynamic status/date query). The server acts on those UIDs when handling the
// request; task tuples cannot atomically guard a restart/restore. No public
// workbench capability is enabled by this low-level native codec.
// Admission is not cancellation/deletion completion. Cancellation of ctx stops
// the original HTTP executor; an unknown write outcome prohibits replay.
func (c *Client) ChangeTasks(ctx context.Context, action string, uids []uint32) (Receipt, Response, error) {
	if len(uids) == 0 {
		return Receipt{}, Response{}, ErrInvalid
	}
	v := url.Values{}
	if err := taskUIDs(v, "uids", uids); err != nil {
		return Receipt{}, Response{}, err
	}
	if c.config.IndexUID != "" {
		v.Set("indexUids", c.config.IndexUID)
	}
	method, path := "", ""
	switch action {
	case "cancel":
		method, path = "POST", "/tasks/cancel"
	case "delete":
		method, path = "DELETE", "/tasks"
	default:
		return Receipt{}, Response{}, ErrInvalid
	}
	request := Request{Method: method, Path: path + "?" + v.Encode()}
	return c.Submit(ctx, request)
}

package meilisearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

// Receipt records admission only. Raw preserves the original native evidence,
// including future fields. Its tuple is not a service-incarnation identifier.
type Receipt struct {
	UID        uint32
	IndexUID   *string
	Type       string
	EnqueuedAt string
	Raw        json.RawMessage
}

// Task is an observation of the original receipt, not a rollback guarantee.
type Task struct {
	Receipt
	Status string
	Raw    json.RawMessage
}

type TaskFailure struct{ Status string }

func (e *TaskFailure) Error() string {
	return "Meilisearch task " + e.Status + "; inspect original task evidence"
}

func validIndex(uid string) bool {
	return connections.IsMeilisearchIndexUID(uid)
}

func nativeObject(raw []byte) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if decodeJSON(raw, &fields) != nil || fields == nil {
		return nil, ErrProtocol
	}
	return fields, nil
}

func nativeString(raw json.RawMessage) (string, error) {
	var value string
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) || json.Unmarshal(raw, &value) != nil {
		return "", ErrProtocol
	}
	return value, nil
}

func nativeUint(raw json.RawMessage, bits int) (uint64, error) {
	value, err := strconv.ParseUint(string(raw), 10, bits)
	if err != nil || strconv.FormatUint(value, 10) != string(raw) {
		return 0, ErrProtocol
	}
	return value, nil
}

func receiptFields(raw []byte, uidField string) (Receipt, string, error) {
	fields, err := nativeObject(raw)
	if err != nil {
		return Receipt{}, "", err
	}
	uid, err := nativeUint(fields[uidField], 32)
	if err != nil {
		return Receipt{}, "", err
	}
	index, present := fields["indexUid"]
	if !present {
		return Receipt{}, "", ErrProtocol
	}
	var indexUID *string
	if !bytes.Equal(index, []byte("null")) {
		value, err := nativeString(index)
		if err != nil || !validIndex(value) {
			return Receipt{}, "", ErrProtocol
		}
		indexUID = &value
	}
	kind, err := nativeString(fields["type"])
	if err != nil || len(kind) == 0 || len(kind) > 128 {
		return Receipt{}, "", ErrProtocol
	}
	for _, ch := range kind {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9') {
			return Receipt{}, "", ErrProtocol
		}
	}
	enqueued, err := nativeString(fields["enqueuedAt"])
	if err != nil || len(enqueued) > 64 {
		return Receipt{}, "", ErrProtocol
	}
	if _, err := time.Parse(time.RFC3339Nano, enqueued); err != nil {
		return Receipt{}, "", ErrProtocol
	}
	status, err := nativeString(fields["status"])
	if err != nil {
		return Receipt{}, "", err
	}
	return Receipt{UID: uint32(uid), IndexUID: indexUID, Type: kind, EnqueuedAt: enqueued, Raw: bytes.Clone(raw)}, status, nil
}

func parseReceipt(raw []byte) (Receipt, error) {
	r, status, err := receiptFields(raw, "taskUid")
	if err != nil || status != "enqueued" {
		return Receipt{}, ErrProtocol
	}
	return r, nil
}

func sameReceipt(a, b Receipt) bool {
	return a.UID == b.UID && a.Type == b.Type && a.EnqueuedAt == b.EnqueuedAt &&
		((a.IndexUID == nil && b.IndexUID == nil) || (a.IndexUID != nil && b.IndexUID != nil && *a.IndexUID == *b.IndexUID))
}

func originalReceipt(r Receipt) (Receipt, error) {
	original, err := parseReceipt(r.Raw)
	if err != nil || !sameReceipt(r, original) {
		return Receipt{}, ErrInvalid
	}
	return original, nil
}

// taskManagementKind identifies native task endpoints whose asynchronous
// admission uses HTTP 200. Other HTTP 200 endpoints remain synchronous.
func taskManagementKind(request Request) string {
	u, err := url.Parse(request.Path)
	if err != nil {
		return ""
	}
	if request.Method == "POST" && u.Path == "/tasks/cancel" {
		return "taskCancelation"
	}
	if request.Method == "DELETE" && u.Path == "/tasks" {
		return "taskDeletion"
	}
	return ""
}
func validateAdmission(request Request, response Response) (Receipt, error) {
	expected, kind := 202, taskManagementKind(request)
	if kind != "" {
		expected = 200
	}
	if backupKind := serverBackupKind(request); backupKind != "" {
		kind = backupKind
	}
	receipt, err := parseReceipt(response.Body)
	if err != nil || readOnly(request) || response.Status != expected || kind != "" && (receipt.Type != kind || receipt.IndexUID != nil) {
		return Receipt{}, ErrProtocol
	}
	return receipt, nil
}

// Submit expects native asynchronous admission: normally HTTP 202, or HTTP
// 200 on the native task cancel/delete endpoints. Neither status proves task
// completion. Synchronous endpoints (e.g. keys) use Do.
func (c *Client) Submit(ctx context.Context, request Request) (Receipt, Response, error) {
	if readOnly(request) {
		return Receipt{}, Response{}, ErrInvalid
	}
	response, err := c.Do(ctx, request)
	if err != nil {
		return Receipt{}, response, err
	}
	r, err := validateAdmission(request, response)
	if err != nil {
		c.retire()
		return Receipt{}, response, &Interrupted{WriteOutcomeUnknown: true, Cause: ErrProtocol}
	}
	return r, response, nil
}

func (c *Client) ReadTask(ctx context.Context, receipt Receipt) (Task, error) {
	original, err := originalReceipt(receipt)
	if err != nil {
		return Task{}, err
	}
	response, err := c.Do(ctx, Request{Method: "GET", Path: "/tasks/" + strconv.FormatUint(uint64(original.UID), 10)})
	if err != nil {
		return Task{Receipt: original, Raw: bytes.Clone(response.Body)}, err
	}
	r, status, err := receiptFields(response.Body, "uid")
	task := Task{Receipt: original, Status: status, Raw: bytes.Clone(response.Body)}
	if err != nil {
		return task, ErrProtocol
	}
	if !sameReceipt(r, original) {
		c.retire()
		return task, ErrTaskChanged
	}
	if !validTaskStatus(status) {
		return task, ErrProtocol
	}
	return task, nil
}

// WaitTask polls one original receipt, with an explicit bound up to ten
// minutes. Cancellation stops observation only; it does not cancel the server
// task, undo admitted writes, or submit anything again.
func (c *Client) WaitTask(ctx context.Context, receipt Receipt, bound time.Duration) (Task, error) {
	original, err := originalReceipt(receipt)
	if err != nil || bound <= 0 || bound > 10*time.Minute {
		return Task{}, ErrInvalid
	}
	if !c.waiter.TryLock() {
		return Task{}, ErrBusy
	}
	defer c.waiter.Unlock()
	c.mu.Lock()
	if c.closed || c.retired {
		c.mu.Unlock()
		return Task{}, ErrRetired
	}
	c.work.Add(1)
	c.mu.Unlock()
	defer c.work.Done()
	child, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	var last Task
	for {
		if err := child.Err(); err != nil {
			return last, err
		}
		observed, readErr := c.ReadTask(child, original)
		if len(observed.Raw) != 0 {
			last = observed
		}
		err = readErr
		if err != nil {
			return last, err
		}
		switch last.Status {
		case "succeeded":
			return last, nil
		case "failed", "canceled":
			return last, &TaskFailure{Status: last.Status}
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-child.Done():
			timer.Stop()
			return last, child.Err()
		case <-timer.C:
		}
	}
}

func (r Receipt) String() string {
	return fmt.Sprintf("Meilisearch admitted task %d (%s)", r.UID, r.Type)
}

package meilisearch

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type taskDocument struct {
	bound      *queryLease
	object     contract.ObjectRef
	key        [32]byte
	management taskMutation
}

func (s *Session) SupportsTaskDocument(o contract.ObjectRef) bool {
	return s.Alive() && o.ProfileID == s.profileID && o.Kind == "tasks" && o.Name == "tasks" && o.Schema == "" && (o.Catalog == "" && s.config.IndexUID == "" || validIndex(o.Catalog) && (s.config.IndexUID == "" || s.config.IndexUID == o.Catalog))
}
func (s *Session) ReserveTaskDocument(o contract.ObjectRef) (contract.TaskDocumentLease, error) {
	return s.reserveTaskDocument(o, "")
}
func (s *Session) reserveTaskDocument(o contract.ObjectRef, allowedOwner string) (contract.TaskDocumentLease, error) {
	if !s.Alive() {
		return nil, contract.ErrRevoked
	}
	if !s.SupportsTaskDocument(o) {
		return nil, contract.ErrUnsupported
	}
	// The adapter shares its existing bounded physical executor/close registry;
	// task requests never pass through the public SQL/REST Execute interface.
	reserved, err := s.ReserveQuery(1)
	if err != nil {
		return nil, err
	}
	d := &taskDocument{bound: reserved.(*queryLease), object: o}
	if err = s.bindNamespaceLease(d.bound, o.Catalog, allowedOwner); err != nil {
		reserved.Release()
		return nil, err
	}
	if _, err = rand.Read(d.key[:]); err != nil {
		reserved.Release()
		return nil, ErrInvalid
	}
	return d, nil
}
func (d *taskDocument) Close() error {
	d.bound.Release()
	d.bound.ops.Lock()
	d.bound.s.releaseNamespace(d.management.owner)
	clear(d.key[:])
	d.management = taskMutation{}
	d.bound.ops.Unlock()
	return nil
}
func (d *taskDocument) sign(kind string, raw []byte) string {
	m := hmac.New(sha256.New, d.key[:])
	m.Write([]byte(kind))
	m.Write(raw)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
func (d *taskDocument) proof(kind, token string) ([]byte, error) {
	if len(token) == 0 || len(token) > 16384 {
		return nil, ErrInvalid
	}
	payload, mac, ok := strings.Cut(token, ".")
	if !ok {
		return nil, ErrInvalid
	}
	raw, e := base64.RawURLEncoding.DecodeString(payload)
	got, e2 := base64.RawURLEncoding.DecodeString(mac)
	m := hmac.New(sha256.New, d.key[:])
	m.Write([]byte(kind))
	m.Write(raw)
	if e != nil || e2 != nil || !hmac.Equal(got, m.Sum(nil)) {
		return nil, ErrInvalid
	}
	return raw, nil
}
func taskStringIDs(input []string) ([]uint32, error) {
	if len(input) > 256 {
		return nil, ErrBounds
	}
	ids := make([]uint32, len(input))
	for i, text := range input {
		n, err := nativeUint([]byte(text), 32)
		if err != nil {
			return nil, ErrInvalid
		}
		ids[i] = uint32(n)
	}
	return ids, nil
}
func (d *taskDocument) query(q contract.TaskQuery) (TaskQuery, string, error) {
	for _, values := range [][]string{q.UIDs, q.Indexes, q.Statuses, q.Types, q.BatchUIDs, q.CanceledBy} {
		if len(values) > 256 {
			return TaskQuery{}, "", ErrBounds
		}
	}
	out := TaskQuery{Limit: q.Limit, Reverse: q.Reverse, IndexUIDs: append([]string(nil), q.Indexes...), Statuses: append([]string(nil), q.Statuses...), Types: append([]string(nil), q.Types...), BeforeEnqueuedAt: q.BeforeEnqueuedAt, AfterEnqueuedAt: q.AfterEnqueuedAt, BeforeStartedAt: q.BeforeStartedAt, AfterStartedAt: q.AfterStartedAt, BeforeFinishedAt: q.BeforeFinishedAt, AfterFinishedAt: q.AfterFinishedAt}
	var err error
	for _, entry := range []struct {
		input  []string
		output *[]uint32
	}{{q.UIDs, &out.UIDs}, {q.BatchUIDs, &out.BatchUIDs}, {q.CanceledBy, &out.CanceledBy}} {
		*entry.output, err = taskStringIDs(entry.input)
		if err != nil {
			return out, "", err
		}
	}
	if d.object.Catalog != "" {
		if len(out.IndexUIDs) > 0 && (len(out.IndexUIDs) != 1 || out.IndexUIDs[0] != d.object.Catalog) {
			return out, "", ErrInvalid
		}
		out.IndexUIDs = []string{d.object.Catalog}
	}
	path, err := (&Client{config: d.bound.s.config}).taskQuery(out)
	if err != nil {
		return out, "", err
	}
	sum := sha256.Sum256([]byte(path))
	digest := hex.EncodeToString(sum[:])
	if q.From != "" {
		raw, e := d.proof("cursor", q.From)
		if e != nil {
			return out, "", e
		}
		var cursor struct {
			UID   uint32
			Query string
		}
		if decodeJSON(raw, &cursor) != nil || cursor.Query != digest {
			return out, "", ErrInvalid
		}
		out.From = &cursor.UID
	}
	return out, digest, nil
}
func (d *taskDocument) record(native TaskRecord) (contract.TaskRecord, error) {
	if d.object.Catalog != "" && (native.IndexUID == nil || *native.IndexUID != d.object.Catalog) {
		return contract.TaskRecord{}, ErrProtocol
	}
	fields, err := nativeObject(native.Raw)
	if err != nil {
		return contract.TaskRecord{}, err
	}
	values := map[string]string{}
	for _, key := range []string{"startedAt", "finishedAt", "duration"} {
		raw, ok := fields[key]
		if !ok {
			return contract.TaskRecord{}, ErrProtocol
		}
		if bytes.Equal(raw, []byte("null")) {
			continue
		}
		value, e := nativeString(raw)
		if e != nil || len(value) > 128 {
			return contract.TaskRecord{}, ErrProtocol
		}
		if key != "duration" {
			if len(value) > 64 {
				return contract.TaskRecord{}, ErrProtocol
			}
			if _, e = time.Parse(time.RFC3339Nano, value); e != nil {
				return contract.TaskRecord{}, ErrProtocol
			}
		}
		values[key] = value
	}
	for _, key := range []string{"details", "error"} {
		raw, ok := fields[key]
		if !ok {
			return contract.TaskRecord{}, ErrProtocol
		}
		if bytes.Equal(raw, []byte("null")) {
			continue
		}
		if _, e := nativeObject(raw); e != nil {
			return contract.TaskRecord{}, ErrProtocol
		}
		values[key] = string(raw)
	}
	text := func(key string) string { return values[key] }
	jsonText := text
	index := ""
	if native.IndexUID != nil {
		index = *native.IndexUID
	}
	result := contract.TaskRecord{UID: strconv.FormatUint(uint64(native.UID), 10), Index: index, Type: native.Type, Status: native.Status, EnqueuedAt: native.EnqueuedAt, StartedAt: text("startedAt"), FinishedAt: text("finishedAt"), Duration: text("duration"), Details: jsonText("details"), Error: jsonText("error"), Raw: string(native.Raw)}
	sum := sha256.Sum256(native.Raw)
	result.Token = d.sign("task", sum[:])
	return result, nil
}
func (d *taskDocument) original(record contract.TaskRecord) (TaskRecord, error) {
	if len(record.Raw) == 0 || len(record.Raw) > responseLimit {
		return TaskRecord{}, ErrInvalid
	}
	digest, err := d.proof("task", record.Token)
	sum := sha256.Sum256([]byte(record.Raw))
	if err != nil || !hmac.Equal(digest, sum[:]) {
		return TaskRecord{}, ErrInvalid
	}
	client := d.bound.client
	if client == nil {
		client = &Client{config: d.bound.s.config}
	}
	native, err := client.taskRecord([]byte(record.Raw))
	if err != nil {
		return TaskRecord{}, err
	}
	expected, err := d.record(native)
	if err != nil {
		return TaskRecord{}, err
	}
	if expected != record {
		return TaskRecord{}, ErrInvalid
	}
	return native, nil
}
func (d *taskDocument) request(ctx context.Context) (context.Context, func(), error) {
	child, done, err := d.bound.request(ctx)
	if err != nil {
		return nil, nil, d.taskError(err)
	}
	if err = d.bound.connect(child); err == nil {
		err = d.bound.s.validateObservation(child, d.bound.client)
	}
	if err != nil {
		done()
		return nil, nil, d.taskError(err)
	}
	return child, done, nil
}
func (d *taskDocument) taskError(err error) error {
	if errors.Is(err, ErrTaskChanged) || errors.Is(err, ErrRetired) || errors.Is(err, contract.ErrRevoked) || errors.Is(err, contract.ErrSQLSessionLost) {
		d.bound.mu.Lock()
		d.bound.lost = true
		d.bound.mu.Unlock()
		return errors.Join(contract.ErrTaskLeaseLost, err)
	}
	return err
}
func (d *taskDocument) List(ctx context.Context, q contract.TaskQuery) (contract.TaskPage, error) {
	d.bound.ops.Lock()
	defer d.bound.ops.Unlock()
	native, digest, err := d.query(q)
	if err != nil {
		return contract.TaskPage{}, err
	}
	ctx, done, err := d.request(ctx)
	if err != nil {
		return contract.TaskPage{}, err
	}
	defer done()
	page, response, err := d.bound.client.ListTasks(ctx, native)
	result := contract.TaskPage{Raw: string(response.Body)}
	path, _ := d.bound.client.taskQuery(native)
	result.Request = "GET " + path
	if err != nil {
		return result, d.taskError(err)
	}
	result.Total = page.Total
	result.Results = make([]contract.TaskRecord, 0, len(page.Results))
	for _, record := range page.Results {
		value, e := d.record(record)
		if e != nil {
			return contract.TaskPage{Raw: result.Raw, Request: result.Request}, e
		}
		result.Results = append(result.Results, value)
	}
	if page.Next != nil {
		raw, _ := json.Marshal(struct {
			UID   uint32
			Query string
		}{*page.Next, digest})
		result.Next = d.sign("cursor", raw)
	}
	return result, nil
}
func (d *taskDocument) read(ctx context.Context, record contract.TaskRecord) (contract.TaskRecord, error) {
	native, err := d.original(record)
	if err != nil {
		return contract.TaskRecord{}, err
	}
	// Explicit index scope is applied on single-task observation as well as lists.
	if d.object.Catalog != "" {
		page, response, err := d.bound.client.ListTasks(ctx, TaskQuery{Limit: 1, UIDs: []uint32{native.UID}, IndexUIDs: []string{d.object.Catalog}})
		if err != nil {
			return contract.TaskRecord{}, &contract.TaskRequestError{Raw: string(response.Body), Cause: d.taskError(err)}
		}
		if len(page.Results) != 1 {
			return contract.TaskRecord{}, ErrTaskUnavailable
		}
		if !sameReceipt(page.Results[0].origin.receipt, native.origin.receipt) {
			d.bound.client.retire()
			return contract.TaskRecord{}, &contract.TaskRequestError{Raw: string(response.Body), Cause: d.taskError(ErrTaskChanged)}
		}
		return d.record(page.Results[0])
	}
	refreshed, response, err := d.bound.client.ReadObservedTask(ctx, native)
	if err != nil {
		return contract.TaskRecord{}, &contract.TaskRequestError{Raw: string(response.Body), Cause: d.taskError(err)}
	}
	return d.record(refreshed)
}
func (d *taskDocument) Read(ctx context.Context, record contract.TaskRecord) (contract.TaskRecord, error) {
	d.bound.ops.Lock()
	defer d.bound.ops.Unlock()
	if _, err := d.original(record); err != nil {
		return contract.TaskRecord{}, err
	}
	ctx, done, err := d.request(ctx)
	if err != nil {
		return contract.TaskRecord{}, err
	}
	defer done()
	return d.read(ctx, record)
}
func (d *taskDocument) Observe(ctx context.Context, record contract.TaskRecord, bound time.Duration) (contract.TaskRecord, error) {
	if bound <= 0 || bound > 10*time.Minute {
		return contract.TaskRecord{}, ErrInvalid
	}
	d.bound.ops.Lock()
	defer d.bound.ops.Unlock()
	if _, err := d.original(record); err != nil {
		return contract.TaskRecord{}, err
	}
	ctx, done, err := d.request(ctx)
	if err != nil {
		return contract.TaskRecord{}, err
	}
	defer done()
	if _, err = d.original(record); err != nil {
		return contract.TaskRecord{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	last := record
	for {
		if err = ctx.Err(); err != nil {
			return last, err
		}
		next, e := d.read(ctx, last)
		if e != nil {
			return last, e
		}
		last = next
		if last.Status == "succeeded" || last.Status == "failed" || last.Status == "canceled" {
			return last, nil
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return last, ctx.Err()
		case <-timer.C:
		}
	}
}

var _ contract.TaskDocumentsProvider = (*Session)(nil)

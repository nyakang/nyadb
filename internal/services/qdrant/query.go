package qdrant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type queryLease struct {
	s              *Session
	document       uint64
	ops, mu        sync.Mutex
	ctx            context.Context
	cancel         context.CancelFunc
	client         *Client
	released, lost bool
}

type leaseLostError struct{ cause error }

func (e *leaseLostError) Error() string {
	return "Qdrant tab session lost; recover explicitly without replay: " + e.cause.Error()
}
func (e *leaseLostError) Unwrap() error { return e.cause }
func (e *leaseLostError) Is(target error) bool {
	return target == contract.ErrSQLSessionLost
}

func (s *Session) ReserveQuery(document uint64) (contract.QueryLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.revoked {
		return nil, contract.ErrRevoked
	}
	if document == 0 || len(s.leases) >= 32 {
		return nil, contract.ErrSQLSessionLimit
	}
	for l := range s.leases {
		if l.document == document {
			return nil, connections.ErrInvalid
		}
	}
	ctx, cancel := context.WithCancel(s.ctx)
	l := &queryLease{s: s, document: document, ctx: ctx, cancel: cancel}
	s.leases[l] = true
	return l, nil
}
func (l *queryLease) Release() {
	l.mu.Lock()
	l.released = true
	l.cancel()
	l.mu.Unlock()
	l.ops.Lock()
	if l.client != nil {
		l.client.Close()
		l.client = nil
	}
	l.ops.Unlock()
	l.s.mu.Lock()
	delete(l.s.leases, l)
	l.s.mu.Unlock()
}
func (l *queryLease) Replace() (contract.QueryLease, error) {
	l.Release()
	return l.s.ReserveQuery(l.document)
}
func (l *queryLease) request(ctx context.Context) (context.Context, func(), error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return nil, nil, contract.ErrRevoked
	}
	if l.lost {
		return nil, nil, contract.ErrSQLSessionLost
	}
	child, done, err := l.s.request(ctx)
	if err != nil {
		return nil, nil, err
	}
	child, cancel := context.WithCancel(child)
	stop := context.AfterFunc(l.ctx, cancel)
	return child, func() { stop(); cancel(); done() }, nil
}
func (l *queryLease) markLost(err error) error {
	if errors.Is(err, ErrRetired) || errors.Is(err, contract.ErrRevoked) {
		l.mu.Lock()
		l.lost = true
		l.mu.Unlock()
		return &leaseLostError{cause: err}
	}
	return err
}
func (l *queryLease) connect(ctx context.Context) error {
	if l.client != nil {
		return nil
	}
	c, err := l.s.open(ctx)
	if err != nil {
		return l.markLost(err)
	}
	l.client = c
	return nil
}
func (l *queryLease) Preconnect(ctx context.Context) error {
	l.ops.Lock()
	defer l.ops.Unlock()
	ctx, done, err := l.request(ctx)
	if err != nil {
		return err
	}
	defer done()
	if err = l.connect(ctx); err != nil {
		return err
	}
	return l.markLost(l.s.validateObservation(ctx, l.client))
}

func asynchronousSnapshot(r Request) bool {
	u, err := url.Parse(r.Path)
	if err != nil {
		return false
	}
	waitFalse := false
	for _, value := range u.Query()["wait"] {
		waitFalse = waitFalse || value == "false"
	}
	if !waitFalse {
		return false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if parts[0] == "snapshots" {
		return r.Method == "POST" && len(parts) == 1 || r.Method == "DELETE" && len(parts) == 2
	}
	if len(parts) < 3 || parts[0] != "collections" {
		return false
	}
	i := 2
	if len(parts) >= 5 && parts[2] == "shards" {
		i = 4
	}
	if parts[i] != "snapshots" {
		return false
	}
	return r.Method == "POST" && len(parts) == i+1 || r.Method == "DELETE" && len(parts) == i+2 || (r.Method == "PUT" || r.Method == "POST") && len(parts) == i+2 && parts[i+1] == "recover"
}

// Native operation receipts, including batch receipts, report admission or
// completion independently of HTTP 200. Explicit snapshot background work and
// HTTP 202 are admission; an HTTP response is not a write/readback guarantee.
func requestOutcome(r Request, response Response) (string, error) {
	state := "completed"
	if response.Status == 202 {
		fields, err := object(response.Body)
		var status string
		if err != nil || decodeJSON(fields["status"], &status) != nil {
			return "", ErrProtocol
		}
		// Native background snapshots return status:accepted without result.
		if status == "accepted" {
			return contract.StatementEnqueued, nil
		}
		if _, err := NativeResult(response.Body); err != nil {
			return "", err
		}
		return contract.StatementEnqueued, nil
	}
	if asynchronousSnapshot(r) {
		state = contract.StatementEnqueued
	}
	if readOnly(r) {
		return state, nil
	}
	result, err := NativeResult(response.Body)
	if err != nil {
		return "", err
	}
	items := []json.RawMessage{result}
	if len(result) > 0 && result[0] == '[' {
		if decodeJSON(result, &items) != nil {
			return "", ErrProtocol
		}
	}
	receipts, acknowledged := 0, false
	for _, raw := range items {
		fields, err := object(raw)
		if err != nil {
			continue // e.g. a native synchronous bool result
		}
		var status string
		_ = decodeJSON(fields["status"], &status)
		id, present := fields["operation_id"]
		if !present && status != "acknowledged" && status != "completed" {
			continue
		}
		n, err := strconv.ParseUint(string(id), 10, 64)
		if err != nil || strconv.FormatUint(n, 10) != string(id) || status != "acknowledged" && status != "completed" {
			return "", ErrProtocol
		}
		receipts++
		if status == "acknowledged" {
			acknowledged = true
		}
	}
	if acknowledged {
		state = contract.StatementEnqueued
	} else if receipts > 0 {
		state = "completed"
	}
	return state, nil
}
func (l *queryLease) Execute(ctx context.Context, namespace, source string) (contract.SQLResult, error) {
	return l.ExecuteStream(ctx, namespace, source, nil)
}
func (l *queryLease) ExecuteStream(ctx context.Context, namespace, source string, sink func(int, []contract.Column, []contract.Cell) error) (result contract.SQLResult, err error) {
	r, err := ParseRequest(source)
	if err != nil {
		return result, err
	}
	if namespace != "" {
		if err = l.s.checkNamespace(l.s.Namespace(namespace)); err != nil {
			return result, err
		}
	}
	// Namespace selection never rewrites a supplied native endpoint or body.
	l.ops.Lock()
	defer l.ops.Unlock()
	ctx, done, err := l.request(ctx)
	if err != nil {
		return result, err
	}
	defer done()
	if err = l.connect(ctx); err != nil {
		return result, err
	}
	if err = l.markLost(l.s.validateObservation(ctx, l.client)); err != nil {
		return result, err
	}
	start := time.Now()
	defer func() { result.Duration = time.Since(start); result.SetLegacyView() }()
	response, err := l.client.Do(ctx, r)
	state := "completed"
	if err == nil {
		if state, err = requestOutcome(r, response); err != nil {
			l.client.retire()
			err = &Interrupted{WriteOutcomeUnknown: !readOnly(r), Cause: ErrProtocol}
		}
	}
	step := contract.StatementOutcome{SQL: r.Method + " " + r.Path, State: state}
	if err != nil {
		err = l.markLost(err)
		step.State = "failed"
		if errors.Is(err, contract.ErrSQLSessionLost) {
			step.State = "interrupted"
			var interrupted *Interrupted
			if errors.As(err, &interrupted) && interrupted.WriteOutcomeUnknown {
				step.State = "uncertain"
			}
		}
		step.Error = err.Error()
	}
	result.Steps = []contract.StatementOutcome{step}
	if response.Status != 0 {
		kind, typ := contract.Text, "HTTP body"
		media, _, _ := mime.ParseMediaType(response.ContentType)
		var value json.RawMessage
		if media == "application/json" && decodeJSON(response.Body, &value) == nil {
			kind, typ = contract.JSON, "JSON"
		}
		binary := !utf8.Valid(response.Body) || media == "application/octet-stream" || media == "application/gzip" || media == "application/x-gzip" || media == "application/x-tar" || media == "application/zip"
		body := contract.Cell{Kind: kind, Text: string(response.Body), Truncated: response.Incomplete}
		if binary {
			typ = "binary"
			body.Kind, body.Text, body.Bytes = contract.Binary, "", bytes.Clone(response.Body)
		}
		columns := []contract.Column{{Name: "HTTP status", DatabaseType: "integer"}, {Name: "response", DatabaseType: typ}, {Name: "Content-Type", DatabaseType: "text"}}
		row := []contract.Cell{{Kind: contract.Integer, Text: strconv.Itoa(response.Status)}, body, {Kind: contract.Text, Text: response.ContentType}}
		result.Sets = []contract.ResultSet{{Columns: columns, Rows: [][]contract.Cell{row}}}
		if response.Incomplete {
			result.Sets[0].Truncation = []string{"incomplete native HTTP response"}
		}
		// A presentation failure cannot change a settled native receipt or
		// dispatch another request. Complete native evidence stays in result.
		if err == nil && sink != nil {
			copy := make([]contract.Cell, len(row))
			for i, cell := range row {
				copy[i] = cell.Clone()
			}
			err = sink(0, append([]contract.Column(nil), columns...), copy)
		}
	}
	return result, err
}

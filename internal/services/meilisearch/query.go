package meilisearch

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
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

func (s *Session) ReserveQuery(document uint64) (contract.QueryLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.revoked {
		return nil, contract.ErrRevoked
	}
	if document == 0 || len(s.leases) >= 32 {
		return nil, contract.ErrSQLSessionLimit
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
		err = errors.Join(contract.ErrSQLSessionLost, err)
		l.mu.Lock()
		l.lost = true
		l.mu.Unlock()
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
func (s *Session) checkRequestScope(r Request) error {
	if s.config.IndexUID == "" {
		return nil
	}
	u, err := url.Parse(r.Path)
	if err != nil {
		return ErrInvalid
	}
	path := "/indexes/" + s.config.IndexUID
	if u.Path != path && !strings.HasPrefix(u.Path, path+"/") {
		return contract.ErrUnsupported
	}
	return nil
}
func (l *queryLease) Execute(ctx context.Context, namespace, text string) (contract.SQLResult, error) {
	return l.ExecuteStream(ctx, namespace, text, nil)
}
func (l *queryLease) ExecuteStream(ctx context.Context, namespace, text string, sink func(int, []contract.Column, []contract.Cell) error) (result contract.SQLResult, err error) {
	r, err := ParseRequest(text)
	if err != nil {
		return result, err
	}
	if namespace != "" {
		if err = l.s.checkNamespace(l.s.Namespace(namespace)); err != nil {
			return result, err
		}
	}
	if err = l.s.checkRequestScope(r); err != nil {
		return result, err
	}
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
	if err == nil && (response.Status == 202 || taskManagementKind(r) != "") {
		if _, receiptErr := validateAdmission(r, response); receiptErr != nil {
			l.client.retire()
			err = &Interrupted{WriteOutcomeUnknown: !readOnly(r), Cause: ErrProtocol}
		} else {
			state = contract.StatementEnqueued
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
		columns := []contract.Column{{Name: "HTTP status", DatabaseType: "integer"}, {Name: "response", DatabaseType: typ}, {Name: "ETag", DatabaseType: "text"}, {Name: "Content-Type", DatabaseType: "text"}}
		row := []contract.Cell{{Kind: contract.Integer, Text: strconv.Itoa(response.Status)}, {Kind: kind, Text: string(response.Body)}, {Kind: contract.Text, Text: response.ETag}, {Kind: contract.Text, Text: response.ContentType}}
		result.Sets = []contract.ResultSet{{Columns: columns, Rows: [][]contract.Cell{row}}}
		// A presentation failure cannot rewrite a settled native admission or
		// synchronous outcome, and must never cause request replay.
		if err == nil && sink != nil {
			err = sink(0, append([]contract.Column(nil), columns...), append([]contract.Cell(nil), row...))
		}
	}
	return result, err
}

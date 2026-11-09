package neo4j

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	nd "github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j/db"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type fakeInfo struct{ agent string }

func (s fakeInfo) Agent() string                       { return s.agent }
func (s fakeInfo) Address() string                     { return "original:7687" }
func (s fakeInfo) ProtocolVersion() db.ProtocolVersion { return db.ProtocolVersion{Major: 5, Minor: 8} }

type fakeDatabase string

func (d fakeDatabase) Name() string { return string(d) }

type fakeCounters struct{ nd.Counters }

func (fakeCounters) NodesCreated() int         { return 2 }
func (fakeCounters) NodesDeleted() int         { return 0 }
func (fakeCounters) RelationshipsCreated() int { return 1 }
func (fakeCounters) RelationshipsDeleted() int { return 0 }
func (fakeCounters) PropertiesSet() int        { return 3 }
func (fakeCounters) LabelsAdded() int          { return 2 }
func (fakeCounters) LabelsRemoved() int        { return 0 }
func (fakeCounters) IndexesAdded() int         { return 0 }
func (fakeCounters) IndexesRemoved() int       { return 0 }
func (fakeCounters) ConstraintsAdded() int     { return 0 }
func (fakeCounters) ConstraintsRemoved() int   { return 0 }
func (fakeCounters) SystemUpdates() int        { return 0 }

type fakeSummary struct {
	nd.ResultSummary
	agent string
}

func (s fakeSummary) Server() nd.ServerInfo             { return fakeInfo{s.agent} }
func (fakeSummary) Database() nd.DatabaseInfo           { return fakeDatabase("neo4j") }
func (fakeSummary) QueryType() nd.QueryType             { return nd.QueryTypeReadWrite }
func (fakeSummary) Counters() nd.Counters               { return fakeCounters{} }
func (fakeSummary) ResultAvailableAfter() time.Duration { return 3 * time.Millisecond }
func (fakeSummary) ResultConsumedAfter() time.Duration  { return 7 * time.Millisecond }
func (fakeSummary) Plan() nd.Plan                       { return nil }
func (fakeSummary) QueryProfile() nd.QueryProfile       { return nil }

type fakeResult struct {
	nd.Result
	keys            []string
	rows            []*nd.Record
	pos             int
	err, consumeErr error
	summary         nd.ResultSummary
	consumed        bool
}

func (r *fakeResult) Keys() ([]string, error) { return r.keys, nil }
func (r *fakeResult) Next(context.Context) bool {
	if r.pos >= len(r.rows) {
		return false
	}
	r.pos++
	return true
}
func (r *fakeResult) Record() *nd.Record { return r.rows[r.pos-1] }
func (r *fakeResult) Err() error         { return r.err }
func (r *fakeResult) Consume(context.Context) (nd.ResultSummary, error) {
	r.consumed = true
	return r.summary, r.consumeErr
}

type fakeSession struct {
	nd.Session
	run          func(context.Context, string, map[string]any, ...func(*nd.TransactionConfig)) (nd.Result, error)
	close        func(context.Context) error
	runs, closes atomic.Int32
}

func (s *fakeSession) Run(ctx context.Context, q string, p map[string]any, opts ...func(*nd.TransactionConfig)) (nd.Result, error) {
	s.runs.Add(1)
	return s.run(ctx, q, p, opts...)
}
func (s *fakeSession) Close(ctx context.Context) error {
	s.closes.Add(1)
	if s.close != nil {
		return s.close(ctx)
	}
	return nil
}
func (*fakeSession) LastBookmarks() nd.Bookmarks { return nd.Bookmarks{"original-bookmark"} }

type fakeDriver struct {
	nd.Driver
	info    nd.ServerInfo
	session *fakeSession
	config  nd.SessionConfig
	closes  atomic.Int32
}

func (d *fakeDriver) GetServerInfo(context.Context) (nd.ServerInfo, error) { return d.info, nil }
func (d *fakeDriver) NewSession(_ context.Context, c nd.SessionConfig) nd.Session {
	d.config = c
	return d.session
}
func (d *fakeDriver) Close(context.Context) error { d.closes.Add(1); return nil }
func fakeClient(t *testing.T, r *fakeResult) (*Client, *fakeDriver) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s := &fakeSession{run: func(context.Context, string, map[string]any, ...func(*nd.TransactionConfig)) (nd.Result, error) {
		return r, nil
	}}
	d := &fakeDriver{info: fakeInfo{"Neo4j/5.26.21"}, session: s}
	c := &Client{driver: d, identity: identity(d.info), config: connections.Neo4jConfig{Database: "neo4j", RequestTimeoutSeconds: 40}, ctx: ctx, cancel: cancel}
	t.Cleanup(func() { _ = c.Close() })
	return c, d
}
func normalResult() *fakeResult {
	return &fakeResult{keys: []string{"value"}, rows: []*nd.Record{{Values: []any{map[string]any{"data": []byte{0, 255}}}}}, summary: fakeSummary{agent: "Neo4j/5.26.21"}}
}
func prepareTest(t *testing.T, c *Client, r Request) Plan {
	t.Helper()
	p, e := c.Prepare(r)
	if e != nil {
		t.Fatal(e)
	}
	return p
}

func TestNeo4jOriginalOneUsePlansDeepCopiesAndSummary(t *testing.T) {
	r := normalResult()
	c, d := fakeClient(t, r)
	other, _ := fakeClient(t, normalResult())
	params := map[string]any{"list": []any{[]byte{1, 2}, map[string]any{"x": int64(99)}}}
	p := prepareTest(t, c, Request{Query: "RETURN $list", Parameters: params})
	preview := p.Parameters()
	preview[0].Value.Values[1].Properties[0].Value.Text = "bad"
	params["list"].([]any)[0].([]byte)[0] = 8
	if _, e := other.Execute(context.Background(), p); !errors.Is(e, ErrUsed) {
		t.Fatal(e)
	}
	d.session.run = func(_ context.Context, q string, captured map[string]any, opts ...func(*nd.TransactionConfig)) (nd.Result, error) {
		if q != "RETURN $list" || !reflect.DeepEqual(captured["list"].([]any)[0], []byte{1, 2}) || captured["list"].([]any)[1].(map[string]any)["x"] != int64(99) {
			t.Fatal("immutable dispatch changed")
		}
		cfg := nd.TransactionConfig{}
		for _, opt := range opts {
			opt(&cfg)
		}
		if cfg.Timeout != 40*time.Second {
			t.Fatal("missing native timeout")
		}
		return r, nil
	}
	response, e := c.Execute(context.Background(), p)
	if e != nil {
		t.Fatal(e)
	}
	if !response.Attempted || !response.Completed || response.Uncertain || !r.consumed || response.Summary.Counters["nodes_created"] != 2 || response.Summary.Database != "neo4j" || response.Summary.QueryType != "rw" {
		t.Fatalf("lost native acknowledgement: %#v", response)
	}
	if d.config.DisableAutoCommitRetries == nil || !*d.config.DisableAutoCommitRetries || d.config.DatabaseName != "neo4j" {
		t.Fatal("session permits automatic replay or loses database")
	}
	response.Rows[0][0].Properties[0].Value.Text = "bad"
	response.Summary.Counters["nodes_created"] = 9
	last := c.LastResponse()
	last.Rows[0][0].Properties[0].Value.Text = "bad"
	if c.LastResponse().Rows[0][0].Properties[0].Value.Text != "AP8=" || c.LastResponse().Summary.Counters["nodes_created"] != 2 {
		t.Fatal("response copy aliases original evidence")
	}
	if _, e := c.Execute(context.Background(), p); !errors.Is(e, ErrUsed) || d.session.runs.Load() != 1 {
		t.Fatal("one-use query replayed", e)
	}
}

func TestNeo4jTruncationConsumesNativeCompletionAndRetainsPrefix(t *testing.T) {
	for _, limit := range []string{"rows", "bytes"} {
		t.Run(limit, func(t *testing.T) {
			r := normalResult()
			r.rows = append(r.rows, &nd.Record{Values: []any{"second"}})
			c, _ := fakeClient(t, r)
			req := Request{Query: "CREATE () RETURN 1", MaxRows: 1}
			if limit == "bytes" {
				req.MaxRows = 100
				req.MaxBytes = 1024
				r.rows[1].Values = []any{string(make([]byte, 4000))}
			}
			response, e := c.Execute(context.Background(), prepareTest(t, c, req))
			if limit == "rows" && e != nil || limit == "bytes" && !errors.Is(e, ErrBounds) {
				t.Fatal(e)
			}
			if !response.Truncated || !response.Completed || response.Uncertain || len(response.Rows) != 1 || !r.consumed {
				t.Fatalf("truncation lost completion: %#v", response)
			}
		})
	}
}

func TestNeo4jFailureAndIdentityMismatchNeverReplay(t *testing.T) {
	c, d := fakeClient(t, normalResult())
	native := &nd.Neo4jError{Code: "Neo.ClientError.Statement.SyntaxError", Msg: "native failure"}
	d.session.run = func(context.Context, string, map[string]any, ...func(*nd.TransactionConfig)) (nd.Result, error) {
		return nil, native
	}
	p := prepareTest(t, c, Request{Query: "BAD"})
	response, e := c.Execute(context.Background(), p)
	if !errors.Is(e, native) || response.FailureCode != native.Code || !response.Uncertain || !c.Alive() {
		t.Fatalf("native failure lost: %#v %v", response, e)
	}
	if _, e := c.Execute(context.Background(), p); !errors.Is(e, ErrUsed) || d.session.runs.Load() != 1 {
		t.Fatal("native failure replayed")
	}
	d.info = fakeInfo{"Neo4j/other"}
	p = prepareTest(t, c, Request{Query: "CREATE ()"})
	response, e = c.Execute(context.Background(), p)
	if !errors.Is(e, ErrProtocol) || response.Attempted || c.Alive() || d.session.runs.Load() != 1 {
		t.Fatal("identity drift dispatched query", e)
	}
	r := normalResult()
	r.summary = fakeSummary{agent: "Neo4j/other"}
	other, _ := fakeClient(t, r)
	response, e = other.Execute(context.Background(), prepareTest(t, other, Request{Query: "CREATE ()"}))
	if !errors.Is(e, ErrRetired) || response.Completed || !response.Uncertain {
		t.Fatal("foreign summary proved completion", e)
	}
}

func TestNeo4jServerTerminationRetiresWithoutReplay(t *testing.T) {
	for _, code := range []string{"Neo.TransientError.Transaction.Terminated", "Neo.TransientError.Transaction.LockClientStopped", "Neo.ClientError.Transaction.TransactionTimedOutClientConfiguration"} {
		t.Run(code, func(t *testing.T) {
			c, d := fakeClient(t, normalResult())
			d.session.run = func(context.Context, string, map[string]any, ...func(*nd.TransactionConfig)) (nd.Result, error) {
				return nil, &nd.Neo4jError{Code: code}
			}
			p := prepareTest(t, c, Request{Query: "CREATE ()"})
			response, e := c.Execute(context.Background(), p)
			if !errors.Is(e, ErrRetired) || !response.Uncertain || response.FailureCode != code || c.Alive() || d.session.runs.Load() != 1 || d.closes.Load() != 1 {
				t.Fatal("server termination remained replayable", e)
			}
		})
	}
}

func TestNeo4jOriginalBookmarksFollowAcknowledgedQueries(t *testing.T) {
	c, d := fakeClient(t, normalResult())
	if _, e := c.Execute(context.Background(), prepareTest(t, c, Request{Query: "CREATE ()"})); e != nil {
		t.Fatal(e)
	}
	if _, e := c.Execute(context.Background(), prepareTest(t, c, Request{Query: "RETURN 1", Mode: "read"})); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(d.config.Bookmarks, nd.Bookmarks{"original-bookmark"}) || d.config.AccessMode != nd.AccessModeRead {
		t.Fatal("lost original causal bookmarks")
	}
	_ = c.Close()
	if c.bookmarks != nil {
		t.Fatal("closed connection retained bookmark state")
	}
}

func TestNeo4jCancelAndCloseWaitOriginalAcknowledgement(t *testing.T) {
	for _, action := range []string{"cancel", "close"} {
		t.Run(action, func(t *testing.T) {
			c, d := fakeClient(t, normalResult())
			replacement, rd := fakeClient(t, normalResult())
			entered, acknowledged, canceled := make(chan struct{}), make(chan struct{}), make(chan struct{})
			d.session.run = func(ctx context.Context, _ string, _ map[string]any, _ ...func(*nd.TransactionConfig)) (nd.Result, error) {
				close(entered)
				<-ctx.Done()
				close(canceled)
				<-acknowledged
				return nil, ctx.Err()
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p := prepareTest(t, c, Request{Query: "CREATE ()"})
			done := make(chan Response, 1)
			go func() {
				r, e := c.Execute(ctx, p)
				if !errors.Is(e, ErrRetired) {
					t.Error(e)
				}
				done <- r
			}()
			<-entered
			busy := prepareTest(t, c, Request{Query: "RETURN 1"})
			if _, e := c.Execute(context.Background(), busy); !errors.Is(e, ErrBusy) {
				t.Fatal(e)
			}
			closed := make(chan struct{})
			if action == "close" {
				go func() { _ = c.Close(); close(closed) }()
			} else {
				cancel()
			}
			<-canceled
			select {
			case <-done:
				t.Fatal("cancellation returned before driver acknowledgement")
			default:
			}
			select {
			case <-closed:
				t.Fatal("close returned before original operation settled")
			default:
			}
			if d.closes.Load() != 0 {
				t.Fatal("driver closed concurrently with request")
			}
			close(acknowledged)
			response := <-done
			if action == "close" {
				<-closed
			} else if !c.LastResponse().Uncertain {
				t.Fatal("lost retained original evidence")
			}
			if !response.Attempted || response.Completed || !response.Uncertain || c.Alive() || d.closes.Load() != 1 || d.session.closes.Load() != 1 || d.session.runs.Load() != 1 {
				t.Fatalf("unsafe interrupted outcome: %#v", response)
			}
			if !replacement.Alive() || rd.closes.Load() != 0 {
				t.Fatal("original close affected replacement")
			}
			if _, e := replacement.Execute(context.Background(), prepareTest(t, replacement, Request{Query: "RETURN 1"})); e != nil {
				t.Fatal(e)
			}
			_ = c.Close()
			if d.closes.Load() != 1 {
				t.Fatal("original driver closed twice")
			}
		})
	}
}

// Package neo4j owns native Bolt clients. Arbitrary Cypher uses one auto-commit
// Run, never a retrying managed transaction. A client owns its original driver;
// interrupted operations settle and retire it before explicit recovery.
package neo4j

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	neo4jdriver "github.com/neo4j/neo4j-go-driver/v6/neo4j"
	neo4jauth "github.com/neo4j/neo4j-go-driver/v6/neo4j/auth"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j/config"
	"github.com/nyakang/nyadb/internal/services/connections"
)

var (
	ErrInvalid  = errors.New("invalid Neo4j request or native value")
	ErrProtocol = errors.New("unsupported Neo4j native response or configuration")
	ErrBounds   = errors.New("Neo4j retained query data exceeds bounds")
	ErrRetired  = errors.New("Neo4j connection retired; recover explicitly without replay")
	ErrBusy     = errors.New("Neo4j connection has an active request")
	ErrUsed     = errors.New("Neo4j original query preparation already used or foreign")
)

type Identity struct {
	Agent, Address               string
	ProtocolMajor, ProtocolMinor int
}
type Request struct {
	Query, Database, Mode                        string
	Parameters                                   map[string]any
	MaxRows, MaxBytes, FetchSize, TimeoutSeconds int
}
type Plan struct {
	owner      *Client
	request    Request
	parameters []Property
	attempt    *atomic.Bool
}

func (p Plan) Query() string          { return p.request.Query }
func (p Plan) Parameters() []Property { return cloneProperties(p.parameters) }

type Summary struct {
	Server                        Identity
	Database, QueryType           string
	Counters                      map[string]int64
	AvailableAfter, ConsumedAfter time.Duration
	Bookmarks                     []string
}
type Response struct {
	Columns                         []string
	Rows                            [][]Value
	Summary                         Summary
	FailureCode                     string
	Attempted, Completed, Truncated bool
	// Uncertain is conservative for arbitrary Cypher, including procedures and
	// queries with side effects. A missing summary does not prove rollback.
	Uncertain bool
}
type Interrupted struct {
	Cause          error
	OutcomeUnknown bool
}

func (e *Interrupted) Error() string {
	return "Neo4j request interrupted; recover explicitly and verify without replay"
}
func (e *Interrupted) Unwrap() error        { return e.Cause }
func (e *Interrupted) Is(target error) bool { return target == ErrRetired }

type Client struct {
	operation       sync.Mutex
	mu              sync.Mutex
	driver          neo4jdriver.Driver
	config          connections.Neo4jConfig
	identity        Identity
	ctx             context.Context
	cancel          context.CancelFunc
	closed, retired bool
	driverClosed    bool
	closeOnce       sync.Once
	closeErr        error
	last            Response
	bookmarks       []string
}

func identity(info neo4jdriver.ServerInfo) Identity {
	p := info.ProtocolVersion()
	return Identity{info.Agent(), info.Address(), p.Major, p.Minor}
}
func readCertificate(path string) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, ErrInvalid
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if e != nil || len(b) > 1<<20 {
		return nil, ErrBounds
	}
	return b, nil
}
func Open(ctx context.Context, cfg connections.Neo4jConfig, secret string) (*Client, error) {
	cfg, e := connections.NormalizeNeo4jConfig(cfg)
	if e != nil {
		return nil, e
	}
	if e = connections.ValidateNeo4jCredential(cfg, secret); e != nil {
		return nil, e
	}
	u, _ := url.Parse(cfg.URI)
	// The pinned driver derives SNI and verification hostname from the URI.
	// Reject a conflicting override instead of silently ignoring it.
	if cfg.ServerName != "" && cfg.ServerName != u.Hostname() {
		return nil, ErrProtocol
	}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.CAFile != "" {
		pem, e := readCertificate(cfg.CAFile)
		if e != nil {
			return nil, e
		}
		roots, e := x509.SystemCertPool()
		if e != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, ErrInvalid
		}
		tlsCfg.RootCAs = roots
	}
	var cert neo4jauth.ClientCertificateProvider
	if cfg.ClientCertFile != "" {
		cert, e = neo4jauth.NewStaticClientCertificateProvider(neo4jauth.ClientCertificate{CertFile: cfg.ClientCertFile, KeyFile: cfg.ClientKeyFile})
		if e != nil {
			return nil, ErrInvalid
		}
	}
	auth := neo4jdriver.NoAuth()
	if cfg.Auth == "basic" {
		auth = neo4jdriver.BasicAuth(cfg.Username, secret, "")
	}
	d, e := neo4jdriver.NewDriver(cfg.URI, auth, func(c *config.Config) {
		c.TelemetryDisabled = true
		c.MaxTransactionRetryTime = 0
		c.DisableAutoCommitRetries = true
		c.MaxConnectionPoolSize = 1
		c.SocketConnectTimeout = time.Duration(cfg.ConnectTimeoutSeconds) * time.Second
		c.ConnectionAcquisitionTimeout = time.Duration(cfg.ConnectTimeoutSeconds) * time.Second
		c.TlsConfig = tlsCfg
		c.ClientCertificateProvider = cert
	})
	if e != nil {
		return nil, ErrInvalid
	}
	openCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.ConnectTimeoutSeconds)*time.Second)
	defer cancel()
	info, e := d.GetServerInfo(openCtx)
	if e == nil && (info == nil || !strings.HasPrefix(info.Agent(), "Neo4j/")) {
		e = ErrProtocol
	}
	if e != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = d.Close(closeCtx)
		return nil, e
	}
	base, cancelBase := context.WithCancel(context.Background())
	return &Client{driver: d, config: cfg, identity: identity(info), ctx: base, cancel: cancelBase}, nil
}
func (c *Client) Identity() Identity { c.mu.Lock(); defer c.mu.Unlock(); return c.identity }
func (c *Client) Alive() bool        { c.mu.Lock(); defer c.mu.Unlock(); return !c.closed && !c.retired }
func (c *Client) Prepare(r Request) (Plan, error) {
	if !c.Alive() {
		return Plan{}, ErrRetired
	}
	if strings.TrimSpace(r.Query) == "" || !utf8.ValidString(r.Query) || len(r.Query) > 8<<20 || strings.ContainsRune(r.Query, 0) || len(r.Database) > 4096 || !utf8.ValidString(r.Database) || strings.ContainsRune(r.Database, 0) {
		return Plan{}, ErrInvalid
	}
	if r.Database == "" {
		r.Database = c.config.Database
	}
	if r.Mode == "" {
		r.Mode = "write"
	}
	if r.Mode != "read" && r.Mode != "write" {
		return Plan{}, ErrInvalid
	}
	if r.MaxRows == 0 {
		r.MaxRows = 1000
	}
	if r.MaxBytes == 0 {
		r.MaxBytes = 16 << 20
	}
	if r.FetchSize == 0 {
		r.FetchSize = 256
	}
	if r.TimeoutSeconds == 0 {
		r.TimeoutSeconds = c.config.RequestTimeoutSeconds
	}
	if r.MaxRows < 1 || r.MaxRows > 100000 || r.MaxBytes < 1 || r.MaxBytes > 64<<20 || r.FetchSize < 1 || r.FetchSize > 10000 || r.TimeoutSeconds < 1 || r.TimeoutSeconds > 600 {
		return Plan{}, ErrBounds
	}
	b := valueBudget{limit: 1 << 20}
	props, params, e := b.properties(r.Parameters, false, 0)
	if e != nil {
		return Plan{}, e
	}
	r.Parameters = params
	return Plan{c, r, props, &atomic.Bool{}}, nil
}
func cloneResponse(r Response) Response {
	r.Columns = slices.Clone(r.Columns)
	r.Rows = slices.Clone(r.Rows)
	for i := range r.Rows {
		r.Rows[i] = slices.Clone(r.Rows[i])
		for j := range r.Rows[i] {
			r.Rows[i][j] = cloneValue(r.Rows[i][j])
		}
	}
	r.Summary.Counters = func() map[string]int64 {
		out := make(map[string]int64, len(r.Summary.Counters))
		for k, v := range r.Summary.Counters {
			out[k] = v
		}
		return out
	}()
	r.Summary.Bookmarks = slices.Clone(r.Summary.Bookmarks)
	return r
}
func (c *Client) LastResponse() Response {
	c.mu.Lock()
	defer c.mu.Unlock()
	return cloneResponse(c.last)
}
func (c *Client) Execute(parent context.Context, p Plan) (response Response, err error) {
	if e := parent.Err(); e != nil {
		return response, e
	}
	if !c.operation.TryLock() {
		return response, ErrBusy
	}
	defer c.operation.Unlock()
	if !c.Alive() {
		return response, ErrRetired
	}
	if p.owner != c || p.attempt == nil || !p.attempt.CompareAndSwap(false, true) {
		return response, ErrUsed
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(p.request.TimeoutSeconds)*time.Second)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	defer func() {
		if err != nil {
			var native *neo4jdriver.Neo4jError
			terminated := false
			if errors.As(err, &native) {
				response.FailureCode = native.Code
				terminated = strings.Contains(native.Code, ".Transaction.Terminated") || strings.Contains(native.Code, ".Transaction.TransactionTimedOut") || strings.HasSuffix(native.Code, ".Transaction.LockClientStopped")
			}
			response.Uncertain = response.Attempted && !response.Completed
			if ctx.Err() != nil || terminated || neo4jdriver.IsConnectivityError(err) || errors.Is(err, ErrProtocol) {
				c.mu.Lock()
				c.retired = true
				c.mu.Unlock()
				if response.Attempted {
					err = &Interrupted{err, response.Uncertain}
				}
				cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				closeErr := c.driver.Close(cleanup)
				cancel()
				c.driverClosed = true
				err = errors.Join(err, closeErr)
			}
		}
		c.mu.Lock()
		c.last = cloneResponse(response)
		c.mu.Unlock()
	}()
	info, e := c.driver.GetServerInfo(ctx)
	if e != nil {
		return response, e
	}
	if info == nil {
		return response, ErrProtocol
	}
	observed := identity(info)
	if observed.Agent != c.identity.Agent || observed.ProtocolMajor != c.identity.ProtocolMajor || observed.ProtocolMinor != c.identity.ProtocolMinor {
		return response, ErrProtocol
	}
	mode := neo4jdriver.AccessModeWrite
	if p.request.Mode == "read" {
		mode = neo4jdriver.AccessModeRead
	}
	noRetry := true
	c.mu.Lock()
	bookmarks := slices.Clone(c.bookmarks)
	c.mu.Unlock()
	session := c.driver.NewSession(ctx, neo4jdriver.SessionConfig{DatabaseName: p.request.Database, AccessMode: mode, FetchSize: p.request.FetchSize, Bookmarks: bookmarks, DisableAutoCommitRetries: &noRetry})
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		closeErr := session.Close(cleanup)
		if closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	if e = ctx.Err(); e != nil {
		return response, e
	}
	response.Attempted = true
	result, e := session.Run(ctx, p.request.Query, p.request.Parameters, func(t *neo4jdriver.TransactionConfig) {
		t.Timeout = time.Duration(p.request.TimeoutSeconds) * time.Second
	})
	if e != nil {
		return response, e
	}
	keys, e := result.Keys()
	if e != nil {
		return response, e
	}
	budget := valueBudget{limit: p.request.MaxBytes}
	var captureErr error
	if len(keys) > 100000 {
		captureErr = ErrBounds
	}
	for _, key := range keys {
		if captureErr != nil {
			break
		}
		if !utf8.ValidString(key) {
			captureErr = ErrProtocol
			break
		}
		if e := budget.add(len(key) + 16); e != nil {
			captureErr = e
			break
		}
	}
	retained := 0
	if captureErr == nil {
		encoded, _ := json.Marshal(keys)
		retained = len(encoded)
		if retained > p.request.MaxBytes {
			captureErr = ErrBounds
		} else {
			response.Columns = slices.Clone(keys)
		}
	}
	if captureErr != nil {
		response.Truncated = true
	}
	for captureErr == nil && result.Next(ctx) {
		if len(response.Rows) >= p.request.MaxRows {
			response.Truncated = true
			break
		}
		record := result.Record()
		if record == nil || len(record.Values) != len(keys) {
			captureErr = ErrProtocol
			break
		}
		row := make([]Value, len(record.Values))
		for i, raw := range record.Values {
			value, _, e := budget.capture(raw, true, 0)
			if e != nil {
				captureErr = e
				break
			}
			row[i] = value
		}
		if captureErr != nil {
			response.Truncated = true
			break
		}
		encoded, e := json.Marshal(row)
		if e != nil {
			captureErr = e
			break
		}
		retained += len(encoded)
		if retained > p.request.MaxBytes {
			response.Truncated = true
			break
		}
		response.Rows = append(response.Rows, row)
	}
	if e = result.Err(); e != nil {
		return response, e
	}
	summary, e := result.Consume(ctx)
	if e != nil {
		return response, e
	}
	if summary == nil || summary.Server() == nil || summary.Counters() == nil {
		return response, ErrProtocol
	}
	confirmed := identity(summary.Server())
	if confirmed.Agent != c.identity.Agent || confirmed.ProtocolMajor != c.identity.ProtocolMajor || confirmed.ProtocolMinor != c.identity.ProtocolMinor {
		return response, ErrProtocol
	}
	response.Completed = true
	counts := summary.Counters()
	response.Summary = Summary{Server: identity(summary.Server()), QueryType: summary.QueryType().String(), AvailableAfter: summary.ResultAvailableAfter(), ConsumedAfter: summary.ResultConsumedAfter(), Counters: map[string]int64{
		"nodes_created": int64(counts.NodesCreated()), "nodes_deleted": int64(counts.NodesDeleted()), "relationships_created": int64(counts.RelationshipsCreated()), "relationships_deleted": int64(counts.RelationshipsDeleted()), "properties_set": int64(counts.PropertiesSet()), "labels_added": int64(counts.LabelsAdded()), "labels_removed": int64(counts.LabelsRemoved()), "indexes_added": int64(counts.IndexesAdded()), "indexes_removed": int64(counts.IndexesRemoved()), "constraints_added": int64(counts.ConstraintsAdded()), "constraints_removed": int64(counts.ConstraintsRemoved()), "system_updates": int64(counts.SystemUpdates())}, Bookmarks: slices.Clone(session.LastBookmarks())}
	if db := summary.Database(); db != nil {
		response.Summary.Database = db.Name()
	}
	c.mu.Lock()
	c.bookmarks = slices.Clone(response.Summary.Bookmarks)
	c.mu.Unlock()
	return response, captureErr
}
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
		c.cancel()
		c.operation.Lock()
		defer c.operation.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if !c.driverClosed {
			c.closeErr = c.driver.Close(ctx)
			c.driverClosed = true
		}
		c.mu.Lock()
		c.driver = nil
		c.last = Response{}
		c.bookmarks = nil
		c.mu.Unlock()
	})
	return c.closeErr
}
func (r Response) String() string {
	return fmt.Sprintf("Neo4j result: %d retained rows; attempted=%t completed=%t truncated=%t uncertain=%t", len(r.Rows), r.Attempted, r.Completed, r.Truncated, r.Uncertain)
}

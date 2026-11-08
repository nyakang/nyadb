// Package qdrant implements original native REST connections. Each request
// uses a fresh socket and is submitted once; cancellation waits for its HTTP
// executor and requires explicit recovery without replay.
package qdrant

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

var (
	ErrInvalid  = errors.New("invalid Qdrant request")
	ErrProtocol = errors.New("invalid or unsupported Qdrant native response")
	ErrBounds   = errors.New("Qdrant request or response exceeds bounds")
	ErrRetired  = errors.New("Qdrant connection retired; recover explicitly without replay")
	ErrBusy     = errors.New("Qdrant connection has an active request")
)

const requestLimit = 8 << 20
const responseLimit = 16 << 20

// Identity observes the binary, not a cluster, server or collection incarnation.
type Identity struct {
	Title   string `json:"title"`
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

type Request struct {
	Method, Path, ContentType string
	Body                      []byte
}
type Response struct {
	Status      int
	ContentType string
	Body        []byte
	Incomplete  bool
}
type HTTPError struct{ Status int }

func (e *HTTPError) Error() string { return fmt.Sprintf("Qdrant HTTP %d", e.Status) }

// Interrupted is returned after the original executor settles. Server writes
// may have completed or been admitted; local cancellation is not rollback.
type Interrupted struct {
	WriteOutcomeUnknown bool
	Cause               error
}

func (e *Interrupted) Error() string {
	return "Qdrant request interrupted; recover explicitly and verify writes without replay"
}
func (e *Interrupted) Unwrap() error        { return e.Cause }
func (e *Interrupted) Is(target error) bool { return target == ErrRetired }

type Client struct {
	operation       sync.Mutex
	mu              sync.Mutex
	config          connections.QdrantConfig
	secret          string
	http            *http.Client
	ctx             context.Context
	cancel          context.CancelFunc
	identity        Identity
	closed, retired bool
}

func readFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrInvalid
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return nil, ErrInvalid
	}
	return raw, nil
}

func Open(ctx context.Context, cfg connections.QdrantConfig, secret string) (*Client, error) {
	cfg, err := connections.NormalizeQdrantConfig(cfg)
	if err != nil {
		return nil, err
	}
	if err = connections.ValidateQdrantCredential(cfg, secret, true); err != nil {
		return nil, err
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: cfg.ServerName}
	if cfg.CAFile != "" {
		raw, e := readFile(cfg.CAFile)
		pool := x509.NewCertPool()
		if e != nil || !pool.AppendCertsFromPEM(raw) {
			return nil, errors.New("invalid Qdrant CA")
		}
		tc.RootCAs = pool
	}
	if cfg.ClientCertFile != "" {
		cert, e1 := readFile(cfg.ClientCertFile)
		key, e2 := readFile(cfg.ClientKeyFile)
		pair, e3 := tls.X509KeyPair(cert, key)
		if e1 != nil || e2 != nil || e3 != nil {
			return nil, errors.New("invalid Qdrant client certificate")
		}
		tc.Certificates = []tls.Certificate{pair}
	}
	connect := time.Duration(cfg.ConnectTimeoutSeconds) * time.Second
	transport := &http.Transport{Proxy: nil, TLSClientConfig: tc, DialContext: (&net.Dialer{Timeout: connect}).DialContext, TLSHandshakeTimeout: connect, ResponseHeaderTimeout: time.Duration(cfg.RequestTimeoutSeconds) * time.Second, MaxResponseHeaderBytes: 64 << 10, DisableKeepAlives: true}
	root, cancel := context.WithCancel(context.Background())
	c := &Client{config: cfg, secret: secret, ctx: root, cancel: cancel, http: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	identity, err := c.observe(ctx)
	if err != nil {
		c.Close()
		return nil, err
	}
	c.identity = identity
	return c, nil
}

func validIdentity(v Identity) bool {
	if v.Title != "qdrant - vector search engine" || len(v.Version) > 32 || len(v.Commit) < 7 || len(v.Commit) > 40 {
		return false
	}
	parts := strings.Split(v.Version, ".")
	if len(parts) != 3 || parts[0] != "1" {
		return false
	}
	for _, part := range parts {
		n, err := strconv.ParseUint(part, 10, 32)
		if err != nil || strconv.FormatUint(n, 10) != part {
			return false
		}
	}
	minor, _ := strconv.Atoi(parts[1])
	if minor < 20 {
		return false
	}
	for _, ch := range v.Commit {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return false
		}
	}
	return true
}

func (c *Client) observe(ctx context.Context) (Identity, error) {
	r, err := c.Do(ctx, Request{Method: "GET", Path: "/"})
	if err != nil {
		return Identity{}, err
	}
	var identity Identity
	if decodeJSON(r.Body, &identity) != nil || !validIdentity(identity) {
		return Identity{}, ErrProtocol
	}
	// The root endpoint can be unauthenticated. Native collection enumeration
	// proves the supplied credentials have actual browse permission.
	if _, _, err := c.Collections(ctx); err != nil {
		return Identity{}, err
	}
	return identity, nil
}
func (c *Client) Identity() Identity { return c.identity }

func readOnly(r Request) bool { return r.Method == "GET" || r.Method == "HEAD" }
func (c *Client) Do(ctx context.Context, r Request) (Response, error) {
	return c.do(ctx, r, readOnly(r))
}

// Constructed native codecs and exact native point-query routes certify POST
// reads. Arbitrary REST requests retain unknown write outcome on interruption.
func (c *Client) do(ctx context.Context, r Request, read bool) (Response, error) {
	if err := validateRequest(r); err != nil {
		return Response{}, err
	}
	if !c.operation.TryLock() {
		return Response{}, ErrBusy
	}
	defer c.operation.Unlock()
	c.mu.Lock()
	unavailable, secret := c.closed || c.retired, c.secret
	c.mu.Unlock()
	if unavailable {
		return Response{}, ErrRetired
	}
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	child, cancel := context.WithTimeout(ctx, time.Duration(c.config.RequestTimeoutSeconds)*time.Second)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	req, err := http.NewRequestWithContext(child, r.Method, c.config.Endpoint+r.Path, bytes.NewReader(bytes.Clone(r.Body)))
	if err != nil {
		return Response{}, ErrInvalid
	}
	// No GetBody, reused sockets, redirects or environment proxies: net/http
	// cannot silently resubmit a mutating request to a different executor.
	req.GetBody = nil
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.config.Auth == "api_key" {
		req.Header.Set("api-key", secret)
	}
	result := Response{}
	interrupted := func(cause error) (Response, error) {
		c.retire()
		return result, &Interrupted{WriteOutcomeUnknown: !read, Cause: cause}
	}
	response, err := c.http.Do(req)
	if err != nil {
		if child.Err() != nil {
			return interrupted(child.Err())
		}
		return interrupted(errors.New("Qdrant HTTP transport failed"))
	}
	defer response.Body.Close()
	result.Status, result.ContentType = response.StatusCode, response.Header.Get("Content-Type")
	result.Body, err = io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	if err != nil {
		// Partial bytes remain original evidence, never a success payload.
		result.Incomplete = true
		if child.Err() != nil {
			return interrupted(child.Err())
		}
		return interrupted(errors.New("Qdrant response truncated"))
	}
	if len(result.Body) > responseLimit {
		result.Body = result.Body[:responseLimit]
		result.Incomplete = true
		return interrupted(ErrBounds)
	}
	if err := child.Err(); err != nil {
		return interrupted(err)
	}
	if result.Status < 200 || result.Status >= 300 {
		diagnostic := &HTTPError{Status: result.Status}
		// No general rollback guarantee exists for arbitrary REST failures.
		if !read {
			return interrupted(diagnostic)
		}
		return result, diagnostic
	}
	return result, nil
}
func (c *Client) retire() { c.mu.Lock(); c.retired = true; c.mu.Unlock() }

func (c *Client) Close() {
	c.mu.Lock()
	c.closed = true
	c.cancel()
	c.mu.Unlock()
	c.operation.Lock()
	c.http.CloseIdleConnections()
	c.mu.Lock()
	c.secret = ""
	c.mu.Unlock()
	c.operation.Unlock()
}

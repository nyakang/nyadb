// Package meilisearch implements native HTTP requests and asynchronous task
// receipts. It does not equate task admission with committed document changes.
package meilisearch

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/services/connections"
)

var (
	ErrInvalid     = errors.New("invalid Meilisearch request")
	ErrProtocol    = errors.New("invalid or unsupported Meilisearch native response")
	ErrBounds      = errors.New("Meilisearch request or response exceeds bounds")
	ErrRetired     = errors.New("Meilisearch connection retired; recover explicitly without replay")
	ErrBusy        = errors.New("Meilisearch connection has an active request")
	ErrTaskChanged = errors.New("original Meilisearch task receipt no longer matches")
)

const requestLimit = 8 << 20
const responseLimit = 16 << 20

// Identity observes the binary version, not a server or index incarnation.
// Its zero value means an explicit index-scoped handshake has no global
// version observation; it must not be presented as verified binary identity.
type Identity struct {
	Version    string `json:"pkgVersion"`
	CommitSHA  string `json:"commitSha"`
	CommitDate string `json:"commitDate"`
}

type Request struct {
	Method, Path, ContentType string
	Body                      []byte
}

type Response struct {
	Status            int
	ContentType, ETag string
	Body              []byte
}

type HTTPError struct{ Status int }

func (e *HTTPError) Error() string { return fmt.Sprintf("Meilisearch HTTP %d", e.Status) }

// Interrupted is returned only after the original HTTP executor has settled.
// A mutating request may already have enqueued a task or changed API keys.
type Interrupted struct {
	WriteOutcomeUnknown bool
	Cause               error
}

func (e *Interrupted) Error() string {
	return "Meilisearch request interrupted; recover explicitly and inspect tasks without replay"
}
func (e *Interrupted) Unwrap() error        { return e.Cause }
func (e *Interrupted) Is(target error) bool { return target == ErrRetired }

type Client struct {
	operation, waiter sync.Mutex
	mu                sync.Mutex
	work              sync.WaitGroup
	config            connections.MeilisearchConfig
	secret            string
	http              *http.Client
	ctx               context.Context
	cancel            context.CancelFunc
	identity          Identity
	closed, retired   bool
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

func Open(ctx context.Context, cfg connections.MeilisearchConfig, secret string) (*Client, error) {
	cfg, err := connections.NormalizeMeilisearchConfig(cfg)
	if err != nil {
		return nil, err
	}
	if err = connections.ValidateMeilisearchCredential(cfg, secret, true); err != nil {
		return nil, err
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: cfg.ServerName}
	if cfg.CAFile != "" {
		raw, err := readFile(cfg.CAFile)
		pool := x509.NewCertPool()
		if err != nil || !pool.AppendCertsFromPEM(raw) {
			return nil, errors.New("invalid Meilisearch CA")
		}
		tc.RootCAs = pool
	}
	if cfg.ClientCertFile != "" {
		cert, e1 := readFile(cfg.ClientCertFile)
		key, e2 := readFile(cfg.ClientKeyFile)
		pair, err := tls.X509KeyPair(cert, key)
		if e1 != nil || e2 != nil || err != nil {
			return nil, errors.New("invalid Meilisearch client certificate")
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
	parts := strings.Split(v.Version, ".")
	if len(parts) != 3 || parts[0] != "1" || len(v.Version) > 32 || len(v.CommitSHA) != 40 || len(v.CommitDate) == 0 || len(v.CommitDate) > 64 {
		return false
	}
	for _, part := range parts {
		if n, err := strconv.ParseUint(part, 10, 32); err != nil || strconv.FormatUint(n, 10) != part {
			return false
		}
	}
	for _, ch := range v.CommitSHA {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return false
		}
	}
	return true
}

// Health is insufficient. Global handshakes require version and indexes.get;
// explicit scoped handshakes verify the configured native index instead.
func (c *Client) observe(ctx context.Context) (Identity, error) {
	r, err := c.Do(ctx, Request{Method: "GET", Path: "/health"})
	if err != nil {
		return Identity{}, err
	}
	health, err := nativeObject(r.Body)
	status, statusErr := nativeString(health["status"])
	if err != nil || statusErr != nil || status != "available" {
		return Identity{}, ErrProtocol
	}
	if c.config.IndexUID != "" {
		r, err := c.Do(ctx, Request{Method: "GET", Path: "/indexes/" + c.config.IndexUID})
		if err != nil {
			return Identity{}, err
		}
		fields, err := nativeObject(r.Body)
		uid, uidErr := nativeString(fields["uid"])
		created, createdErr := nativeString(fields["createdAt"])
		updated, updatedErr := nativeString(fields["updatedAt"])
		_, e1 := time.Parse(time.RFC3339Nano, created)
		_, e2 := time.Parse(time.RFC3339Nano, updated)
		if err != nil || uidErr != nil || uid != c.config.IndexUID || createdErr != nil || updatedErr != nil || e1 != nil || e2 != nil {
			return Identity{}, ErrProtocol
		}
		return Identity{}, nil
	}
	r, err = c.Do(ctx, Request{Method: "GET", Path: "/version"})
	if err != nil {
		return Identity{}, err
	}
	version, err := nativeObject(r.Body)
	identity := Identity{}
	identity.Version, statusErr = nativeString(version["pkgVersion"])
	identity.CommitSHA, _ = nativeString(version["commitSha"])
	identity.CommitDate, _ = nativeString(version["commitDate"])
	if err != nil || statusErr != nil || !validIdentity(identity) {
		return Identity{}, ErrProtocol
	}
	r, err = c.Do(ctx, Request{Method: "GET", Path: "/indexes?offset=0&limit=1"})
	if err != nil {
		return Identity{}, err
	}
	indexes, err := nativeObject(r.Body)
	var results []json.RawMessage
	if err != nil || decodeJSON(indexes["results"], &results) != nil || results == nil || len(results) > 1 {
		return Identity{}, ErrProtocol
	}
	o, e1 := nativeUint(indexes["offset"], 64)
	l, e2 := nativeUint(indexes["limit"], 64)
	total, e3 := nativeUint(indexes["total"], 64)
	if e1 != nil || e2 != nil || e3 != nil || o != 0 || l != 1 || total < uint64(len(results)) {
		return Identity{}, ErrProtocol
	}
	return identity, nil
}

func (c *Client) Identity() Identity { return c.identity }

func validateRequest(r Request) error {
	switch r.Method {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE":
	default:
		return ErrInvalid
	}
	if len(r.Body) > requestLimit || (r.Method == "GET" || r.Method == "HEAD") && len(r.Body) != 0 || len(r.Path) == 0 || len(r.Path) > 8192 || !utf8.ValidString(r.Path) || !strings.HasPrefix(r.Path, "/") || strings.HasPrefix(r.Path, "//") || strings.ContainsAny(r.Path, "\\#") || strings.IndexFunc(r.Path, func(ch rune) bool { return unicode.IsSpace(ch) || unicode.IsControl(ch) }) >= 0 {
		return ErrInvalid
	}
	u, err := url.Parse(r.Path)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" || u.Fragment != "" || u.RawPath != "" || strings.Contains(u.Path, "//") || !utf8.ValidString(u.Path) {
		return ErrInvalid
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." || strings.ContainsAny(part, "\\%") || strings.IndexFunc(part, unicode.IsControl) >= 0 {
			return ErrInvalid
		}
	}
	if _, err := url.ParseQuery(u.RawQuery); err != nil {
		return ErrInvalid
	}
	if r.ContentType != "" && r.ContentType != "application/json" {
		return ErrInvalid
	}
	if len(r.Body) != 0 {
		var raw json.RawMessage
		if decodeJSON(r.Body, &raw) != nil {
			return ErrInvalid
		}
	}
	return nil
}

func readOnly(r Request) bool { return r.Method == "GET" || r.Method == "HEAD" }
func (c *Client) Do(ctx context.Context, r Request) (Response, error) {
	return c.do(ctx, r, readOnly(r))
}

// Only a native codec may certify a constructed search POST as a read.
func (c *Client) do(ctx context.Context, r Request, read bool) (Response, error) {
	if err := validateRequest(r); err != nil {
		return Response{}, err
	}
	if !c.operation.TryLock() {
		return Response{}, ErrBusy
	}
	defer c.operation.Unlock()
	c.mu.Lock()
	unavailable := c.closed || c.retired
	secret := c.secret
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
	r.Body = bytes.Clone(r.Body)
	req, err := http.NewRequestWithContext(child, r.Method, c.config.Endpoint+r.Path, bytes.NewReader(r.Body))
	if err != nil {
		return Response{}, ErrInvalid
	}
	req.GetBody = nil
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.config.Auth == "bearer" {
		req.Header.Set("Authorization", "Bearer "+secret)
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
		return interrupted(errors.New("Meilisearch HTTP transport failed"))
	}
	defer response.Body.Close()
	result.Status, result.ContentType, result.ETag = response.StatusCode, response.Header.Get("Content-Type"), response.Header.Get("ETag")
	result.Body, err = io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	if err != nil {
		result.Body = nil
		return interrupted(errors.New("Meilisearch response truncated"))
	}
	if len(result.Body) > responseLimit {
		result.Body = nil
		return interrupted(ErrBounds)
	}
	if err := child.Err(); err != nil {
		return interrupted(err)
	}
	if result.Status < 200 || result.Status >= 300 {
		diagnostic := &HTTPError{Status: result.Status}
		if !read {
			return interrupted(diagnostic)
		}
		return result, diagnostic
	}
	return result, nil
}

func (c *Client) retire() { c.mu.Lock(); c.retired = true; c.mu.Unlock() }

// Close waits for the original HTTP executor and explicit task-wait worker.
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
	c.work.Wait()
}

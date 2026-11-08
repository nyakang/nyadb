// Package couchdb implements the native CouchDB HTTP transport. Each request
// uses a fresh connection, disables redirects/proxies and is submitted once.
package couchdb

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
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/services/connections"
)

var (
	ErrRetired  = errors.New("CouchDB connection retired; recover explicitly without replay")
	ErrBusy     = errors.New("CouchDB connection has an active request")
	ErrBounds   = errors.New("CouchDB request or response exceeds bounds")
	ErrProtocol = errors.New("invalid or unsupported CouchDB native response")
	ErrInvalid  = errors.New("invalid CouchDB request")
)

const requestLimit = 8 << 20
const responseLimit = 16 << 20

// Identity is an observation of the original endpoint's node. UUID is neither
// a cluster identity nor a database incarnation, and survives node restarts.
type Identity struct {
	CouchDB string `json:"couchdb"`
	UUID    string `json:"uuid"`
	Version string `json:"version"`
	GitSHA  string `json:"git_sha"`
	Vendor  struct {
		Name string `json:"name"`
	} `json:"vendor"`
}

type Principal struct {
	Name  *string  `json:"name"`
	Roles []string `json:"roles"`
}

type Request struct {
	Method, Path, ContentType string
	Body                      []byte
}

// Response preserves complete native bytes, including non-success statuses.
// HTTP success alone does not certify individual _bulk_docs writes.
type Response struct {
	Status            int
	ContentType, ETag string
	Body              []byte
}

type HTTPError struct{ Status int }

func (e *HTTPError) Error() string { return fmt.Sprintf("CouchDB HTTP %d", e.Status) }

// Interrupted means the original HTTP worker has settled. The server may have
// committed writes; local cancellation does not acknowledge remote rollback.
type Interrupted struct {
	WriteOutcomeUnknown bool
	Cause               error
}

func (e *Interrupted) Error() string {
	return "CouchDB request interrupted; recover explicitly and verify writes without replay"
}
func (e *Interrupted) Unwrap() error        { return e.Cause }
func (e *Interrupted) Is(target error) bool { return target == ErrRetired }

type Client struct {
	operation       sync.Mutex
	mu              sync.Mutex
	config          connections.CouchDBConfig
	secret          string
	http            *http.Client
	ctx             context.Context
	cancel          context.CancelFunc
	identity        Identity
	principal       Principal
	closed, retired bool
}

func Open(ctx context.Context, cfg connections.CouchDBConfig, secret string) (*Client, error) {
	cfg, err := connections.NormalizeCouchDBConfig(cfg)
	if err != nil {
		return nil, err
	}
	if err := connections.ValidateCouchDBCredential(cfg, secret, true); err != nil {
		return nil, err
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: cfg.ServerName}
	if cfg.CAFile != "" {
		file, err := os.Open(cfg.CAFile)
		if err != nil {
			return nil, errors.New("CouchDB CA cannot be read")
		}
		raw, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
		_ = file.Close()
		pool := x509.NewCertPool()
		if err != nil || len(raw) > 1<<20 || !pool.AppendCertsFromPEM(raw) {
			return nil, errors.New("invalid CouchDB CA")
		}
		tlsConfig.RootCAs = pool
	}
	// Disable reuse and GetBody: net/http must not replay on a reused socket.
	transport := &http.Transport{Proxy: nil, TLSClientConfig: tlsConfig, DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 40 * time.Second, MaxResponseHeaderBytes: 64 << 10, DisableKeepAlives: true}
	root, cancel := context.WithCancel(context.Background())
	c := &Client{config: cfg, secret: secret, ctx: root, cancel: cancel, http: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	fail := func(err error) (*Client, error) { c.Close(); return nil, err }
	identity, principal, err := c.observe(ctx)
	if err != nil {
		return fail(err)
	}
	c.identity, c.principal = identity, principal
	return c, nil
}

// observe checks the native node and actual principal on the captured client.
// Neither observation is atomic with a later request or a database incarnation.
func (c *Client) observe(ctx context.Context) (Identity, Principal, error) {
	var identity Identity
	r, err := c.Do(ctx, Request{Method: "GET", Path: "/"})
	if err != nil {
		return Identity{}, Principal{}, err
	}
	if decodeJSON(r.Body, &identity) != nil || !validIdentity(identity) {
		return Identity{}, Principal{}, ErrProtocol
	}
	r, err = c.Do(ctx, Request{Method: "GET", Path: "/_session"})
	if err != nil {
		return Identity{}, Principal{}, err
	}
	var session struct {
		OK      bool      `json:"ok"`
		UserCtx Principal `json:"userCtx"`
		Info    struct {
			Authenticated string `json:"authenticated"`
		} `json:"info"`
	}
	if decodeJSON(r.Body, &session) != nil || !session.OK || session.UserCtx.Roles == nil {
		return Identity{}, Principal{}, ErrProtocol
	}
	if c.config.Auth == "basic" {
		if session.UserCtx.Name == nil || *session.UserCtx.Name != c.config.Username || session.Info.Authenticated != "default" {
			return Identity{}, Principal{}, ErrProtocol
		}
	} else if session.UserCtx.Name != nil || session.Info.Authenticated != "" {
		return Identity{}, Principal{}, ErrProtocol
	}
	return identity, session.UserCtx, nil
}

func validIdentity(v Identity) bool {
	if v.CouchDB != "Welcome" || v.Vendor.Name != "The Apache Software Foundation" || len(v.UUID) != 32 || v.Version == "" {
		return false
	}
	for _, ch := range v.UUID {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return false
		}
	}
	return true
}

func (c *Client) Identity() Identity { return c.identity }
func (c *Client) Principal() Principal {
	v := c.principal
	v.Roles = append([]string{}, v.Roles...)
	if v.Name != nil {
		n := *v.Name
		v.Name = &n
	}
	return v
}

func validateRequest(r Request) error {
	switch r.Method {
	case "GET", "HEAD", "POST", "PUT", "DELETE":
	default:
		return ErrInvalid
	}
	if len(r.Body) > requestLimit || len(r.Path) == 0 || len(r.Path) > 8192 || !utf8.ValidString(r.Path) || strings.IndexFunc(r.Path, func(ch rune) bool { return unicode.IsControl(ch) || unicode.IsSpace(ch) }) >= 0 || strings.ContainsAny(r.Path, "\\#") || !strings.HasPrefix(r.Path, "/") || strings.HasPrefix(r.Path, "//") {
		return ErrInvalid
	}
	u, err := url.Parse(r.Path)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" || u.Fragment != "" || !utf8.ValidString(u.Path) {
		return ErrInvalid
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." || strings.IndexFunc(part, unicode.IsControl) >= 0 || strings.ContainsAny(part, "\\%") {
			return ErrInvalid
		}
	}
	if _, err := url.ParseQuery(u.RawQuery); err != nil {
		return ErrInvalid
	}
	if r.ContentType != "" && r.ContentType != "application/json" {
		return ErrInvalid
	}
	return nil
}

// Native CouchDB GET/HEAD APIs do not mutate documents. POST (including Mango)
// remains conservative at this raw transport boundary; codecs certify reads.
func readOnly(r Request) bool {
	return (r.Method == "GET" || r.Method == "HEAD") && len(r.Body) == 0
}

func (c *Client) Do(ctx context.Context, r Request) (Response, error) {
	return c.do(ctx, r, readOnly(r))
}

// Only native codecs can certify their constructed POST as a read. Raw REST
// requests retain conservative write outcome semantics.
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
	c.mu.Unlock()
	if unavailable {
		return Response{}, ErrRetired
	}
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	child, cancel := context.WithCancel(ctx)
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
	if c.config.Auth == "basic" {
		req.SetBasicAuth(c.config.Username, c.secret)
	}
	result := Response{}
	interrupted := func(cause error) (Response, error) {
		c.mu.Lock()
		c.retired = true
		c.mu.Unlock()
		return result, &Interrupted{WriteOutcomeUnknown: !read, Cause: cause}
	}
	response, err := c.http.Do(req)
	if err != nil {
		if child.Err() != nil {
			return interrupted(child.Err())
		}
		return interrupted(errors.New("CouchDB HTTP transport failed"))
	}
	defer response.Body.Close()
	result.Status = response.StatusCode
	result.ContentType = response.Header.Get("Content-Type")
	result.ETag = response.Header.Get("ETag")
	result.Body, err = io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	if err != nil {
		result.Body = nil
		return interrupted(errors.New("CouchDB response truncated"))
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

// Close waits for this client's original worker and never replays remote work.
func (c *Client) Close() {
	c.mu.Lock()
	c.closed = true
	c.cancel()
	c.mu.Unlock()
	c.operation.Lock()
	defer c.operation.Unlock()
	c.http.CloseIdleConnections()
	c.secret = ""
}

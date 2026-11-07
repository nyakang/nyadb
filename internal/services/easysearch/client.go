// Package easysearch implements native Easysearch REST operations.
// It never follows redirects, fails over, or replays an interrupted request.
package easysearch

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/services/connections"
)

var (
	ErrRetired  = errors.New("Easysearch connection retired; open explicitly without replay")
	ErrBusy     = errors.New("Easysearch connection has an active request")
	ErrBounds   = errors.New("Easysearch request or response exceeds bounds")
	ErrProtocol = errors.New("invalid or unsupported Easysearch native response")
	ErrInvalid  = errors.New("invalid Easysearch request")
)

const requestLimit = 8 << 20
const responseLimit = 16 << 20

type Identity struct {
	Name        string `json:"name"`
	ClusterName string `json:"cluster_name"`
	ClusterUUID string `json:"cluster_uuid"`
	Version     struct {
		Number       string `json:"number"`
		Distribution string `json:"distribution"`
		Distributor  string `json:"distributor"`
		Hash         string `json:"build_hash"`
		Snapshot     bool   `json:"build_snapshot"`
		Lucene       string `json:"lucene_version"`
	} `json:"version"`
	Tagline string `json:"tagline"`
}

type Request struct {
	Method, Path, ContentType string
	Body                      []byte
}

// Response retains complete original bytes and the actual HTTP status. A raw
// REST response does not imply that a bulk or distributed operation is atomic.
type Response struct {
	Status      int
	ContentType string
	Body        []byte
}

type HTTPError struct {
	Status int
	Type   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("Easysearch HTTP %d (%s)", e.Status, e.Type)
}

// Interrupted cannot certify remote cancellation or the outcome of a write.
// The HTTP worker has settled when this is returned; the server may still run.
type Interrupted struct {
	WriteOutcomeUnknown bool
	Cause               error
}

func (e *Interrupted) Error() string {
	return "Easysearch request interrupted; recover explicitly and verify writes without replay"
}
func (e *Interrupted) Unwrap() error        { return e.Cause }
func (e *Interrupted) Is(target error) bool { return target == ErrRetired }

type Client struct {
	operation sync.Mutex
	mu        sync.Mutex
	config    connections.EasysearchConfig
	secret    string
	http      *http.Client
	ctx       context.Context
	cancel    context.CancelFunc
	identity  Identity
	closed    bool
	retired   bool
	disposed  bool
	closeErr  error
	opaque    string
	pits      map[*Cursor]string
	scrolls   map[*ScrollCursor]string
}

func Open(ctx context.Context, cfg connections.EasysearchConfig, secret string) (*Client, error) {
	cfg, err := connections.NormalizeEasysearchConfig(cfg)
	if err != nil {
		return nil, err
	}
	if err = connections.ValidateEasysearchCredential(cfg, secret, true); err != nil {
		return nil, err
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: cfg.ServerName}
	if cfg.CAFile != "" {
		file, e := os.Open(cfg.CAFile)
		if e != nil {
			return nil, errors.New("Easysearch CA cannot be read")
		}
		raw, e := io.ReadAll(io.LimitReader(file, (1<<20)+1))
		_ = file.Close()
		pool := x509.NewCertPool()
		if e != nil || len(raw) > 1<<20 || !pool.AppendCertsFromPEM(raw) {
			return nil, errors.New("invalid Easysearch CA")
		}
		tlsConfig.RootCAs = pool
	}
	// Fresh connections also prevent net/http's implicit retry on a previously
	// used idle connection. Native user requests are submitted exactly once.
	transport := &http.Transport{Proxy: nil, TLSClientConfig: tlsConfig, DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 40 * time.Second, MaxResponseHeaderBytes: 64 << 10, DisableKeepAlives: true}
	root, cancel := context.WithCancel(context.Background())
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		cancel()
		return nil, errors.New("Easysearch request identity cannot be created")
	}
	c := &Client{config: cfg, secret: secret, ctx: root, cancel: cancel, opaque: "nyadb-" + hex.EncodeToString(random[:]), pits: map[*Cursor]string{}, scrolls: map[*ScrollCursor]string{}}
	c.http = &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, err := c.Do(ctx, Request{Method: "GET", Path: "/"})
	if err != nil || decodeJSON(r.Body, &c.identity) != nil || !validNativeIdentity(c.identity) {
		_ = c.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("Easysearch native identity requires verified native 2.5.1")
	}
	return c, nil
}

func validNativeIdentity(identity Identity) bool {
	v := identity.Version
	return v.Number == "2.5.1" && v.Distribution == "easysearch" && v.Distributor == "INFINI Labs" &&
		v.Hash == "c8206f7d5ec3da50ae75b7db483cefb90a0b726b" && !v.Snapshot && v.Lucene == "9.12.2" &&
		identity.ClusterUUID != "" && identity.ClusterUUID != "_na_" && identity.Tagline == "You Know, For Easy Search!"
}

func (c *Client) Identity() Identity { return c.identity }

func validateRequest(r Request) error {
	switch r.Method {
	case "GET", "POST", "PUT", "DELETE", "HEAD", "PATCH":
	default:
		return ErrInvalid
	}
	if len(r.Body) > requestLimit || len(r.Path) == 0 || len(r.Path) > 8192 || !utf8.ValidString(r.Path) || strings.ContainsAny(r.Path, "\r\n\x00\\# ") || !strings.HasPrefix(r.Path, "/") || strings.HasPrefix(r.Path, "//") {
		return ErrInvalid
	}
	u, err := url.Parse(r.Path)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" || u.Fragment != "" {
		return ErrInvalid
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." || strings.ContainsAny(part, "\r\n\x00\\") {
			return ErrInvalid
		}
	}
	if r.ContentType != "" && r.ContentType != "application/json" && r.ContentType != "application/x-ndjson" {
		return ErrInvalid
	}
	return nil
}

func (c *Client) available() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.retired {
		return ErrRetired
	}
	return nil
}

func (c *Client) Do(ctx context.Context, r Request) (Response, error) {
	if err := validateRequest(r); err != nil {
		return Response{}, err
	}
	if !c.operation.TryLock() {
		return Response{}, ErrBusy
	}
	defer c.operation.Unlock()
	if err := c.available(); err != nil {
		return Response{}, err
	}
	return c.perform(ctx, r)
}

var errorType = regexp.MustCompile(`^[a-z][a-z0-9_]{0,127}$`)

// perform is called with operation held. Cleanup may use it after retirement.
func (c *Client) perform(ctx context.Context, r Request) (Response, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	return c.exchange(ctx, r)
}

func (c *Client) exchange(ctx context.Context, r Request) (Response, error) {
	var result Response
	if err := ctx.Err(); err != nil {
		return result, err
	}
	request, err := http.NewRequestWithContext(ctx, r.Method, c.config.Endpoint+r.Path, bytes.NewReader(bytes.Clone(r.Body)))
	if err != nil {
		return result, ErrInvalid
	}
	request.GetBody = nil
	content := r.ContentType
	if content == "" {
		content = "application/json"
	}
	request.Header.Set("Content-Type", content)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Opaque-Id", c.opaque)
	switch c.config.Auth {
	case "basic":
		request.SetBasicAuth(c.config.Username, c.secret)
	case "access_token":
		request.Header.Set("X-API-TOKEN", c.secret)
	}
	response, err := c.http.Do(request)
	interrupted := func(cause error) (Response, error) {
		c.mu.Lock()
		c.retired = true
		c.mu.Unlock()
		return result, &Interrupted{WriteOutcomeUnknown: r.Method != "GET" && r.Method != "HEAD", Cause: cause}
	}
	if err != nil {
		if ctx.Err() != nil {
			return interrupted(ctx.Err())
		}
		return interrupted(errors.New("Easysearch HTTP transport failed"))
	}
	defer response.Body.Close()
	result.Status = response.StatusCode
	result.ContentType = response.Header.Get("Content-Type")
	result.Body, err = io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	if err != nil {
		result.Body = nil
		return interrupted(errors.New("Easysearch response transfer failed"))
	}
	if len(result.Body) > responseLimit {
		result.Body = nil
		return interrupted(ErrBounds)
	}
	if result.Status < 200 || result.Status >= 300 {
		var diagnostic struct {
			Error struct {
				Type string `json:"type"`
			} `json:"error"`
			Status int `json:"status"`
		}
		_ = decodeJSON(result.Body, &diagnostic)
		if !errorType.MatchString(diagnostic.Error.Type) {
			diagnostic.Error.Type = "native_error"
		}
		nativeError := &HTTPError{Status: result.Status, Type: diagnostic.Error.Type}
		if r.Method != "GET" && r.Method != "HEAD" && (result.Status >= 500 || result.Status == 408 || result.Status == 429) {
			return interrupted(nativeError)
		}
		return result, nativeError
	}
	return result, nil
}

// Close waits for the original HTTP worker, then frees captured PIT resources.
// Remote operations without an acknowledged resource ID may finish later.
func (c *Client) Close() error {
	c.mu.Lock()
	c.closed = true
	c.cancel()
	c.mu.Unlock()
	c.operation.Lock()
	defer c.operation.Unlock()
	if c.disposed {
		return c.closeErr
	}
	c.mu.Lock()
	pits := make([]string, 0, len(c.pits))
	cursors := make([]*Cursor, 0, len(c.pits))
	for cursor, id := range c.pits {
		pits = append(pits, id)
		cursors = append(cursors, cursor)
	}
	c.pits = nil
	scrollIDs := make([]string, 0, len(c.scrolls))
	scrolls := make([]*ScrollCursor, 0, len(c.scrolls))
	for scroll, id := range c.scrolls {
		scrollIDs = append(scrollIDs, id)
		scrolls = append(scrolls, scroll)
	}
	c.scrolls = nil
	c.mu.Unlock()
	for _, cursor := range cursors {
		cursor.mu.Lock()
		cursor.closed = true
		cursor.client, cursor.query, cursor.after = nil, nil, nil
		cursor.mu.Unlock()
	}
	for _, id := range pits {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		body, _ := json.Marshal(map[string][]string{"pit_id": {id}})
		r, err := c.exchange(ctx, Request{Method: "DELETE", Path: "/_pit", Body: body})
		if err == nil {
			err = pitClosed(r.Body, id)
		}
		c.closeErr = errors.Join(c.closeErr, err)
		cancel()
	}
	for _, scroll := range scrolls {
		scroll.mu.Lock()
		scroll.closed, scroll.client, scroll.first = true, nil, Page{}
		scroll.mu.Unlock()
	}
	for _, id := range scrollIDs {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		body, _ := json.Marshal(map[string][]string{"scroll_id": {id}})
		response, err := c.exchange(ctx, Request{Method: "DELETE", Path: "/_search/scroll", Body: body})
		if err == nil {
			err = scrollClosed(response.Body)
		}
		c.closeErr = errors.Join(c.closeErr, err)
		cancel()
	}
	c.http.CloseIdleConnections()
	c.secret = ""
	c.disposed = true
	return c.closeErr
}

// decodeJSON rejects duplicate members instead of silently replacing values.
// RawMessage fields keep integer/decimal spellings and nested document bytes.
func decodeJSON(raw []byte, target any) error {
	if len(raw) == 0 || len(raw) > responseLimit || !utf8.Valid(raw) {
		return ErrProtocol
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var unique func(int) error
	unique = func(depth int) error {
		if depth > 64 {
			return ErrBounds
		}
		token, err := d.Token()
		if err != nil {
			return ErrProtocol
		}
		if delimiter, ok := token.(json.Delim); ok {
			switch delimiter {
			case '{':
				seen := map[string]bool{}
				for d.More() {
					key, e := d.Token()
					name, ok := key.(string)
					if e != nil || !ok || seen[name] {
						return ErrProtocol
					}
					seen[name] = true
					if e = unique(depth + 1); e != nil {
						return e
					}
				}
			case '[':
				for d.More() {
					if err = unique(depth + 1); err != nil {
						return err
					}
				}
			default:
				return ErrProtocol
			}
			if _, err = d.Token(); err != nil {
				return ErrProtocol
			}
		}
		return nil
	}
	if err := unique(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrProtocol
	}
	if json.Unmarshal(raw, target) != nil {
		return ErrProtocol
	}
	return nil
}

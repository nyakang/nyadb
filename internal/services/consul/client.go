package consul

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
)

var (
	ErrInvalid      = errors.New("invalid Consul request or configuration")
	ErrUnsupported  = errors.New("unsupported Consul native operation")
	ErrProtocol     = errors.New("invalid Consul native response")
	ErrBounds       = errors.New("Consul request or response exceeds bounds")
	ErrBusy         = errors.New("original Consul connection has an active request")
	ErrRetired      = errors.New("original Consul connection retired; recover explicitly without replay")
	ErrPlanUsed     = errors.New("Consul preparation used or belongs to another original connection")
	ErrWriteUnknown = errors.New("original Consul write acknowledgement is unknown; further writes are blocked")
	ErrConflict     = errors.New("Consul rejected the original condition")
)

type HTTPError struct{ Status int }

func (e *HTTPError) Error() string { return fmt.Sprintf("Consul HTTP %d", e.Status) }

// HTTP capture, native semantic ACK and optional projections remain independent.
// Body includes every observed byte even when its bounded capture is incomplete.
type Response struct {
	URL                                                                                     string // actual scoped URL; authentication is never a URL parameter
	Request                                                                                 Request
	Kind                                                                                    string
	Attempted, Ended, Captured, EOF, Acknowledged, Succeeded, WriteOutcomeUnknown, Canceled bool
	Status                                                                                  int
	Protocol                                                                                string
	Header, Trailer                                                                         http.Header
	Body                                                                                    []byte
	Metadata                                                                                Metadata
	Entries                                                                                 []Entry
	Txn                                                                                     *TxnResult
	SessionID                                                                               string
	CaptureError, ProjectionError                                                           string
}

func (r Response) Clone() Response {
	r.Request = r.Request.Clone()
	r.Body = bytes.Clone(r.Body)
	r.Header = r.Header.Clone()
	r.Trailer = r.Trailer.Clone()
	r.Metadata = r.Metadata.Clone()
	r.Entries = append([]Entry(nil), r.Entries...)
	for i := range r.Entries {
		r.Entries[i] = r.Entries[i].Clone()
	}
	if r.Txn != nil {
		t := r.Txn.Clone()
		r.Txn = &t
	}
	return r
}

type Client struct {
	operation       sync.Mutex
	mu              sync.Mutex
	config          Config
	token           string
	http            *http.Client
	ctx             context.Context
	cancel          context.CancelFunc
	closed, retired bool
	last            Response
	unknown         *Response
	cache           bool
}

// New is pure with respect to Consul: TLS files are captured locally, no HTTP is sent.
func New(cfg Config, token string) (*Client, error) {
	cfg, tc, e := normalizeConfig(cfg, token)
	if e != nil {
		return nil, e
	}
	return newCapturedClient(cfg, tc, token), nil
}

// Managed document clients use captured TLS material and let their original
// lease own receipt storage; no file is reopened and no duplicate cache retained.
func newCapturedClient(cfg Config, tc *tls.Config, token string) *Client {
	if tc != nil {
		tc.NextProtos = []string{"http/1.1"}
	}
	tr := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: cfg.ConnectTimeout}).DialContext, TLSClientConfig: tc, TLSHandshakeTimeout: cfg.ConnectTimeout, ResponseHeaderTimeout: cfg.RequestTimeout, MaxResponseHeaderBytes: 64 << 10, DisableKeepAlives: true, DisableCompression: true, ForceAttemptHTTP2: false}
	root, cancel := context.WithCancel(context.Background())
	c := &Client{config: cfg, token: token, ctx: root, cancel: cancel, cache: true, http: &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	return c
}
func (c *Client) Alive() bool            { c.mu.Lock(); defer c.mu.Unlock(); return !c.closed && !c.retired }
func (c *Client) Config() Config         { return c.config }
func (c *Client) LastResponse() Response { c.mu.Lock(); defer c.mu.Unlock(); return c.last.Clone() }
func (c *Client) UnknownResponse() (Response, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.unknown == nil {
		return Response{}, false
	}
	return c.unknown.Clone(), true
}
func (c *Client) Do(ctx context.Context, r Request) (Response, error) {
	p, e := c.Prepare(r)
	if e != nil {
		return Response{}, e
	}
	return c.Execute(ctx, p)
}
func (c *Client) requestURL(p Plan) (string, error) {
	q := p.request.Query.Clone()
	if q == nil {
		q = url.Values{}
	}
	// Agent endpoints select the original contacted agent, not a scoped namespace.
	if p.request.Path != "/v1/agent/self" {
		for key, value := range map[string]string{"dc": c.config.Datacenter, "ns": c.config.Namespace, "partition": c.config.Partition} {
			if value != "" {
				q.Set(key, value)
			}
		}
	}
	scopedRead := !p.write && (p.kind == "kv-read" || p.kind == "txn" || p.request.Path != "/v1/agent/self" && p.request.Path != "/v1/status/leader" && p.request.Path != "/v1/status/peers" && p.request.Path != "/v1/catalog/datacenters")
	if scopedRead && c.config.Consistency != "default" {
		other := "stale"
		if c.config.Consistency == "stale" {
			other = "consistent"
		}
		if q.Has(other) {
			return "", ErrInvalid
		}
		q.Set(c.config.Consistency, "")
	}
	target := c.config.Endpoint + p.request.Path
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	return target, nil
}
func (c *Client) Execute(ctx context.Context, p Plan) (result Response, err error) {
	if p.owner != c || p.used == nil || p.kind == "" {
		return result, ErrPlanUsed
	}
	if !c.operation.TryLock() {
		return result, ErrBusy
	}
	defer c.operation.Unlock()
	c.mu.Lock()
	unavailable, unknown, token := c.closed || c.retired, c.unknown != nil, c.token
	c.mu.Unlock()
	if unavailable {
		return result, ErrRetired
	}
	if p.write && unknown {
		return result, ErrWriteUnknown
	}
	if !p.used.CompareAndSwap(false, true) {
		return result, ErrPlanUsed
	}
	result.Request, result.Kind = p.Snapshot(), p.kind
	defer func() {
		result.Ended = true
		c.mu.Lock()
		if c.cache {
			c.last = result.Clone()
		}
		if result.WriteOutcomeUnknown {
			v := Response{WriteOutcomeUnknown: true}
			if c.cache {
				v = result.Clone()
			}
			c.unknown = &v
		}
		c.mu.Unlock()
	}()
	if e := ctx.Err(); e != nil {
		return result, e
	}
	target, e := c.requestURL(p)
	if e != nil {
		return result, e
	}
	result.URL = target
	child, cancel := context.WithTimeout(ctx, c.config.RequestTimeout)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	req, e := http.NewRequestWithContext(child, p.request.Method, target, bytes.NewReader(p.request.Body))
	if e != nil {
		return result, ErrInvalid
	}
	req.GetBody = nil
	req.Close = true
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if p.kind == "kv-put" {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	if c.config.Auth == "token" {
		req.Header.Set("X-Consul-Token", token)
	}
	result.Attempted = true
	response, e := c.http.Do(req)
	interrupted := func(cause error) (Response, error) {
		c.mu.Lock()
		c.retired = true
		c.mu.Unlock()
		c.cancel()
		result.Canceled = child.Err() != nil
		result.WriteOutcomeUnknown = p.write && !result.Acknowledged
		return result, cause
	}
	if e != nil {
		if child.Err() != nil {
			return interrupted(child.Err())
		}
		return interrupted(errors.New("Consul original HTTP transport failed"))
	}
	result.Status, result.Protocol, result.Header = response.StatusCode, response.Proto, response.Header.Clone()
	raw, e := io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	result.Body = raw
	closeErr := response.Body.Close()
	result.Trailer = response.Trailer.Clone()
	if e != nil || len(raw) > responseLimit {
		result.CaptureError = "original Consul response body was incomplete"
		if len(raw) > responseLimit {
			result.CaptureError = ErrBounds.Error()
			return interrupted(ErrBounds)
		}
		if child.Err() != nil {
			return interrupted(child.Err())
		}
		return interrupted(errors.New(result.CaptureError))
	}
	result.Captured, result.EOF = true, true
	if closeErr != nil {
		result.CaptureError = "original Consul response close failed"
	}
	metadata, e := decodeMetadata(result.Header)
	if e != nil {
		result.ProjectionError = e.Error()
	} else {
		result.Metadata = metadata
	}
	ackErr := projectResponse(&result, p)
	if p.write && !result.Acknowledged {
		result.WriteOutcomeUnknown = true
	}
	if child.Err() != nil {
		return interrupted(child.Err())
	}
	// A metadata/cleanup projection failure cannot undo a complete native ACK.
	if ackErr != nil {
		return result, ackErr
	}
	if e != nil {
		return result, e
	}
	if closeErr != nil {
		return result, errors.New(result.CaptureError)
	}
	return result, nil
}
func (c *Client) Close() error {
	c.mu.Lock()
	c.closed = true
	c.cancel()
	c.mu.Unlock()
	c.operation.Lock()
	defer c.operation.Unlock()
	c.http.CloseIdleConnections()
	c.mu.Lock()
	c.token = ""
	c.last = Response{}
	c.unknown = nil
	c.mu.Unlock()
	return nil
}

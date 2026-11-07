// Package solr implements the native Solr HTTP transport. It retains raw
// responses, never follows redirects and never replays an interrupted request.
package solr

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
	ErrRetired  = errors.New("Solr connection retired; recover explicitly without replay")
	ErrBusy     = errors.New("Solr connection has an active request")
	ErrBounds   = errors.New("Solr request or response exceeds bounds")
	ErrProtocol = errors.New("invalid or unsupported Solr native response")
	ErrInvalid  = errors.New("invalid Solr request")
)

const requestLimit = 8 << 20
const responseLimit = 16 << 20

type Identity struct {
	Mode     string `json:"mode"`
	SolrHome string `json:"solr_home"`
	Lucene   struct {
		SolrVersion   string `json:"solr-spec-version"`
		SolrBuild     string `json:"solr-impl-version"`
		LuceneVersion string `json:"lucene-spec-version"`
		LuceneBuild   string `json:"lucene-impl-version"`
	} `json:"lucene"`
}
type Request struct {
	Method, Path, ContentType string
	Body                      []byte
}

// A successful HTTP response does not imply that a native bulk or distributed
// operation is atomic. Consumers must validate their operation's own receipt.
type Response struct {
	Status      int
	ContentType string
	Body        []byte
}
type HTTPError struct{ Status int }

func (e *HTTPError) Error() string { return fmt.Sprintf("Solr HTTP %d", e.Status) }

// Interrupted settles the original HTTP worker. Remote writes may still run;
// neither local cancellation nor a server error proves remote rollback.
type Interrupted struct {
	WriteOutcomeUnknown bool
	Cause               error
}

func (e *Interrupted) Error() string {
	return "Solr request interrupted; recover explicitly and verify writes without replay"
}
func (e *Interrupted) Unwrap() error        { return e.Cause }
func (e *Interrupted) Is(target error) bool { return target == ErrRetired }

type Client struct {
	operation       sync.Mutex
	mu              sync.Mutex
	config          connections.SolrConfig
	secret          string
	http            *http.Client
	ctx             context.Context
	cancel          context.CancelFunc
	identity        Identity
	closed, retired bool
}

func Open(ctx context.Context, cfg connections.SolrConfig, secret string) (*Client, error) {
	cfg, err := connections.NormalizeSolrConfig(cfg)
	if err != nil {
		return nil, err
	}
	if err = connections.ValidateSolrCredential(cfg, secret, true); err != nil {
		return nil, err
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: cfg.ServerName}
	if cfg.CAFile != "" {
		file, e := os.Open(cfg.CAFile)
		if e != nil {
			return nil, errors.New("Solr CA cannot be read")
		}
		raw, e := io.ReadAll(io.LimitReader(file, (1<<20)+1))
		_ = file.Close()
		pool := x509.NewCertPool()
		if e != nil || len(raw) > 1<<20 || !pool.AppendCertsFromPEM(raw) {
			return nil, errors.New("invalid Solr CA")
		}
		tlsConfig.RootCAs = pool
	}
	// Fresh connections prevent net/http retries on reused connections, including
	// GET CoreAdmin mutations. Explicit application requests are submitted once.
	transport := &http.Transport{Proxy: nil, TLSClientConfig: tlsConfig, DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 40 * time.Second, MaxResponseHeaderBytes: 64 << 10, DisableKeepAlives: true}
	root, cancel := context.WithCancel(context.Background())
	c := &Client{config: cfg, secret: secret, ctx: root, cancel: cancel, http: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	response, err := c.Do(ctx, Request{Method: "GET", Path: "/admin/info/system?wt=json"})
	if err != nil {
		c.Close()
		return nil, err
	}
	if decodeJSON(response.Body, &c.identity) != nil || !validIdentity(c.identity) {
		c.Close()
		return nil, ErrProtocol
	}
	return c, nil
}
func validIdentity(v Identity) bool {
	return (v.Mode == "std" || v.Mode == "solrcloud") && v.SolrHome != "" && v.Lucene.SolrVersion == "9.9.0" && v.Lucene.LuceneVersion == "9.12.2" && strings.HasPrefix(v.Lucene.SolrBuild, "9.9.0 386ed096a1946c488cfe576a19a147bdb1153508 ")
}
func (c *Client) Identity() Identity { return c.identity }

func validateRequest(r Request) error {
	switch r.Method {
	case "GET", "HEAD", "POST", "PUT", "DELETE", "PATCH":
	default:
		return ErrInvalid
	}
	if len(r.Body) > requestLimit || len(r.Path) == 0 || len(r.Path) > 8192 || !utf8.ValidString(r.Path) || strings.IndexFunc(r.Path, func(v rune) bool { return unicode.IsControl(v) || unicode.IsSpace(v) }) >= 0 || strings.ContainsAny(r.Path, "\\#") || !strings.HasPrefix(r.Path, "/") || strings.HasPrefix(r.Path, "//") {
		return ErrInvalid
	}
	u, e := url.Parse(r.Path)
	if e != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" || u.Fragment != "" || !utf8.ValidString(u.Path) {
		return ErrInvalid
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." || strings.IndexFunc(part, unicode.IsControl) >= 0 || strings.ContainsAny(part, "\\%") {
			return ErrInvalid
		}
	}
	if _, e = url.ParseQuery(u.RawQuery); e != nil {
		return ErrInvalid
	}
	switch r.ContentType {
	case "", "application/json", "application/xml", "text/xml", "application/x-www-form-urlencoded", "text/csv":
	default:
		return ErrInvalid
	}
	return nil
}

// Only fixed metadata endpoints with a closed parameter set certify a read.
// User-defined handlers and unrecognized GET parameters remain conservative.
func readOnly(r Request) bool {
	if (r.Method != "GET" && r.Method != "HEAD") || len(r.Body) != 0 {
		return false
	}
	u, e := url.Parse(r.Path)
	if e != nil {
		return false
	}
	q, e := url.ParseQuery(u.RawQuery)
	if e != nil {
		return false
	}
	switch u.Path {
	case "/admin/info/system":
		for key, values := range q {
			if key != "wt" || len(values) != 1 || values[0] != "json" {
				return false
			}
		}
		return true
	case "/admin/cores":
		for key, values := range q {
			if len(values) != 1 {
				return false
			}
			switch key {
			case "wt":
				if values[0] != "json" {
					return false
				}
			case "action":
				if values[0] != "STATUS" {
					return false
				}
			case "core", "indexInfo":
			default:
				return false
			}
		}
		return q.Get("action") == "STATUS"
	default:
		return false
	}
}
func (c *Client) Do(ctx context.Context, r Request) (Response, error) {
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
	request, err := http.NewRequestWithContext(child, r.Method, c.config.Endpoint+r.Path, bytes.NewReader(r.Body))
	if err != nil {
		return Response{}, ErrInvalid
	}
	request.GetBody = nil
	content := r.ContentType
	if content == "" {
		content = "application/json"
	}
	request.Header.Set("Content-Type", content)
	request.Header.Set("Accept", "application/json")
	if c.config.Auth == "basic" {
		request.SetBasicAuth(c.config.Username, c.secret)
	}
	result := Response{}
	interrupted := func(cause error) (Response, error) {
		c.mu.Lock()
		c.retired = true
		c.mu.Unlock()
		return result, &Interrupted{WriteOutcomeUnknown: !readOnly(r), Cause: cause}
	}
	response, err := c.http.Do(request)
	if err != nil {
		if child.Err() != nil {
			return interrupted(child.Err())
		}
		return interrupted(errors.New("Solr HTTP transport failed"))
	}
	defer response.Body.Close()
	result.Status = response.StatusCode
	result.ContentType = response.Header.Get("Content-Type")
	result.Body, err = io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	if err != nil {
		result.Body = nil
		return interrupted(errors.New("Solr response transfer failed"))
	}
	if len(result.Body) > responseLimit {
		result.Body = nil
		return interrupted(ErrBounds)
	}
	if result.Status < 200 || result.Status >= 300 {
		diagnostic := &HTTPError{Status: result.Status}
		if !readOnly(r) {
			return interrupted(diagnostic)
		}
		return result, diagnostic
	}
	return result, nil
}

// Close cancels and waits for its own HTTP worker. Other clients are independent.
// No remote operation is submitted by Close; unknown writes are never replayed.
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

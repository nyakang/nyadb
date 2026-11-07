// Package trino implements the native HTTP statement protocol. It never
// resubmits an initial POST or reconnects a retired logical session.
package trino

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
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/services/connections"
)

var ErrLost = errors.New("Trino session lost; recover explicitly without replaying SQL")
var ErrBusy = errors.New("Trino session already has an active statement")
var ErrBounds = errors.New("Trino statement or result exceeds native client bounds")
var ErrProtocol = errors.New("invalid or unsupported Trino protocol response")

// QueryError exposes stable native diagnostics without retaining SQL or server
// messages, which may contain a statement's credentials or literal values.
type QueryError struct {
	Code       int64
	Name, Type string
}

func (e *QueryError) Error() string {
	return fmt.Sprintf("Trino query failed: %s (%d)", e.Name, e.Code)
}

// Interrupted records whether the coordinator acknowledged cancellation.
// The caller must treat an interrupted write as uncertain and never replay it.
type Interrupted struct {
	QueryID            string
	CancelAcknowledged bool
	Cause              error
}

func (e *Interrupted) Error() string {
	return "Trino statement interrupted; outcome may be uncertain; recover without replay"
}
func (e *Interrupted) Unwrap() error        { return e.Cause }
func (e *Interrupted) Is(target error) bool { return target == ErrLost }

type Column struct {
	Name      string          `json:"name"`
	Type      string          `json:"type"`
	Signature json.RawMessage `json:"typeSignature"`
}

// Raw JSON values preserve native integer/decimal/temporal and complex values.
// No float64 conversion is performed in the transport. Each consumer receives
// private copies of the complete columns and row values.
type Consumer func([]Column, []json.RawMessage) error
type Result struct {
	QueryID, UpdateType string
	Columns             []Column
	UpdateCount         *int64
	Rows                int64
}

type response struct {
	ID          string          `json:"id"`
	Next        string          `json:"nextUri"`
	Columns     []Column        `json:"columns"`
	Data        json.RawMessage `json:"data"`
	UpdateType  string          `json:"updateType"`
	UpdateCount *int64          `json:"updateCount"`
	Error       *struct {
		Code int64  `json:"errorCode"`
		Name string `json:"errorName"`
		Type string `json:"errorType"`
	} `json:"error"`
}

type Client struct {
	operation    sync.Mutex
	mu           sync.Mutex
	config       connections.TrinoConfig
	password     string
	http         *http.Client
	ctx          context.Context
	cancel       context.CancelFunc
	closed, lost bool
	headers      http.Header
	disposed     bool
	closeErr     error
}

// Open verifies the coordinator and native kernel before exposing the client.
// Plain HTTP supports only Trino's explicit unauthenticated user mode.
func Open(ctx context.Context, cfg connections.TrinoConfig, password string) (*Client, error) {
	cfg, err := connections.NormalizeTrinoConfig(cfg)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(cfg.Endpoint)
	if u.Scheme != "https" && password != "" {
		return nil, errors.New("Trino passwords require verified HTTPS")
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: cfg.ServerName}
	if cfg.CAFile != "" {
		f, err := os.Open(cfg.CAFile)
		if err != nil {
			return nil, errors.New("Trino CA cannot be read")
		}
		raw, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
		_ = f.Close()
		pool := x509.NewCertPool()
		if err != nil || len(raw) > 1<<20 || !pool.AppendCertsFromPEM(raw) {
			return nil, errors.New("invalid Trino CA")
		}
		tlsConfig.RootCAs = pool
	}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: tlsConfig, DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 35 * time.Second, MaxResponseHeaderBytes: 64 << 10, MaxIdleConnsPerHost: 2}
	root, cancel := context.WithCancel(context.Background())
	c := &Client{config: cfg, password: password, ctx: root, cancel: cancel, headers: http.Header{}}
	c.http = &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	c.headers.Set("X-Trino-User", cfg.Username)
	c.headers.Set("X-Trino-Source", "NyaDB")
	c.headers.Set("X-Trino-Time-Zone", "UTC")
	c.headers.Set("X-Trino-Client-Capabilities", "PARAMETRIC_DATETIME")
	c.headers.Set("X-Trino-Transaction-Id", "NONE")
	if cfg.Catalog != "" {
		c.headers.Set("X-Trino-Catalog", cfg.Catalog)
	}
	if cfg.Schema != "" {
		c.headers.Set("X-Trino-Schema", cfg.Schema)
	}
	status, _, raw, err := c.request(ctx, http.MethodGet, cfg.Endpoint+"/v1/info", "")
	var identity struct {
		NodeVersion struct {
			Version string `json:"version"`
		} `json:"nodeVersion"`
		Coordinator bool `json:"coordinator"`
		Starting    bool `json:"starting"`
	}
	if err != nil || status != 200 || json.Unmarshal(raw, &identity) != nil || identity.NodeVersion.Version != "484" || !identity.Coordinator || identity.Starting {
		c.Close()
		if err != nil {
			return nil, fmt.Errorf("Trino coordinator identity request failed: %w", err)
		}
		return nil, fmt.Errorf("Trino coordinator authentication or native 484 identity check failed (HTTP %d)", status)
	}
	var version string
	_, err = c.Run(ctx, "SELECT version()", func(cols []Column, row []json.RawMessage) error {
		if len(cols) != 1 || len(row) != 1 || json.Unmarshal(row[0], &version) != nil {
			return ErrProtocol
		}
		return nil
	})
	if err != nil || version != "484" {
		c.Close()
		if err != nil {
			return nil, fmt.Errorf("Trino native kernel query failed: %w", err)
		}
		return nil, errors.New("unsupported Trino native kernel; requires 484")
	}
	return c, nil
}

func (c *Client) request(ctx context.Context, method, target, source string) (int, http.Header, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, target, strings.NewReader(source))
	if err != nil {
		return 0, nil, nil, ErrProtocol
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	if method == http.MethodPost {
		req.Header = c.headers.Clone()
		req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	}
	if c.password != "" {
		req.SetBasicAuth(c.config.Username, c.password)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil, nil, ctx.Err()
		}
		return 0, nil, nil, errors.New("Trino HTTP transport failed")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
	if err != nil {
		return resp.StatusCode, resp.Header, nil, errors.New("Trino response transfer failed")
	}
	if len(raw) > 16<<20 {
		return resp.StatusCode, resp.Header, nil, ErrBounds
	}
	return resp.StatusCode, resp.Header, raw, nil
}

var queryID = regexp.MustCompile(`^[0-9]{8}_[0-9]{6}_[0-9]+_[a-z0-9]+$`)
var diagnostic = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

func (c *Client) nextURI(raw, id string) (string, error) {
	u, err := url.Parse(raw)
	base, _ := url.Parse(c.config.Endpoint)
	if err != nil || u.Scheme != base.Scheme || u.Host != base.Host || u.User != nil || u.Opaque != "" || u.Fragment != "" || u.RawQuery != "" || u.RawPath != "" || len(raw) > 4096 {
		return "", ErrProtocol
	}
	parts := strings.Split(u.Path, "/")
	if len(parts) < 7 || parts[1] != "v1" || parts[2] != "statement" || parts[3] != "queued" && parts[3] != "executing" || parts[4] != id {
		return "", ErrProtocol
	}
	for _, p := range parts[5:] {
		if p == "" || strings.ContainsAny(p, ".\\") {
			return "", ErrProtocol
		}
	}
	return u.String(), nil
}

func (c *Client) interrupted(id, next string, cause error) error {
	ack := false
	if next != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		status, _, _, err := c.request(ctx, http.MethodDelete, next, "")
		ack = err == nil && status == http.StatusNoContent
		cancel()
	}
	c.mu.Lock()
	c.lost = true
	c.mu.Unlock()
	return &Interrupted{QueryID: id, CancelAcknowledged: ack, Cause: cause}
}

// Run executes one native statement with bounded streaming results. A terminal
// server query error preserves acknowledged session state. Any interruption or
// uncertain response retires the session, even after cancellation is confirmed.
func (c *Client) Run(ctx context.Context, source string, consume Consumer) (Result, error) {
	var result Result
	if len(source) == 0 || len(source) > 1<<20 || !utf8.ValidString(source) || strings.ContainsRune(source, 0) {
		return result, ErrBounds
	}
	if !c.operation.TryLock() {
		return result, ErrBusy
	}
	defer c.operation.Unlock()
	c.mu.Lock()
	unavailable := c.closed || c.lost
	c.mu.Unlock()
	if unavailable {
		return result, ErrLost
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	method, target := http.MethodPost, c.config.Endpoint+"/v1/statement"
	next := ""
	var columns []Column
	var columnBytes []byte
	for pages := 0; pages < 100000; pages++ {
		status, header, raw, err := c.request(ctx, method, target, source)
		if err != nil || status != 200 {
			if err == nil {
				err = fmt.Errorf("Trino HTTP response status %d", status)
			}
			return result, c.interrupted(result.QueryID, next, err)
		}
		var page response
		if !json.Valid(raw) || json.Unmarshal(raw, &page) != nil || !queryID.MatchString(page.ID) || result.QueryID != "" && page.ID != result.QueryID {
			return result, c.interrupted(result.QueryID, next, ErrProtocol)
		}
		result.QueryID = page.ID
		if page.Next != "" {
			next, err = c.nextURI(page.Next, page.ID)
			if err != nil {
				return result, c.interrupted(page.ID, targetIfContinuation(method, target), err)
			}
		} else {
			next = ""
		}
		if err = c.applyHeaders(header); err != nil {
			return result, c.interrupted(page.ID, next, err)
		}
		if len(page.Columns) > 0 {
			current, _ := json.Marshal(page.Columns)
			if len(page.Columns) > 512 || columnBytes != nil && !bytes.Equal(current, columnBytes) {
				return result, c.interrupted(page.ID, next, ErrProtocol)
			}
			for _, col := range page.Columns {
				if col.Type == "" || !json.Valid(col.Signature) {
					return result, c.interrupted(page.ID, next, ErrProtocol)
				}
			}
			columns, columnBytes = page.Columns, current
			result.Columns = make([]Column, len(columns))
			for i, col := range columns {
				result.Columns[i] = col
				result.Columns[i].Signature = bytes.Clone(col.Signature)
			}
		}
		if page.Error != nil {
			if next != "" || !diagnostic.MatchString(page.Error.Name) || !diagnostic.MatchString(page.Error.Type) {
				return result, c.interrupted(page.ID, next, ErrProtocol)
			}
			return result, &QueryError{Code: page.Error.Code, Name: page.Error.Name, Type: page.Error.Type}
		}
		if len(page.Data) > 0 && string(page.Data) != "null" {
			var rows [][]json.RawMessage
			if len(columns) == 0 || json.Unmarshal(page.Data, &rows) != nil || len(rows) > 10000 {
				return result, c.interrupted(page.ID, next, ErrProtocol)
			}
			for _, row := range rows {
				if len(row) != len(columns) {
					return result, c.interrupted(page.ID, next, ErrProtocol)
				}
				for _, v := range row {
					if len(v) > 1<<20 {
						return result, c.interrupted(page.ID, next, ErrBounds)
					}
				}
				result.Rows++
				if consume != nil {
					copyCols := make([]Column, len(columns))
					for i, col := range columns {
						copyCols[i] = col
						copyCols[i].Signature = bytes.Clone(col.Signature)
					}
					if err = consume(copyCols, row); err != nil {
						return result, c.interrupted(page.ID, next, err)
					}
				}
			}
		}
		if page.UpdateType != "" {
			result.UpdateType = page.UpdateType
		}
		if page.UpdateCount != nil {
			v := *page.UpdateCount
			result.UpdateCount = &v
		}
		if next == "" {
			return result, nil
		}
		method, target, source = http.MethodGet, next, ""
	}
	return result, c.interrupted(result.QueryID, next, ErrBounds)
}

func targetIfContinuation(method, target string) string {
	if method == http.MethodGet {
		return target
	}
	return ""
}

// Close waits for the active request's cancellation acknowledgement, then
// releases any captured original transaction through a bounded ROLLBACK.
// Cleanup never replays a user command or sends COMMIT.
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
	c.disposed = true
	if id := c.headers.Get("X-Trino-Transaction-Id"); id != "" && id != "NONE" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		scratch := &Client{config: c.config, password: c.password, http: c.http, ctx: ctx, cancel: cancel, headers: c.headers.Clone()}
		_, c.closeErr = scratch.Run(ctx, "ROLLBACK", nil)
		cancel()
	}
	c.password = ""
	c.headers = nil
	c.http.CloseIdleConnections()
	return c.closeErr
}

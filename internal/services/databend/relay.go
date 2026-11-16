// Package databend preserves the original Databend agent and product protocol.
package databend

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

var (
	ErrRelayTarget = errors.New("Databend original HTTP target or certificate configuration is invalid")
	ErrRelayClosed = errors.New("Databend original transport is closed")
)

const (
	queryIDHeader    = "X-DATABEND-QUERY-ID"
	maxRequestBytes  = 8 << 20
	maxCachedReceipt = 1 << 20
	maxCacheBytes    = 32 << 20
	maxQueryIDs      = 100000
)

// Relay owns one original JDBC process's fixed upstream target. JDBC 0.4.8
// generates an ID before its initial POST but retries that POST automatically.
// An ID is submitted at most once here. Tombstones remain until process close,
// even when a complete response cannot be cached. Identical SQL with a fresh
// original ID remains a distinct, legitimate execution.
//
// The upstream executor belongs to the relay, not to a local HTTP connection:
// a JDBC timeout/disconnect does not cancel an unacknowledged write or authorize
// replay. Close waits for the captured handlers to settle. Closing a relay is
// transport cleanup; it does not prove server-side cancellation or rollback.
type Relay struct {
	listener  net.Listener
	server    *http.Server
	transport *http.Transport
	client    *http.Client
	target    url.URL
	jdbcQuery string
	auth      string
	localAuth string
	password  string
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	closed    bool
	queries   map[string]*relayQuery
	cacheSize int
	wg        sync.WaitGroup
	serveDone chan struct{}
	once      sync.Once
}

type relayQuery struct {
	fingerprint [32]byte
	done        chan struct{}
	status      int
	headers     http.Header
	body        []byte
	cached      bool
	complete    bool
	uncertain   bool
}

// RelayQueryStatus contains no SQL, credentials, or result values.
type RelayQueryStatus struct {
	Complete, Uncertain, Cached bool
	HTTPStatus                  int
}

// StartRelay performs no queries and does not launch or publish an agent.
// All HTTPS requests verify the captured original host and trust configuration.
// The local endpoint uses a fresh secret unrelated to upstream credentials.
func StartRelay(ctx context.Context, config connections.DatabendConfig, secret string) (*Relay, error) {
	if ctx == nil {
		return nil, connections.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c, err := connections.NormalizeDatabendConfig(config)
	if err != nil || strings.ContainsAny(secret, "\r\n\x00") || c.Auth == "none" && secret != "" || c.Auth == "bearer" && (secret == "" || strings.ContainsAny(secret, " \t")) {
		return nil, ErrRelayTarget
	}
	u, _ := url.Parse(c.Endpoint)
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname()}
	if c.ServerName != "" {
		tlsConfig.ServerName = c.ServerName
	}
	if c.CAFile != "" {
		raw, err := readCertificate(c.CAFile)
		if err != nil {
			return nil, ErrRelayTarget
		}
		tlsConfig.RootCAs = x509.NewCertPool()
		if !tlsConfig.RootCAs.AppendCertsFromPEM(raw) {
			return nil, ErrRelayTarget
		}
	}
	if c.ClientCertFile != "" {
		certPEM, certErr := readCertificate(c.ClientCertFile)
		keyPEM, keyErr := readCertificate(c.ClientKeyFile)
		if certErr != nil || keyErr != nil {
			return nil, ErrRelayTarget
		}
		cert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			return nil, ErrRelayTarget
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return nil, ErrRelayTarget
	}
	password := hex.EncodeToString(token)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, ErrRelayTarget
	}
	root, cancel := context.WithCancel(context.Background())
	timeout := time.Duration(c.ConnectTimeoutSeconds) * time.Second
	transport := &http.Transport{
		Proxy: nil, DialContext: (&net.Dialer{Timeout: timeout}).DialContext,
		TLSClientConfig: tlsConfig, TLSHandshakeTimeout: timeout,
		ResponseHeaderTimeout:  time.Duration(c.RequestTimeoutSeconds) * time.Second,
		MaxResponseHeaderBytes: 64 << 10, MaxIdleConns: 64, MaxIdleConnsPerHost: 32,
		IdleConnTimeout: 30 * time.Second, DisableCompression: true,
	}
	r := &Relay{
		listener: listener, target: *u, transport: transport,
		client: &http.Client{Transport: transport, Timeout: time.Duration(c.RequestTimeoutSeconds) * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		localAuth: "Basic " + base64.StdEncoding.EncodeToString([]byte("nyadb:"+password)),
		password:  password, ctx: root, cancel: cancel, queries: make(map[string]*relayQuery),
		serveDone: make(chan struct{}),
	}
	// JDBC 0.4.8 preserves URL properties on its HTTP login/query URLs.
	// Admit this one immutable query string locally and remove it upstream.
	r.jdbcQuery = url.Values{
		"sslmode": {"disable"}, "connection_timeout": {strconv.Itoa(c.ConnectTimeoutSeconds)},
		"socket_timeout": {strconv.Itoa(max(11, c.RequestTimeoutSeconds+5))}, "query_timeout": {strconv.Itoa(c.RequestTimeoutSeconds)},
		"wait_time_secs": {"1"}, "presign": {"off"}, "query_result_format": {"json"}, "max_rows_per_page": {"1000"},
	}.Encode()
	switch c.Auth {
	case "basic":
		r.auth = "Basic " + base64.StdEncoding.EncodeToString([]byte(c.Username+":"+secret))
	case "bearer":
		r.auth = "Bearer " + secret
	}
	r.server = &http.Server{
		Handler: r, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second,
		IdleTimeout: 30 * time.Second, MaxHeaderBytes: 64 << 10,
		ErrorLog: log.New(io.Discard, "", 0),
	}
	go func() { defer close(r.serveDone); _ = r.server.Serve(listener) }()
	if err := ctx.Err(); err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}

func readCertificate(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrRelayTarget
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return nil, ErrRelayTarget
	}
	return raw, nil
}

func (r *Relay) Endpoint() string              { return "http://" + r.listener.Addr().String() }
func (r *Relay) Credentials() (string, string) { return "nyadb", r.password }

// ConnectionParams binds the original driver to this relay's immutable
// options. Upstream secrets remain in the relay; only its local credential is
// supplied to the original process through stdin.
func (r *Relay) ConnectionParams(database string) (map[string]any, error) {
	if database == "" || len(database) > 1024 || strings.ContainsAny(database, "\x00\r\n") {
		return nil, connections.ErrInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, ErrRelayClosed
	}
	u := url.URL{Scheme: "databend", Host: r.listener.Addr().String(), Path: "/" + database, RawQuery: r.jdbcQuery}
	return map[string]any{"connection_string": "jdbc:" + u.String(), "database": database, "username": "nyadb", "password": r.password}, nil
}

func (r *Relay) QueryStatus(id string) (RelayQueryStatus, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	q, ok := r.queries[id]
	if !ok {
		return RelayQueryStatus{}, false
	}
	return RelayQueryStatus{Complete: q.complete, Uncertain: q.uncertain, Cached: q.cached, HTTPStatus: q.status}, true
}

func (r *Relay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		relayError(w, "original transport is closed")
		return
	}
	r.wg.Add(1)
	r.mu.Unlock()
	defer r.wg.Done()
	if subtle.ConstantTimeCompare([]byte(req.Header.Get("Authorization")), []byte(r.localAuth)) != 1 {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if req.Host != r.listener.Addr().String() || req.URL.IsAbs() || req.URL.RawQuery != "" && req.URL.RawQuery != r.jdbcQuery || req.URL.ForceQuery || req.URL.RawPath != "" {
		relayError(w, "request escaped the original target")
		return
	}
	isQuery := req.Method == http.MethodPost && req.URL.Path == "/v1/query"
	if !isQuery && !validRelayPath(req.Method, req.URL.Path) {
		relayError(w, "unsupported original HTTP operation")
		return
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, maxRequestBytes+1))
	if err != nil || len(body) > maxRequestBytes {
		relayError(w, "original request is incomplete or too large")
		return
	}
	var q *relayQuery
	if isQuery {
		id := req.Header.Get(queryIDHeader)
		if !validQueryID(id) {
			relayError(w, "original query ID is required")
			return
		}
		fingerprint := requestFingerprint(req, body)
		r.mu.Lock()
		q = r.queries[id]
		if q != nil {
			same := q.fingerprint == fingerprint
			r.mu.Unlock()
			if !same {
				relayError(w, "original query ID was reused for a different request")
				return
			}
			select {
			case <-q.done:
			case <-req.Context().Done():
				return
			case <-r.ctx.Done():
				relayError(w, "original transport is closed")
				return
			}
			if q.cached {
				writeRelayHeaders(w, q.headers)
				w.WriteHeader(q.status)
				_, _ = w.Write(q.body)
			} else {
				relayError(w, "original query receipt is unavailable; explicit recovery is required")
			}
			return
		}
		if len(r.queries) >= maxQueryIDs {
			r.mu.Unlock()
			relayError(w, "original transport capacity is exhausted; explicit recovery is required")
			return
		}
		q = &relayQuery{fingerprint: fingerprint, done: make(chan struct{})}
		r.queries[id] = q
		r.mu.Unlock()
	}
	r.forward(w, req, body, q)
}

func requestFingerprint(req *http.Request, body []byte) [32]byte {
	// Exclude socket/retry details but retain every original product/session
	// header. The same query ID cannot be redirected to a different session,
	// warehouse, tenant, result format, or request body.
	hash := sha256.New()
	keys := make([]string, 0, len(req.Header))
	for k := range req.Header {
		if strings.HasPrefix(strings.ToLower(k), "x-databend-") || k == "Content-Type" || k == "Accept" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		_, _ = io.WriteString(hash, k+"\x00")
		for _, value := range req.Header[k] {
			_, _ = io.WriteString(hash, value+"\x00")
		}
	}
	_, _ = hash.Write(body)
	var out [32]byte
	copy(out[:], hash.Sum(nil))
	return out
}

func validQueryID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, v := range id {
		if v < '0' || v > '9' && v < 'a' || v > 'f' {
			return false
		}
	}
	return true
}

func validRelayPath(method, path string) bool {
	if method == http.MethodPost {
		return path == "/v1/session/login" || path == "/v1/session/logout" || path == "/v1/session/heartbeat"
	}
	if method != http.MethodGet || !strings.HasPrefix(path, "/v1/query/") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/v1/query/"), "/")
	if !validQueryID(parts[0]) {
		return false
	}
	if len(parts) == 1 {
		return true
	}
	if len(parts) == 2 {
		return parts[1] == "final" || parts[1] == "kill"
	}
	if len(parts) == 3 && parts[1] == "page" && len(parts[2]) > 0 && len(parts[2]) <= 20 {
		for _, c := range parts[2] {
			if c < '0' || c > '9' {
				return false
			}
		}
		return true
	}
	return false
}

func (r *Relay) forward(w http.ResponseWriter, original *http.Request, body []byte, q *relayQuery) {
	target := r.target
	target.Path = original.URL.Path
	req, err := http.NewRequestWithContext(r.ctx, original.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		r.settle(q, 0, nil, nil, false, true)
		relayError(w, "original transport could not submit the request")
		return
	}
	req.Header = original.Header.Clone()
	stripHopHeaders(req.Header)
	req.Header.Del("Proxy-Authorization")
	req.Header.Del("Idempotency-Key")
	req.Header.Del("X-Idempotency-Key")
	req.Header.Del("Authorization")
	if r.auth != "" {
		req.Header.Set("Authorization", r.auth)
	}
	// JDBC encodes its base URI as the initial route hint. Replace only our
	// local authority; server-issued hints retain their exact original bytes.
	if hint := req.Header.Get("X-Databend-Route-Hint"); strings.HasSuffix(hint, "#") {
		if raw, err := base64.StdEncoding.DecodeString(strings.TrimSuffix(hint, "#")); err == nil {
			if route, err := url.Parse(string(raw)); err == nil && route.Scheme == "http" && route.Host == r.listener.Addr().String() {
				route.Scheme, route.Host = r.target.Scheme, r.target.Host
				req.Header.Set("X-Databend-Route-Hint", base64.StdEncoding.EncodeToString([]byte(route.String()))+"#")
			}
		}
	}
	// Go's transport must not reconstruct a POST after a connection failure.
	req.GetBody = nil
	response, err := r.client.Do(req)
	if err != nil {
		r.settle(q, 0, nil, nil, false, true)
		relayError(w, "original upstream receipt is unavailable; explicit recovery is required")
		return
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		// Never let the original trust-all JDBC client follow an upstream
		// redirect outside this certificate-verifying relay.
		_, err = io.Copy(io.Discard, response.Body)
		r.settle(q, response.StatusCode, nil, nil, err == nil, true)
		relayError(w, "original upstream redirect requires a configured target")
		return
	}
	writeRelayHeaders(w, response.Header)
	w.WriteHeader(response.StatusCode)
	capture := &receiptReader{source: response.Body}
	_, copyErr := io.Copy(w, capture)
	if copyErr != nil {
		// A local socket can disappear while the server has already committed.
		// Keep consuming the original response, without issuing another POST.
		_, err = io.Copy(io.Discard, capture)
	} else {
		err = nil
	}
	if capture.upstreamErr != nil {
		err = capture.upstreamErr
	}
	var cached []byte
	if !capture.large {
		cached = append([]byte{}, capture.body.Bytes()...)
	}
	r.settle(q, response.StatusCode, response.Header, cached, err == nil, err != nil)
	if err != nil {
		// A short/failed upstream body must not be framed as a complete HTTP
		// response. JDBC may retry locally; the tombstone blocks resubmission.
		panic(http.ErrAbortHandler)
	}
}

func (r *Relay) settle(q *relayQuery, status int, headers http.Header, body []byte, complete, uncertain bool) {
	if q == nil {
		return
	}
	r.mu.Lock()
	q.status, q.complete, q.uncertain = status, complete, uncertain
	if complete && !uncertain && body != nil && len(body) <= maxCachedReceipt && r.cacheSize+len(body) <= maxCacheBytes {
		q.headers, q.body, q.cached = headers.Clone(), bytes.Clone(body), true
		r.cacheSize += len(body)
	}
	close(q.done)
	r.mu.Unlock()
}

// receiptReader always consumes the full original body but bounds the replay
// cache. An oversized response reaches its first caller unchanged; its ID is
// retained without a cache and can never cause another upstream execution.
type receiptReader struct {
	source      io.Reader
	body        bytes.Buffer
	large       bool
	upstreamErr error
}

func (r *receiptReader) Read(p []byte) (int, error) {
	n, err := r.source.Read(p)
	if err != nil && err != io.EOF {
		r.upstreamErr = err
	}
	if !r.large {
		if r.body.Len()+n > maxCachedReceipt {
			r.large = true
			r.body.Reset()
		} else {
			_, _ = r.body.Write(p[:n])
		}
	}
	return n, err
}

func writeRelayHeaders(w http.ResponseWriter, original http.Header) {
	headers := original.Clone()
	stripHopHeaders(headers)
	headers.Del("Location")
	for k, values := range headers {
		w.Header()[k] = values
	}
}

func stripHopHeaders(h http.Header) {
	for _, name := range strings.Split(h.Get("Connection"), ",") {
		h.Del(strings.TrimSpace(name))
	}
	for _, name := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "TE", "Trailer", "Transfer-Encoding", "Upgrade"} {
		h.Del(name)
	}
}

func relayError(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	_, _ = io.WriteString(w, "{\"error\":{\"message\":\""+message+"\"}}")
}

func (r *Relay) Close() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		r.mu.Lock()
		r.closed = true
		r.cancel()
		r.mu.Unlock()
		_ = r.server.Close()
		<-r.serveDone
		r.wg.Wait()
		r.transport.CloseIdleConnections()
		r.mu.Lock()
		clear(r.queries)
		r.cacheSize = 0
		r.mu.Unlock()
	})
}

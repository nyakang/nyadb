package easysearch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func sessionProfile(t *testing.T, cfg connections.EasysearchConfig) connections.Profile {
	t.Helper()
	u, err := url.Parse(cfg.Endpoint)
	if err != nil {
		t.Fatal("invalid fixture endpoint")
	}
	p, _ := connections.Defaults(connections.Easysearch)
	p.ID, p.Name, p.Host = 101, "native session", u.Hostname()
	n, err := strconv.ParseUint(u.Port(), 10, 16)
	if err != nil {
		t.Fatal("fixture port missing")
	}
	p.Port = uint16(n)
	p.Username = cfg.Username
	p.EasysearchOptions.Auth = cfg.Auth
	if p.EasysearchOptions.Auth == "" {
		p.EasysearchOptions.Auth = "none"
	}
	p.EasysearchOptions.CAFile, p.EasysearchOptions.ServerName = cfg.CAFile, cfg.ServerName
	if u.Scheme == "http" {
		p.TLSMode = "disable"
	}
	return p
}

func TestEasysearchSessionPhysicalClientRejectsChangedCluster(t *testing.T) {
	var changed atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := nativeIdentity
		if changed.Load() {
			raw = strings.Replace(raw, "own-cluster", "foreign-cluster", 1)
		}
		_, _ = io.WriteString(w, raw)
	}))
	defer server.Close()
	raw, err := OpenSession(context.Background(), sessionProfile(t, connections.EasysearchConfig{Endpoint: server.URL}), "")
	if err != nil {
		t.Fatal(err)
	}
	s := raw.(*Session)
	defer s.Close()
	namespaces, err := s.Namespaces(context.Background())
	if err != nil || len(namespaces) != 1 || namespaces[0].Ref.Catalog != "own-cluster" || namespaces[0].Label != "nyadb" || s.Namespace("nyadb") != namespaces[0].Ref {
		t.Fatal("cluster label became namespace identity")
	}
	l, err := s.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	changed.Store(true)
	if err = l.Preconnect(context.Background()); !errors.Is(err, contract.ErrRevoked) || s.Alive() {
		t.Fatal("new physical handle admitted another cluster", err)
	}
	if _, err = s.Namespaces(context.Background()); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("retired cluster kept publishing metadata")
	}
}

func TestEasysearchQueryReleaseWaitsCapturedHTTPAcknowledgement(t *testing.T) {
	c := fakeClient(t, func(http.ResponseWriter, *http.Request) {})
	raw, err := OpenSession(context.Background(), sessionProfile(t, c.config), "")
	if err != nil {
		t.Fatal(err)
	}
	s := raw.(*Session)
	defer s.Close()
	lease, err := s.ReserveQuery(2)
	if err != nil {
		t.Fatal(err)
	}
	l := lease.(*queryLease)
	if err = l.Preconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	l.client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		close(canceled)
		<-release
		return nil, r.Context().Err()
	})
	completed := make(chan error, 1)
	go func() {
		_, err := l.Execute(context.Background(), "nyadb", "PUT /own/_create/one\n{\"n\":9007199254740993}")
		completed <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker not started")
	}
	closed := make(chan struct{})
	go func() { l.Release(); close(closed) }()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("Release did not cancel captured request")
	}
	select {
	case <-closed:
		t.Fatal("Release returned before HTTP acknowledgement")
	default:
	}
	close(release)
	select {
	case err := <-completed:
		if !errors.Is(err, contract.ErrSQLSessionLost) {
			t.Fatal("interrupted lease did not require explicit recovery", err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not settle")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Release did not finish")
	}
	if _, err = l.Execute(context.Background(), "nyadb", "GET /"); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("released lease executed")
	}
	if l.client != nil {
		t.Fatal("released physical client retained")
	}
}

func TestEasysearchNativeMappingColumnsPreserveMembersAndRejectAmbiguity(t *testing.T) {
	cols, err := mappingColumns([]byte(`{"one":{"mappings":{"properties":{"n":{"type":"long"},"obj":{"properties":{"child":{"type":"keyword"}}},"text":{"type":"text","fields":{"raw":{"type":"keyword"}}}}}},"two":{"mappings":{"properties":{"n":{"type":"keyword"}}}}}`))
	if err != nil || len(cols) != 6 || cols[0].Name != "one:n" || cols[0].Type != "long" || cols[5].Name != "two:n" || cols[5].Type != "keyword" {
		t.Fatal("alias mappings conflated incompatible member fields", err, cols)
	}
	for _, raw := range []string{`{"one":{"mappings":{"properties":{"a.b":{"type":"long"},"a":{"properties":{"b":{"type":"keyword"}}}}}}}`, `{"one":{"mappings":{"properties":{"n":{"type":"long"},"n":{"type":"keyword"}}}}}`} {
		if _, err = mappingColumns([]byte(raw)); err == nil {
			t.Fatal("ambiguous native mapping accepted")
		}
	}
}

func TestEasysearchResolveDoesNotHidePermissionOrPartialMetadata(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{{403, `{"error":{"type":"security_exception"},"status":403}`}, {200, `{"indices":[],"aliases":[]}`}, {200, `{"indices":[],"aliases":[{"name":"alias","indices":[]}],"data_streams":[]}`}} {
		c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = io.WriteString(w, tc.body)
		})
		if _, err := c.ResolveTargets(context.Background(), "*"); err == nil {
			t.Fatal("denied or incomplete resolution became an empty success")
		}
	}
}

const nativeIdentity = `{"name":"native","cluster_name":"nyadb","cluster_uuid":"own-cluster","version":{"number":"2.5.1","distribution":"easysearch","distributor":"INFINI Labs","build_hash":"c8206f7d5ec3da50ae75b7db483cefb90a0b726b","build_snapshot":false,"lucene_version":"9.12.2"},"tagline":"You Know, For Easy Search!"}`

func fakeClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			_, _ = io.WriteString(w, nativeIdentity)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	c, err := Open(context.Background(), connections.EasysearchConfig{Endpoint: server.URL, Auth: "none"}, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestEasysearchMetadataRejectsOriginalIndexUUIDAndAliasDefinitionChanges(t *testing.T) {
	for _, change := range []string{"uuid", "members", "filter", "stable"} {
		t.Run(change, func(t *testing.T) {
			var resolves, aliases, settings int
			c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasPrefix(r.URL.Path, "/_resolve/index/"):
					resolves++
					member := "one"
					if change == "members" && resolves > 1 {
						member = "two"
					}
					_, _ = io.WriteString(w, `{"indices":[],"aliases":[{"name":"target","indices":["`+member+`"]}],"data_streams":[]}`)
				case strings.Contains(r.URL.Path, "/_settings"):
					settings++
					uuid := "original"
					if change == "uuid" && settings > 1 {
						uuid = "replacement"
					}
					_, _ = io.WriteString(w, `{"one":{"settings":{"index.uuid":"`+uuid+`"}}}`)
				case strings.HasSuffix(r.URL.Path, "/_mapping"):
					_, _ = io.WriteString(w, `{"one":{"mappings":{"properties":{"n":{"type":"long"}}}}}`)
				case strings.HasPrefix(r.URL.Path, "/_alias/"):
					aliases++
					value := "1"
					if change == "filter" && aliases > 1 {
						value = "2"
					}
					_, _ = io.WriteString(w, `{"one":{"aliases":{"target":{"filter":{"term":{"n":`+value+`}}}}}}`)
				default:
					t.Error("unexpected metadata request", r.URL.Path)
				}
			})
			m, err := c.ReadTargetMetadata(context.Background(), "target", "alias")
			if change == "stable" {
				if err != nil || m.IndexUUIDs["one"] != "original" || len(m.Settings) == 0 {
					t.Fatal("stable native metadata lost original identity", err)
				}
			} else if !errors.Is(err, ErrTargetChanged) {
				t.Fatal("replacement metadata published as original", err)
			}
		})
	}
	for _, raw := range []string{`{"one":{"settings":{}}}`, `{"one":{"settings":{"index.uuid":""}}}`, `{"one":{"settings":{"index.uuid":"bad\btoken"}}}`, `{"two":{"settings":{"index.uuid":"other"}}}`, `{"one":{"settings":{"index.uuid":"one","index.uuid":"two"}}}`} {
		if _, err := metadataIndexUUIDs([]byte(raw), []string{"one"}); err == nil {
			t.Fatal("missing or ambiguous original UUID accepted")
		}
	}
}

func TestEasysearchQueryInterruptedHandshakeRequiresExplicitReplacement(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/" {
			if calls.Add(1) == 2 {
				conn, _, _ := w.(http.Hijacker).Hijack()
				_ = conn.Close()
				return
			}
			_, _ = io.WriteString(w, nativeIdentity)
			return
		}
		_, _ = io.WriteString(w, `{"n":9007199254740993}`)
	}))
	defer server.Close()
	raw, err := OpenSession(context.Background(), sessionProfile(t, connections.EasysearchConfig{Endpoint: server.URL}), "")
	if err != nil {
		t.Fatal(err)
	}
	s := raw.(*Session)
	defer s.Close()
	l, err := s.ReserveQuery(10)
	if err != nil {
		t.Fatal(err)
	}
	if err = l.Preconnect(context.Background()); !errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatal("interrupted original handshake did not settle as lost", err)
	}
	if err = l.Preconnect(context.Background()); !errors.Is(err, contract.ErrSQLSessionLost) || calls.Load() != 2 {
		t.Fatal("handshake was implicitly retried", err)
	}
	if _, err = l.Execute(context.Background(), "nyadb", "GET /value"); !errors.Is(err, contract.ErrSQLSessionLost) || calls.Load() != 2 {
		t.Fatal("lost lease silently opened a new client", err)
	}
	fresh, err := l.Replace()
	if err != nil {
		t.Fatal(err)
	}
	result, err := fresh.Execute(context.Background(), "nyadb", "GET /value")
	if err != nil || calls.Load() != 3 || len(result.Sets) != 1 || result.Sets[0].Rows[0][1].Kind != contract.JSON || result.Sets[0].Rows[0][1].Text != `{"n":9007199254740993}` {
		t.Fatal("explicit replacement failed to retain exact JSON", err)
	}
	if _, err = l.Execute(context.Background(), "nyadb", "GET /"); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("original lease resurrected")
	}
}

func TestEasysearchSessionCloseRevokesIndependentLeasesAndNativeBodyKinds(t *testing.T) {
	c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/text" {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, "native text\n")
		} else {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"n":9007199254740993}`)
		}
	})
	raw, err := OpenSession(context.Background(), sessionProfile(t, c.config), "")
	if err != nil {
		t.Fatal(err)
	}
	s := raw.(*Session)
	defer s.Close()
	first, _ := s.ReserveQuery(1)
	second, _ := s.ReserveQuery(2)
	if err = first.Preconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = second.Preconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	first.Release()
	result, err := second.Execute(context.Background(), "nyadb", "GET /text")
	if err != nil || result.Sets[0].Rows[0][1].Kind != contract.Text || result.Sets[0].Rows[0][1].Text != "native text\n" {
		t.Fatal("closing sibling damaged original native text response", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = second.Execute(context.Background(), "nyadb", "GET /"); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("terminal session left a physical lease active", err)
	}
	if _, err = second.Replace(); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("terminal session recovered implicitly", err)
	}
	if len(s.leases) != 0 || s.secret != "" {
		t.Fatal("terminal session retained leases or credentials")
	}
}

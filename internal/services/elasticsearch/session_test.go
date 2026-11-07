package elasticsearch

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

func sessionProfile(t *testing.T, cfg connections.ElasticsearchConfig) connections.Profile {
	t.Helper()
	u, err := url.Parse(cfg.Endpoint)
	if err != nil {
		t.Fatal("invalid fixture endpoint")
	}
	p, _ := connections.Defaults(connections.Elasticsearch)
	p.ID, p.Name, p.Host = 101, "native session", u.Hostname()
	n, err := strconv.ParseUint(u.Port(), 10, 16)
	if err != nil {
		t.Fatal("fixture port missing")
	}
	p.Port = uint16(n)
	p.Username = cfg.Username
	p.ElasticsearchOptions.Auth = cfg.Auth
	if p.ElasticsearchOptions.Auth == "" {
		p.ElasticsearchOptions.Auth = "none"
	}
	p.ElasticsearchOptions.CAFile, p.ElasticsearchOptions.ServerName = cfg.CAFile, cfg.ServerName
	if u.Scheme == "http" {
		p.TLSMode = "disable"
	}
	return p
}

func TestElasticsearchSessionPhysicalClientRejectsChangedCluster(t *testing.T) {
	var changed atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		raw := nativeIdentity
		if changed.Load() {
			raw = strings.Replace(raw, "own-cluster", "foreign-cluster", 1)
		}
		_, _ = io.WriteString(w, raw)
	}))
	defer server.Close()
	raw, err := OpenSession(context.Background(), sessionProfile(t, connections.ElasticsearchConfig{Endpoint: server.URL}), "")
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

func TestElasticsearchQueryReleaseWaitsCapturedHTTPAcknowledgement(t *testing.T) {
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

func TestElasticsearchNativeMappingColumnsPreserveMembersAndRejectAmbiguity(t *testing.T) {
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

func TestElasticsearchResolveDoesNotHidePermissionOrPartialMetadata(t *testing.T) {
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

func TestElasticsearchTargetMetadataRejectsChangedAliasAndStreamMembers(t *testing.T) {
	for _, kind := range []string{"alias", "data_stream"} {
		c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasPrefix(r.URL.Path, "/_resolve/index/"):
				if kind == "alias" {
					_, _ = io.WriteString(w, `{"indices":[],"aliases":[{"name":"target","indices":["one"]}],"data_streams":[]}`)
				} else {
					_, _ = io.WriteString(w, `{"indices":[],"aliases":[],"data_streams":[{"name":"target","backing_indices":["one"]}]}`)
				}
			case strings.HasSuffix(r.URL.Path, "/_mapping"):
				_, _ = io.WriteString(w, `{"one":{"mappings":{}}}`)
			case strings.HasPrefix(r.URL.Path, "/_alias/"):
				_, _ = io.WriteString(w, `{"one":{"aliases":{"other":{}}}}`)
			case strings.HasPrefix(r.URL.Path, "/_data_stream/"):
				_, _ = io.WriteString(w, `{"data_streams":[{"name":"target","indices":[{"index_name":"other"}]}]}`)
			default:
				t.Error("unexpected metadata request")
			}
		})
		if _, err := c.ReadTargetMetadata(context.Background(), "target", kind); !errors.Is(err, ErrProtocol) {
			t.Fatal("changed members were published as original target metadata", kind, err)
		}
	}
	if _, err := mappingColumns([]byte(`{"one":{}}`)); err == nil {
		t.Fatal("missing native mappings envelope accepted")
	}
}

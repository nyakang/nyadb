package meilisearch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

const mockIndex = `{"uid":"owned","createdAt":"2026-11-08T08:00:00.123456789Z","updatedAt":"2026-11-08T08:01:00.987654321Z","primaryKey":"id","future":9007199254740993}`

func profileConfig(t *testing.T, cfg connections.MeilisearchConfig) connections.Profile {
	t.Helper()
	cfg, err := connections.NormalizeMeilisearchConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(cfg.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	p, err := connections.Defaults(connections.Meilisearch)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.ParseUint(u.Port(), 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	p.ID, p.Name, p.Host, p.Port = 71, "owned original profile", u.Hostname(), uint16(port)
	if u.Scheme == "http" {
		p.TLSMode = "disable"
	} else {
		p.TLSMode = "verify-full"
	}
	p.MeilisearchOptions = connections.MeilisearchOptions{Version: 1, Auth: cfg.Auth, ContextPath: u.Path, IndexUID: cfg.IndexUID, CAFile: cfg.CAFile, ServerName: cfg.ServerName, ClientCertFile: cfg.ClientCertFile, ClientKeyFile: cfg.ClientKeyFile, ConnectTimeoutSeconds: cfg.ConnectTimeoutSeconds, RequestTimeoutSeconds: cfg.RequestTimeoutSeconds}
	return p
}
func testSession(t *testing.T, cfg connections.MeilisearchConfig, secret string) *Session {
	t.Helper()
	raw, err := OpenSession(context.Background(), profileConfig(t, cfg), secret)
	if err != nil {
		t.Fatal(err)
	}
	s := raw.(*Session)
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func sessionServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/health":
			fmt.Fprint(w, `{"status":"available"}`)
		case "/version":
			fmt.Fprint(w, mockVersion)
		case "/indexes":
			if r.Method == "GET" && r.URL.Query().Get("limit") == "1" {
				fmt.Fprint(w, `{"results":[],"offset":0,"limit":1,"total":0}`)
				return
			}
			handler(w, r)
		default:
			handler(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}
func TestMeilisearchSessionQualifiedIndexesAndIndependentDefinitions(t *testing.T) {
	settings := ` {"rankingRules":["words","typo"],"displayedAttributes":["*"],"future":{"exact":9007199254740993,"decimal":1.2300e-20,"text":"原生🐱"}} `
	var calls atomic.Int32
	server := sessionServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Path {
		case "/indexes":
			fmt.Fprint(w, `{"results":[`+strings.Replace(mockIndex, `"owned"`, `"other"`, 1)+`,`+mockIndex+`],"offset":0,"limit":256,"total":2}`)
		case "/indexes/owned", "/indexes/other":
			fmt.Fprint(w, strings.Replace(mockIndex, `"owned"`, strconv.Quote(strings.TrimPrefix(r.URL.Path, "/indexes/")), 1))
		case "/indexes/owned/settings", "/indexes/other/settings":
			fmt.Fprint(w, settings)
		case "/indexes/owned/stats":
			fmt.Fprint(w, `{"numberOfDocuments":2,"isIndexing":false,"fieldDistribution":{"id":2}}`)
		default:
			w.WriteHeader(404)
		}
	})
	s := testSession(t, connections.MeilisearchConfig{Endpoint: server.URL}, "")
	ns, err := s.Namespaces(context.Background())
	if err != nil || len(ns) != 2 || ns[1].Ref != s.Namespace("owned") {
		t.Fatal(ns, err)
	}
	var refs []contract.ObjectRef
	for _, name := range []string{"owned", "other"} {
		list, err := s.Browse(context.Background(), s.Namespace(name))
		if err != nil || len(list) != 5 || list[0].CreatedAt != "2026-11-08T08:00:00.123456789Z" || !strings.Contains(list[0].Definition, `"primaryKey":"id"`) {
			t.Fatal(list, err)
		}
		m, err := s.TableMetadata(context.Background(), list[2].Ref)
		if err != nil || m.Editable || len(m.Columns) != 0 || m.Definition != settings || len(m.Definitions) != 1 || m.Definitions[0].SQL != settings {
			t.Fatal(m, err)
		}
		refs = append(refs, list[2].Ref)
	}
	if refs[0] == refs[1] {
		t.Fatal("same-name settings aliased")
	}
	before := calls.Load()
	for _, n := range []contract.NamespaceRef{{ProfileID: 72, Catalog: "owned"}, {ProfileID: 71, Catalog: "owned", Schema: "sql"}, {ProfileID: 71, Catalog: "../escape"}} {
		if _, err = s.Browse(context.Background(), n); !errors.Is(err, connections.ErrInvalid) {
			t.Fatal(err)
		}
	}
	if _, err = s.TableMetadata(context.Background(), s.Namespace("owned").Object("table", "settings")); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal(err)
	}
	if _, err = s.TableMetadata(context.Background(), s.Namespace("owned").Object("settings", "wrong")); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal(err)
	}
	if calls.Load() != before {
		t.Fatal("invalid identity dispatched")
	}
	if s.Capabilities() != (contract.Capabilities{EditableTables: true, Query: true, Metadata: true, Objects: true, Documents: true, Count: true, FieldChunks: true, Export: true, JSONL: true}) {
		t.Fatal(s.Capabilities())
	}
	if _, err := contract.Require[contract.DataDocumentsProvider](s); err != nil {
		t.Fatal("public document provider unavailable", err)
	}
}
func TestMeilisearchIndexPaginationCompletenessAndChangingPages(t *testing.T) {
	for _, mode := range []string{"complete", "duplicate", "total_changed", "short_page", "wrong_offset", "bounds"} {
		t.Run(mode, func(t *testing.T) {
			var pages atomic.Int32
			server := sessionServer(t, func(w http.ResponseWriter, r *http.Request) {
				pages.Add(1)
				offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
				start, end, total, reported := offset, 300, 300, offset
				if end-start > 256 {
					end = start + 256
				}
				if mode == "short_page" {
					end = start + 1
				}
				if mode == "bounds" {
					total = 4097
				}
				if offset > 0 {
					switch mode {
					case "duplicate":
						start = 0
						end = 44
					case "total_changed":
						total = 301
					case "wrong_offset":
						reported = 0
					}
				}
				fmt.Fprint(w, `{"results":[`)
				for i := start; i < end; i++ {
					if i > start {
						fmt.Fprint(w, ",")
					}
					fmt.Fprint(w, strings.Replace(mockIndex, `"owned"`, fmt.Sprintf(`"index_%04d"`, i), 1))
				}
				fmt.Fprintf(w, `],"offset":%d,"limit":256,"total":%d}`, reported, total)
			})
			s := testSession(t, connections.MeilisearchConfig{Endpoint: server.URL}, "")
			out, err := s.Namespaces(context.Background())
			if mode == "complete" {
				if err != nil || len(out) != 300 || pages.Load() != 2 {
					t.Fatal(len(out), pages.Load(), err)
				}
			} else if err == nil || out != nil {
				t.Fatal("bad pagination claimed complete", mode, len(out), err)
			}
		})
	}
}
func TestMeilisearchScopedMetadataPermissionFailureDoesNotRevokeAllowedReads(t *testing.T) {
	var escaped atomic.Int32
	server := sessionServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/indexes/owned":
			fmt.Fprint(w, mockIndex)
		case "/indexes/owned/settings", "/indexes/owned/stats":
			w.WriteHeader(403)
			fmt.Fprint(w, `{"code":"invalid_api_key","message":"private native response"}`)
		case "/indexes/owned/documents":
			fmt.Fprint(w, `{"results":[],"offset":0,"limit":20,"total":0}`)
		default:
			escaped.Add(1)
			w.WriteHeader(404)
		}
	})
	s := testSession(t, connections.MeilisearchConfig{Endpoint: server.URL, IndexUID: "owned"}, "")
	if s.Identity() != (Identity{}) {
		t.Fatal("scoped binary observation fabricated")
	}
	ns, err := s.Namespaces(context.Background())
	if err != nil || len(ns) != 1 || ns[0].Ref != s.Namespace("owned") {
		t.Fatal(ns, err)
	}
	list, err := s.Browse(context.Background(), ns[0].Ref)
	if err != nil || len(list) != 5 {
		t.Fatal(list, err)
	}
	for _, i := range []int{2, 3} {
		_, err = s.TableMetadata(context.Background(), list[i].Ref)
		var status *HTTPError
		if !errors.As(err, &status) || status.Status != 403 || !s.Alive() || strings.Contains(err.Error(), "private native response") {
			t.Fatal(err)
		}
	}
	l, _ := s.ReserveQuery(1)
	for _, text := range []string{"GET /version", "GET /indexes", "GET /indexes/owned_other/documents", "GET /tasks/0", "POST /multi-search\n{}"} {
		if _, err = l.Execute(context.Background(), "owned", text); !errors.Is(err, contract.ErrUnsupported) {
			t.Fatal(text, err)
		}
	}
	if _, err = l.Execute(context.Background(), "other", "GET /indexes/owned"); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal(err)
	}
	if _, err = l.Execute(context.Background(), "owned", "GET /indexes/owned/documents"); err != nil || escaped.Load() != 0 {
		t.Fatal("allowed read revoked or escaped", escaped.Load(), err)
	}
}
func TestMeilisearchMetadataMalformedIndexSettingsAndDeniedList(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, strings.Replace(mockIndex, `"primaryKey":"id"`, `"primaryKey":false`, 1), strings.Replace(mockIndex, `"updatedAt":"2026-11-08T08:01:00.987654321Z"`, `"updatedAt":null`, 1), strings.Replace(mockIndex, `"uid":"owned"`, `"uid":"../escape"`, 1)} {
		if _, err := parseIndex([]byte(raw), "owned"); err == nil {
			t.Fatal("malformed index accepted", raw)
		}
	}
	server := sessionServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/settings") {
			fmt.Fprint(w, `[]`)
		} else {
			w.WriteHeader(403)
		}
	})
	s := testSession(t, connections.MeilisearchConfig{Endpoint: server.URL}, "")
	if out, err := s.Namespaces(context.Background()); err == nil || out != nil {
		t.Fatal("denied list hidden", out, err)
	}
	if _, err := s.TableMetadata(context.Background(), s.Namespace("owned").Object("settings", "settings")); !errors.Is(err, ErrProtocol) {
		t.Fatal(err)
	}
}
func TestMeilisearchLeaseAdmissionUnknownAndSynchronousOutcomes(t *testing.T) {
	var calls atomic.Int32
	server := sessionServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Path {
		case "/indexes/owned/documents":
			w.WriteHeader(202)
			fmt.Fprint(w, mockReceipt)
		case "/malformed":
			w.WriteHeader(202)
			fmt.Fprint(w, `{"taskUid":0,"status":"enqueued"}`)
		case "/keys":
			w.WriteHeader(201)
			fmt.Fprint(w, `{"uid":"original","key":"native-test-key"}`)
		default:
			fmt.Fprint(w, `{"alive":true}`)
		}
	})
	s := testSession(t, connections.MeilisearchConfig{Endpoint: server.URL}, "")
	l, _ := s.ReserveQuery(1)
	var streamed string
	r, err := l.ExecuteStream(context.Background(), "owned", "POST /indexes/owned/documents\n[]", func(_ int, cols []contract.Column, row []contract.Cell) error {
		streamed = row[1].Text
		row[1].Text = "changed"
		cols[0].Name = "changed"
		return nil
	})
	if err != nil || r.Steps[0].State != contract.StatementEnqueued || r.AffectedRows != 0 || r.Sets[0].AffectedRows != 0 || streamed != mockReceipt || r.Sets[0].Rows[0][1].Text != mockReceipt || r.Sets[0].Columns[0].Name != "HTTP status" {
		t.Fatal(r, err)
	}
	sinkError := errors.New("presentation closed")
	r, err = l.ExecuteStream(context.Background(), "", "POST /indexes/owned/documents\n[]", func(int, []contract.Column, []contract.Cell) error { return sinkError })
	if !errors.Is(err, sinkError) || r.Steps[0].State != contract.StatementEnqueued {
		t.Fatal(r, err)
	}
	r, err = l.Execute(context.Background(), "", "POST /keys\n{}")
	if err != nil || r.Steps[0].State != "completed" || r.AffectedRows != 0 || r.Sets[0].Rows[0][0].Text != "201" {
		t.Fatal(r, err)
	}
	r, err = l.Execute(context.Background(), "", "POST /malformed\n{}")
	if !errors.Is(err, contract.ErrSQLSessionLost) || r.Steps[0].State != "uncertain" || r.Sets[0].Rows[0][1].Text != `{"taskUid":0,"status":"enqueued"}` {
		t.Fatal(r, err)
	}
	before := calls.Load()
	if _, err = l.Execute(context.Background(), "", "POST /malformed\n{}"); !errors.Is(err, contract.ErrSQLSessionLost) || calls.Load() != before {
		t.Fatal("unknown operation replayed", err)
	}
	replacement, err := l.Replace()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = replacement.Execute(context.Background(), "", "GET /alive"); err != nil || calls.Load() != before+1 {
		t.Fatal("replacement replayed prior write", calls.Load(), err)
	}
}
func TestMeilisearchLeaseCancelReleaseAndCloseSettleOriginalOnly(t *testing.T) {
	for _, action := range []string{"cancel", "release", "close"} {
		t.Run(action, func(t *testing.T) {
			entered, acked := make(chan struct{}), make(chan struct{})
			var writes atomic.Int32
			server := sessionServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					_, _ = io.Copy(io.Discard, r.Body)
					writes.Add(1)
					close(entered)
					<-r.Context().Done()
					close(acked)
					return
				}
				fmt.Fprint(w, `{"alive":true}`)
			})
			cfg := connections.MeilisearchConfig{Endpoint: server.URL}
			s := testSession(t, cfg, "")
			first, _ := s.ReserveQuery(1)
			other, _ := s.ReserveQuery(2)
			fresh, _ := testSession(t, cfg, "").ReserveQuery(1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type outcome struct {
				r   contract.SQLResult
				err error
			}
			settled := make(chan outcome, 1)
			go func() {
				r, err := first.Execute(ctx, "owned", "POST /indexes/owned/documents\n[]")
				settled <- outcome{r, err}
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("executor not entered")
			}
			if action == "cancel" {
				cancel()
			} else {
				var wg sync.WaitGroup
				for i := 0; i < 2; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						if action == "close" {
							_ = s.Close()
						} else {
							first.Release()
						}
					}()
				}
				wg.Wait()
			}
			select {
			case o := <-settled:
				if !errors.Is(o.err, contract.ErrSQLSessionLost) || !errors.Is(o.err, context.Canceled) || o.r.Steps[0].State != "uncertain" {
					t.Fatal(o.r, o.err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("executor not settled")
			}
			select {
			case <-acked:
			case <-time.After(5 * time.Second):
				t.Fatal("server not canceled")
			}
			if action != "close" {
				if _, err := other.Execute(context.Background(), "", "GET /alive"); err != nil {
					t.Fatal("other lease canceled", err)
				}
			}
			if _, err := fresh.Execute(context.Background(), "", "GET /alive"); err != nil {
				t.Fatal("other session canceled", err)
			}
			if action == "cancel" {
				replacement, err := first.Replace()
				if err != nil {
					t.Fatal(err)
				}
				if _, err = replacement.Execute(context.Background(), "", "GET /alive"); err != nil {
					t.Fatal(err)
				}
			}
			if writes.Load() != 1 {
				t.Fatal("write replayed")
			}
		})
	}
}
func TestMeilisearchMetadataCloseSettlesAndReservationAdmissionBound(t *testing.T) {
	entered := make(chan struct{})
	server := sessionServer(t, func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done() })
	s := testSession(t, connections.MeilisearchConfig{Endpoint: server.URL}, "")
	if _, err := s.ReserveQuery(0); !errors.Is(err, contract.ErrSQLSessionLimit) {
		t.Fatal(err)
	}
	for i := uint64(1); i <= 32; i++ {
		if _, err := s.ReserveQuery(i); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ReserveQuery(33); !errors.Is(err, contract.ErrSQLSessionLimit) {
		t.Fatal(err)
	}
	settled := make(chan error, 1)
	go func() { _, err := s.Namespaces(context.Background()); settled <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("metadata not entered")
	}
	_ = s.Close()
	select {
	case err := <-settled:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("metadata executor not settled")
	}
	if _, err := s.ReserveQuery(1); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed session admitted lease", err)
	}
}

func TestMeilisearchChangedBinaryObservationRevokesOriginalSessionWithoutReplay(t *testing.T) {
	var changed atomic.Bool
	var dispatched atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			fmt.Fprint(w, `{"status":"available"}`)
		case "/version":
			version := mockVersion
			if changed.Load() {
				version = strings.Replace(version, "1.56.1", "1.56.2", 1)
			}
			fmt.Fprint(w, version)
		case "/indexes":
			fmt.Fprint(w, `{"results":[],"offset":0,"limit":1,"total":0}`)
		default:
			dispatched.Add(1)
			fmt.Fprint(w, `{}`)
		}
	}))
	defer server.Close()
	s := testSession(t, connections.MeilisearchConfig{Endpoint: server.URL}, "")
	l, _ := s.ReserveQuery(1)
	if err := l.Preconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	changed.Store(true)
	if _, err := l.Execute(context.Background(), "", "POST /indexes/owned/documents\n[]"); !errors.Is(err, contract.ErrSQLSessionLost) || s.Alive() || dispatched.Load() != 0 {
		t.Fatal("binary drift did not revoke original admission", err)
	}
	if _, err := l.Replace(); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("replacement recovered revoked observation automatically", err)
	}
	// A fresh explicitly opened session may observe the new binary. Neither
	// old nor new observation asserts atomic service/index incarnation.
	fresh := testSession(t, connections.MeilisearchConfig{Endpoint: server.URL}, "")
	if fresh.Identity().Version != "1.56.2" {
		t.Fatal("fresh binary observation lost")
	}
}

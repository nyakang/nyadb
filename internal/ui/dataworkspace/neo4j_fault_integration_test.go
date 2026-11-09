package dataworkspace

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/services/neo4j"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// This owned relay forwards the exact native page RUN and withholds its reply.
// It does not synthesize data or responses, and counts only the armed request.
type neo4jDataRelay struct {
	listener   net.Listener
	fixture    neo4jDataFixture
	needle     []byte
	mu         sync.Mutex
	pairs      []net.Conn
	closed     bool
	wg         sync.WaitGroup
	hold       atomic.Bool
	armed      atomic.Bool
	dispatches atomic.Int32
	observed   chan struct{}
}

func newNeo4jDataRelay(t *testing.T, f neo4jDataFixture, needle string) *neo4jDataRelay {
	t.Helper()
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	r := &neo4jDataRelay{listener: ln, fixture: f, needle: []byte(needle), observed: make(chan struct{}, 1)}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		for {
			client, e := ln.Accept()
			if e != nil {
				return
			}
			r.wg.Add(1)
			go r.pipe(client)
		}
	}()
	t.Cleanup(r.Close)
	return r
}
func (r *neo4jDataRelay) pipe(client net.Conn) {
	defer r.wg.Done()
	defer client.Close()
	u, _ := url.Parse(r.fixture.Config.URI)
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	var server net.Conn
	var err error
	if strings.Contains(u.Scheme, "+") {
		pem, e := os.ReadFile(r.fixture.Config.CAFile)
		if e != nil {
			return
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return
		}
		cfg := &tls.Config{RootCAs: roots, ServerName: u.Hostname(), MinVersion: tls.VersionTLS12}
		if r.fixture.Config.ClientCertFile != "" {
			cert, e := tls.LoadX509KeyPair(r.fixture.Config.ClientCertFile, r.fixture.Config.ClientKeyFile)
			if e != nil {
				return
			}
			cfg.Certificates = []tls.Certificate{cert}
		}
		server, err = tls.DialWithDialer(dialer, "tcp", u.Host, cfg)
	} else {
		server, err = dialer.Dial("tcp", u.Host)
	}
	if err != nil {
		return
	}
	defer server.Close()
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.pairs = append(r.pairs, client, server)
	r.mu.Unlock()
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		buf := make([]byte, 4096)
		for {
			n, e := server.Read(buf)
			if n > 0 && !r.hold.Load() {
				if _, w := client.Write(buf[:n]); w != nil {
					return
				}
			}
			if e != nil {
				return
			}
		}
	}()
	buf := make([]byte, 4096)
	history := []byte{}
	for {
		n, e := client.Read(buf)
		triggered := false
		if n > 0 {
			history = append(history, buf[:n]...)
			if !r.armed.Load() {
				history = nil
			}
			for {
				i := bytes.Index(history, r.needle)
				if i < 0 {
					break
				}
				r.dispatches.Add(1)
				r.hold.Store(true)
				triggered = true
				history = history[i+len(r.needle):]
			}
			if len(history) > len(r.needle) {
				history = history[len(history)-len(r.needle):]
			}
			if _, w := server.Write(buf[:n]); w != nil {
				break
			}
			if triggered {
				select {
				case r.observed <- struct{}{}:
				default:
				}
			}
		}
		if e != nil {
			break
		}
	}
	_ = server.Close()
	_ = client.Close()
	<-readerDone
}
func (r *neo4jDataRelay) Close() {
	r.mu.Lock()
	r.closed = true
	_ = r.listener.Close()
	for _, c := range r.pairs {
		_ = c.Close()
	}
	r.mu.Unlock()
	r.wg.Wait()
}

func (r *neo4jDataRelay) dropPairs() {
	r.mu.Lock()
	pairs := append([]net.Conn(nil), r.pairs...)
	r.mu.Unlock()
	for _, pair := range pairs {
		_ = pair.Close()
	}
}

func TestNeo4jNativePublicDataLostCanceledOriginalCloseAndExplicitReopen(t *testing.T) {
	f := neo4jDataFixtureTest(t)
	admin, err := neo4j.Open(context.Background(), f.Config, f.Secret)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := "NyaGridFault_" + f.Nonce
	neo4jDataRun(t, admin, "UNWIND range(0,1) AS i CREATE (n:$($name) {i:i,exact:$exact}) RETURN n", map[string]any{"name": name, "exact": int64(9223372036854775807)})
	defer neo4jDataRun(t, admin, "MATCH (n) WHERE $name IN labels(n) DETACH DELETE n", map[string]any{"name": name})
	const query = "MATCH (entity) WHERE $name IN labels(entity) AND elementId(entity) > $after RETURN entity ORDER BY elementId(entity) LIMIT $limit"
	for _, read := range []string{"refresh", "next"} {
		for _, fault := range []string{"lost", "cancel", "document_close"} {
			t.Run(read+"/"+fault, func(t *testing.T) {
				relay := newNeo4jDataRelay(t, f, query)
				original := f
				original.Config.URI = "bolt://" + relay.listener.Addr().String()
				original.Config.CAFile, original.Config.ClientCertFile, original.Config.ClientKeyFile = "", "", ""
				profile := neo4jDataProfile(t, original)
				manager := dbruntime.New()
				dbruntime.RegisterBuiltins(manager, nil)
				defer manager.Close()
				handle, err := manager.ConnectSession(context.Background(), profile, f.Secret)
				if err != nil {
					t.Fatal(err)
				}
				active := Session{ProfileID: profile.ID, Generation: handle.Generation(), Connected: true}
				s := New(Dependencies{Session: func() Session { return active }, RuntimeFor: manager.Data})
				defer s.CloseAll()
				object := manager.Namespace(profile.ID, "neo4j").Object("label", name)
				d := s.OpenObject(1, object, handle.Capabilities())
				d.limit = 1
				current := d
				s.deps.ReopenDocument = func(services ui.Services, original *Document) {
					if original != d || original.Object() != object || original.Generation() != active.Generation {
						t.Error("reopen lost original public identity")
						return
					}
					s.Close(original)
					current = s.OpenObject(1, object, handle.Capabilities())
					current.limit = 1
					s.Load(services, current, false)
				}
				s.Load(ui.Services{}, d, false)
				waitNeo4jData(t, s, d)
				if d.err != "" || d.page == nil || len(d.page.Rows) != 1 || !d.page.HasMore || d.page.NextCursor == nil {
					t.Fatal("initial native original page", d.err)
				}
				cached := d.page
				relay.armed.Store(true)
				if read == "next" {
					s.changeMySQLDataPage(ui.Services{}, d, 1)
				} else {
					s.Load(ui.Services{}, d, true)
				}
				if d.page != cached {
					t.Fatal("pending native page discarded successful cache")
				}
				select {
				case <-relay.observed:
				case <-time.After(15 * time.Second):
					t.Fatal("actual public graph page RUN not forwarded")
				}
				switch fault {
				case "lost":
					relay.dropPairs()
				case "cancel":
					s.cancelMySQLDataRead(d)
					if !d.loading || d.results == nil || s.CanReopen(d) {
						t.Fatal("public cancel settled before executor acknowledgement")
					}
				case "document_close":
					s.Close(d)
				}
				if fault != "document_close" {
					waitNeo4jData(t, s, d)
					if !d.readRetired || d.err == "" || d.page != cached || d.offset != cached.Offset || !s.CanReopen(d) {
						t.Fatal("public failed range lost cache or explicit recovery", d.err)
					}
					if fault == "cancel" && read == "refresh" {
						captureNeo4jData(t, s, d, "retired")
					}
					s.Load(ui.Services{}, d, true)
					s.Refresh(ui.Services{}, d)
					if d.loading || d.page != cached || relay.dispatches.Load() != 1 {
						t.Fatal("failed page replayed or refresh discarded cache")
					}
					relay.armed.Store(false)
					relay.hold.Store(false)
					s.Reopen(ui.Services{}, d)
					waitNeo4jData(t, s, current)
					if current == d || !d.closed || current.err != "" || current.Object() != object || current.Generation() != active.Generation || current.page.Rows[0][0].Text != cached.Rows[0][0].Text {
						t.Fatal("explicit graph reopen changed original identity", current.err)
					}
				}
				if relay.dispatches.Load() != 1 {
					t.Fatal("affected public native RUN replayed")
				}
				freshProfile := neo4jDataProfile(t, f)
				replacement, err := manager.ConnectSession(context.Background(), freshProfile, f.Secret)
				if err != nil {
					t.Fatal(err)
				}
				s.Close(d)
				s.Close(current)
				handle.Close()
				if !manager.IsConnected(profile.ID) || manager.Generation(profile.ID) != replacement.Generation() {
					t.Fatal("old native data cleanup closed replacement")
				}
			})
		}
	}
}

package sqlworkspace

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
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

type neo4jWorkbenchFixture struct {
	Config                 connections.Neo4jConfig
	Secret, Nonce, Version string
}

func neo4jWorkbenchFixtureTest(t *testing.T) neo4jWorkbenchFixture {
	t.Helper()
	if os.Getenv("NYADB_NEO4J_PUBLIC_IT") != "1" {
		t.Skip("requires owned Neo4j public fixture")
	}
	raw, e := os.ReadFile(os.Getenv("NYADB_NEO4J_CONFIG_FILE"))
	if e != nil {
		t.Fatal("cannot read private fixture")
	}
	var f neo4jWorkbenchFixture
	if json.Unmarshal(raw, &f) != nil || len(f.Nonce) != 32 || f.Version != "5.26.21" {
		t.Fatal("invalid fixture")
	}
	return f
}
func neo4jWorkbenchProfile(t *testing.T, f neo4jWorkbenchFixture) connections.Profile {
	t.Helper()
	u, _ := url.Parse(f.Config.URI)
	u.Path = "/" + f.Config.Database
	if f.Config.Auth == "basic" {
		u.User = url.User(f.Config.Username)
	}
	q := url.Values{"auth": {f.Config.Auth}}
	for k, v := range map[string]string{"ca_file": f.Config.CAFile, "client_cert_file": f.Config.ClientCertFile, "client_key_file": f.Config.ClientKeyFile} {
		if v != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	parsed, e := connections.ParseURI(u.String())
	if e != nil {
		t.Fatal("invalid profile")
	}
	p := parsed.Profile
	p.ID = 85
	p.Name = "Owned Neo4j workspace"
	return p
}
func neo4jNativeRun(t *testing.T, c *neo4j.Client, r contract.GraphQueryRequest) contract.GraphResult {
	t.Helper()
	p, e := c.Prepare(r)
	if e != nil {
		t.Fatal(e)
	}
	out, e := c.Execute(context.Background(), p)
	if e != nil || !out.Completed {
		t.Fatal("native observation failed", e)
	}
	return out
}

// The relay forwards native RUN/PULL to the owned server but drains and withholds
// every response after the exact fault query. A separate native client observes
// the committed marker before the original response is lost/canceled/closed.
// This proves server mutation; local dispatch alone is never treated as commit.
type neo4jRelay struct {
	listener   net.Listener
	fixture    neo4jWorkbenchFixture
	needle     []byte
	mu         sync.Mutex
	pairs      []net.Conn
	closed     bool
	wg         sync.WaitGroup
	hold       atomic.Bool
	dispatches atomic.Int32
	observed   chan struct{}
	responded  chan struct{}
}

func newNeo4jRelay(t *testing.T, f neo4jWorkbenchFixture, needle string) *neo4jRelay {
	t.Helper()
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	r := &neo4jRelay{listener: ln, fixture: f, needle: []byte(needle), observed: make(chan struct{}, 1), responded: make(chan struct{}, 1)}
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
func (r *neo4jRelay) pipe(client net.Conn) {
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
			if n > 0 {
				if r.hold.Load() {
					select {
					case r.responded <- struct{}{}:
					default:
					}
				} else if _, w := client.Write(buf[:n]); w != nil {
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
		if n > 0 {
			history = append(history, buf[:n]...)
			for {
				i := bytes.Index(history, r.needle)
				if i < 0 {
					break
				}
				r.dispatches.Add(1)
				r.hold.Store(true)
				select {
				case r.observed <- struct{}{}:
				default:
				}
				history = history[i+len(r.needle):]
			}
			if len(history) > len(r.needle) {
				history = history[len(history)-len(r.needle):]
			}
			if _, w := server.Write(buf[:n]); w != nil {
				break
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
func (r *neo4jRelay) Close() {
	r.mu.Lock()
	r.closed = true
	_ = r.listener.Close()
	for _, c := range r.pairs {
		_ = c.Close()
	}
	r.mu.Unlock()
	r.wg.Wait()
}

func TestNeo4jNativePublicCypherLostCanceledAndOriginalCloseNeverReplay(t *testing.T) {
	f := neo4jWorkbenchFixtureTest(t)
	admin, e := neo4j.Open(context.Background(), f.Config, f.Secret)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	for _, fault := range []string{"lost", "cancel", "close"} {
		t.Run(fault, func(t *testing.T) {
			marker := "nya_" + f.Nonce + "_public_" + fault
			query := "CREATE (n:NyaFoundation {marker:'" + marker + "',exact:$exact}) RETURN n"
			defer neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "MATCH (n:NyaFoundation {marker:$marker}) DETACH DELETE n", Parameters: map[string]any{"marker": marker}})
			relay := newNeo4jRelay(t, f, query)
			original := f
			original.Config.URI = "bolt://" + relay.listener.Addr().String()
			original.Config.CAFile = ""
			original.Config.ClientCertFile = ""
			original.Config.ClientKeyFile = ""
			p := neo4jWorkbenchProfile(t, original)
			m := dbruntime.New()
			dbruntime.RegisterBuiltins(m, nil)
			defer m.Close()
			if _, e = m.ConnectSession(context.Background(), p, f.Secret); e != nil {
				t.Fatal(e)
			}
			runtime := EngineRuntime(m.Session)
			s := New(Dependencies{QueryDialect: func(int64) string { return "neo4j" }, Namespace: m.Namespace, Runtime: func() Runtime { return runtime }, Connected: m.IsConnected})
			defer s.CloseAll()
			d := s.Open(1, query, p.ID, "neo4j", false)
			d.graphParameters = `{"exact":9223372036854775807}`
			wait := func() {
				t.Helper()
				end := time.Now().Add(15 * time.Second)
				for (d.Loading() || d.prepareResults != nil) && time.Now().Before(end) {
					s.Consume()
					time.Sleep(time.Millisecond)
				}
				if d.Loading() || d.prepareResults != nil {
					t.Fatal("original worker did not acknowledge")
				}
			}
			s.Prepare(d)
			wait()
			s.Run(nil, d)
			select {
			case <-relay.observed:
			case <-time.After(15 * time.Second):
				t.Fatal("original native query not dispatched")
			}
			// Observe a committed write independently before interrupting its lost reply.
			committed := false
			end := time.Now().Add(15 * time.Second)
			for time.Now().Before(end) {
				r := neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "MATCH (n:NyaFoundation {marker:$marker}) RETURN count(n) AS n", Parameters: map[string]any{"marker": marker}})
				if r.Rows[0][0].Text == "1" {
					committed = true
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !committed {
				t.Fatal("native server never committed original mutation")
			}
			switch fault {
			case "lost":
				relay.Close()
			case "cancel":
				s.RequestCancel(d)
				if !d.Loading() || !d.CancelRequested() {
					t.Fatal("cancel cleared before acknowledgement")
				}
			case "close":
				s.Close(d)
			}
			if fault != "close" {
				wait()
				if !d.NeedsRecovery() || d.Result() == nil || d.Result().Graph == nil || !d.Result().Graph.Uncertain || d.Result().Graph.Completed || d.Editor.Buffer.String() != query {
					t.Fatal("public uncertainty or source lost", d.Error())
				}
				s.Run(nil, d)
				if d.Loading() {
					t.Fatal("uncertain query replayed")
				}
			}
			freshProfile := neo4jWorkbenchProfile(t, f)
			freshSession, e := m.ConnectSession(context.Background(), freshProfile, f.Secret)
			if e != nil {
				t.Fatal(e)
			}
			if fault != "close" {
				s.Recover(d)
				wait()
				if d.NeedsRecovery() || d.Editor.Buffer.String() != query {
					t.Fatal("explicit original recovery failed", d.Error())
				}
				s.Close(d)
			}
			if !m.IsConnected(p.ID) || m.Generation(p.ID) != freshSession.Generation() {
				t.Fatal("old tab cleanup closed replacement")
			}
			r := neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "MATCH (n:NyaFoundation {marker:$marker}) RETURN count(n) AS n", Parameters: map[string]any{"marker": marker}})
			if r.Rows[0][0].Text != "1" || relay.dispatches.Load() != 1 {
				t.Fatal("public recovery replayed committed write")
			}
		})
	}
}

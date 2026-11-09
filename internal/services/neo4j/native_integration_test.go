package neo4j

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	nd "github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// This opt-in suite mutates only the uniquely owned official Docker fixture
// described by tools/verify_neo4j_fixture.py. Secrets remain in its private file.
type nativeFixture struct {
	Config                                    connections.Neo4jConfig
	Secret, Mode, Version, Nonce, WrongCAFile string
}

func fixtureTest(t *testing.T) nativeFixture {
	t.Helper()
	if os.Getenv("NYADB_NEO4J_IT") != "1" {
		t.Skip("requires an owned Neo4j fixture")
	}
	b, e := os.ReadFile(os.Getenv("NYADB_NEO4J_CONFIG_FILE"))
	if e != nil {
		t.Fatal("cannot read owned fixture configuration")
	}
	var f nativeFixture
	if json.Unmarshal(b, &f) != nil || len(f.Nonce) != 32 || f.Version != "5.26.21" || f.Config.Database != "neo4j" {
		t.Fatal("invalid owned fixture")
	}
	u, e := url.Parse(f.Config.URI)
	if e != nil || u.Hostname() != "localhost" {
		t.Fatal("fixture must use local published Bolt endpoint")
	}
	return f
}
func nativeOpenTest(t *testing.T, f nativeFixture) *Client {
	t.Helper()
	c, e := Open(context.Background(), f.Config, f.Secret)
	if e != nil {
		t.Fatalf("native open failed (%T)", e)
	}
	t.Cleanup(func() {
		if e := c.Close(); e != nil {
			t.Errorf("native cleanup failed (%T)", e)
		}
	})
	return c
}
func nativeRunTest(t *testing.T, c *Client, r Request) Response {
	t.Helper()
	result, e := c.Execute(context.Background(), prepareTest(t, c, r))
	if e != nil {
		t.Fatalf("native execution failed (%T): %s", e, result.String())
	}
	if !result.Attempted || !result.Completed || result.Uncertain {
		t.Fatalf("missing native completion: %s", result.String())
	}
	return result
}
func columnTest(t *testing.T, r Response, row int, key string) Value {
	t.Helper()
	for i, k := range r.Columns {
		if k == key && row < len(r.Rows) {
			return r.Rows[row][i]
		}
	}
	t.Fatalf("missing native field %s", key)
	return Value{}
}
func markerTest(f nativeFixture, suffix string) string { return "nya_" + f.Nonce + "_" + suffix }
func cleanupMarkerTest(t *testing.T, c *Client, marker string) {
	t.Helper()
	nativeRunTest(t, c, Request{Query: "MATCH (n:NyaFoundation {marker:$marker}) DETACH DELETE n", Parameters: map[string]any{"marker": marker}})
}

func TestNeo4jNativeAuthenticationIdentityAndTransport(t *testing.T) {
	f := fixtureTest(t)
	c := nativeOpenTest(t, f)
	if c.Identity().Agent != "Neo4j/"+f.Version || c.Identity().ProtocolMajor != 5 {
		t.Fatalf("wrong native identity: %#v", c.Identity())
	}
	r := nativeRunTest(t, c, Request{Query: "CALL dbms.components() YIELD name,versions,edition RETURN name,versions,edition"})
	if columnTest(t, r, 0, "versions").Values[0].Text != f.Version || columnTest(t, r, 0, "edition").Text != "community" {
		t.Fatal("fixture version or edition mismatch")
	}
	if f.Mode != "anonymous" {
		if wrong, e := Open(context.Background(), f.Config, "invalid-owned-fixture-password"); e == nil {
			_ = wrong.Close()
			t.Fatal("wrong authentication accepted")
		}
	}
	if f.Mode == "tls" || f.Mode == "mtls" {
		wrong := f.Config
		wrong.CAFile = f.WrongCAFile
		if client, e := Open(context.Background(), wrong, f.Secret); e == nil {
			_ = client.Close()
			t.Fatal("untrusted CA accepted")
		}
		wrong = f.Config
		wrong.URI = strings.Replace(wrong.URI, "localhost", "127.0.0.1", 1)
		if client, e := Open(context.Background(), wrong, f.Secret); e == nil {
			_ = client.Close()
			t.Fatal("wrong hostname accepted")
		}
		wrong = f.Config
		wrong.ServerName = "unrelated.example"
		if _, e := Open(context.Background(), wrong, f.Secret); !errors.Is(e, ErrProtocol) {
			t.Fatal("unsupported SNI override silently accepted")
		}
		selfSigned := f
		selfSigned.Config.URI = strings.Replace(f.Config.URI, "+s:", "+ssc:", 1)
		selfSigned.Config.CAFile = ""
		_ = nativeOpenTest(t, selfSigned)
		if f.Mode == "mtls" {
			wrong = f.Config
			wrong.ClientCertFile = ""
			wrong.ClientKeyFile = ""
			if client, e := Open(context.Background(), wrong, f.Secret); e == nil {
				_ = client.Close()
				t.Fatal("missing client certificate accepted")
			}
			wrong = f.Config
			wrong.ClientCertFile = f.WrongCAFile
			wrong.ClientKeyFile = strings.TrimSuffix(f.WrongCAFile, ".crt") + ".key"
			if client, e := Open(context.Background(), wrong, f.Secret); e == nil {
				_ = client.Close()
				t.Fatal("untrusted client certificate accepted")
			}
		}
	}
}

func TestNeo4jNativeExactParametersTemporalSpatialAndGraph(t *testing.T) {
	f := fixtureTest(t)
	c := nativeOpenTest(t, f)
	marker := markerTest(f, "typed")
	t.Cleanup(func() { cleanupMarkerTest(t, c, marker) })
	zone, e := time.LoadLocation("Europe/Stockholm")
	if e != nil {
		t.Fatal(e)
	}
	stamp := time.Date(2026, 7, 3, 1, 2, 3, 123456789, zone)
	params := map[string]any{
		"max": int64(math.MaxInt64), "min": int64(math.MinInt64), "nil": nil, "bool": true,
		"nan": math.NaN(), "positiveinf": math.Inf(1), "negativeinf": math.Inf(-1), "negativezero": math.Copysign(0, -1),
		"list": []int64{math.MaxInt64, math.MinInt64}, "map": map[string]any{"猫": "你好\t ", "nil": nil},
		"bytes": []byte{0, 255, 3}, "datetime": stamp, "date": nd.Date(stamp), "localdatetime": nd.LocalDateTime(stamp),
		"time": nd.Time(stamp), "localtime": nd.LocalTime(stamp), "duration": nd.Duration{Months: 2, Days: 3, Seconds: 4, Nanos: 123456789},
		"point2d": nd.Point2D{X: 1.5, Y: 2.5, SpatialRefId: 7203}, "point3d": nd.Point3D{X: 1.5, Y: 2.5, Z: 3.5, SpatialRefId: 9157},
		"geographic": nd.Point3D{X: 113.5, Y: 22.3, Z: 10.5, SpatialRefId: 4979},
	}
	fields := []string{"max", "min", "nil", "bool", "list", "map", "bytes", "datetime", "date", "localdatetime", "time", "localtime", "duration", "point2d", "point3d", "nan", "positiveinf", "negativeinf", "negativezero", "geographic"}
	parts := make([]string, len(fields))
	for i, k := range fields {
		parts[i] = "$" + k + " AS " + k
	}
	r := nativeRunTest(t, c, Request{Query: "RETURN " + strings.Join(parts, ","), Parameters: params})
	if columnTest(t, r, 0, "max").Text != "9223372036854775807" || columnTest(t, r, 0, "min").Text != "-9223372036854775808" || columnTest(t, r, 0, "bytes").Text != "AP8D" || columnTest(t, r, 0, "nil").Kind != "null" || columnTest(t, r, 0, "list").Values[1].Text != "-9223372036854775808" {
		t.Fatal("native scalar/list precision lost")
	}
	for key, kind := range map[string]string{"datetime": "datetime", "date": "date", "localdatetime": "local_datetime", "time": "time", "localtime": "local_time", "duration": "duration", "point2d": "point2d", "point3d": "point3d"} {
		if columnTest(t, r, 0, key).Kind != kind {
			t.Fatalf("lost native type %s", key)
		}
	}
	if propertyTest(t, columnTest(t, r, 0, "datetime").Properties, "zone").Text != "Europe/Stockholm" || propertyTest(t, columnTest(t, r, 0, "datetime").Properties, "offset_seconds").Text != "7200" || propertyTest(t, columnTest(t, r, 0, "duration").Properties, "nanoseconds").Text != "123456789" {
		t.Fatal("lost native temporal components")
	}
	for key, text := range map[string]string{"nan": "NaN", "positiveinf": "+Inf", "negativeinf": "-Inf", "negativezero": "-0"} {
		if columnTest(t, r, 0, key).Kind != "float" || columnTest(t, r, 0, key).Text != text {
			t.Fatalf("lost native special float %s", key)
		}
	}
	if propertyTest(t, columnTest(t, r, 0, "geographic").Properties, "srid").Text != "4979" || propertyTest(t, columnTest(t, r, 0, "map").Properties, "猫").Text != "你好\t " {
		t.Fatal("native geographic SRID or Unicode map value lost")
	}
	graph := nativeRunTest(t, c, Request{Query: "CREATE p=(a:NyaFoundation {marker:$marker, exact:$max})-[r:NYA_LINK {weight:1.5}]->(b:NyaFoundation {marker:$marker}) RETURN a,r,b,p", Parameters: map[string]any{"marker": marker, "max": int64(math.MaxInt64)}})
	a, rel, b, p := columnTest(t, graph, 0, "a"), columnTest(t, graph, 0, "r"), columnTest(t, graph, 0, "b"), columnTest(t, graph, 0, "p")
	if a.Kind != "node" || rel.Kind != "relationship" || p.Kind != "path" || a.Node.ElementID == "" || rel.Relationship.StartElementID != a.Node.ElementID || rel.Relationship.EndElementID != b.Node.ElementID || len(p.Path.Nodes) != 2 || p.Path.Nodes[0].ElementID != a.Node.ElementID || p.Path.Relationships[0].ElementID != rel.Relationship.ElementID || graph.Summary.Counters["nodes_created"] != 2 || graph.Summary.Counters["relationships_created"] != 1 || propertyTest(t, a.Node.Properties, "exact").Text != "9223372036854775807" {
		t.Fatal("native graph identity/traversal/statistics lost")
	}
	read := nativeRunTest(t, c, Request{Query: "MATCH p=(a:NyaFoundation {marker:$marker})-[r:NYA_LINK]->(b) RETURN p", Mode: "read", Parameters: map[string]any{"marker": marker}})
	if columnTest(t, read, 0, "p").Path.Nodes[0].ElementID != a.Node.ElementID {
		t.Fatal("graph identity changed on readback")
	}
}

func TestNeo4jNativeCompletionErrorsAndBoundedResults(t *testing.T) {
	f := fixtureTest(t)
	c := nativeOpenTest(t, f)
	marker := markerTest(f, "bounded")
	t.Cleanup(func() { cleanupMarkerTest(t, c, marker) })
	p := prepareTest(t, c, Request{Query: "UNWIND range(1,20) AS i CREATE (n:NyaFoundation {marker:$marker, i:i}) RETURN i", Parameters: map[string]any{"marker": marker}, MaxRows: 3, FetchSize: 2})
	r, e := c.Execute(context.Background(), p)
	if e != nil || !r.Truncated || !r.Completed || r.Uncertain || len(r.Rows) != 3 || r.Summary.Counters["nodes_created"] != 20 {
		t.Fatalf("bounded write lacks native acknowledgement: %s (%T)", r.String(), e)
	}
	if _, e := c.Execute(context.Background(), p); !errors.Is(e, ErrUsed) {
		t.Fatal("completed query reused")
	}
	count := nativeRunTest(t, c, Request{Query: "MATCH (n:NyaFoundation {marker:$marker}) RETURN count(n) AS n", Parameters: map[string]any{"marker": marker}})
	if columnTest(t, count, 0, "n").Text != "20" {
		t.Fatal("bounded write replayed or discarded completion did not commit")
	}
	p = prepareTest(t, c, Request{Query: "RETURN [i IN range(1,3000)|'long native scalar'] AS large", MaxBytes: 1024})
	r, e = c.Execute(context.Background(), p)
	if !errors.Is(e, ErrBounds) || !r.Completed || !r.Truncated || r.Uncertain || len(r.Rows) != 0 {
		t.Fatalf("byte bounds lost completion: %s (%T)", r.String(), e)
	}
	empty := nativeRunTest(t, c, Request{Query: "MATCH (n:NyaFoundation {marker:$marker}) WHERE false RETURN n", Parameters: map[string]any{"marker": marker}})
	if len(empty.Rows) != 0 || len(empty.Columns) != 1 {
		t.Fatal("empty native result lost columns")
	}
	p = prepareTest(t, c, Request{Query: "THIS IS INVALID CYPHER"})
	r, e = c.Execute(context.Background(), p)
	if e == nil || r.FailureCode != "Neo.ClientError.Statement.SyntaxError" || !r.Attempted || r.Completed || !r.Uncertain {
		t.Fatal("native error lost conservative original outcome")
	}
	if _, e := c.Execute(context.Background(), p); !errors.Is(e, ErrUsed) {
		t.Fatal("failed query replayed")
	}
}

func TestNeo4jNativeRoutingAndOriginalDatabase(t *testing.T) {
	f := fixtureTest(t)
	u, e := url.Parse(f.Config.URI)
	if e != nil {
		t.Fatal(e)
	}
	u.Scheme = strings.Replace(u.Scheme, "bolt", "neo4j", 1)
	u.RawQuery = "region=owned"
	f.Config.URI = u.String()
	c := nativeOpenTest(t, f)
	marker := markerTest(f, "routing")
	t.Cleanup(func() { cleanupMarkerTest(t, c, marker) })
	r := nativeRunTest(t, c, Request{Query: "CREATE (n:NyaFoundation {marker:$marker}) RETURN n", Parameters: map[string]any{"marker": marker}})
	if r.Summary.Database != "neo4j" || r.Summary.Server.Agent != "Neo4j/"+f.Version {
		t.Fatal("routing lost native database/server identity")
	}
	r = nativeRunTest(t, c, Request{Query: "SHOW DATABASES YIELD name RETURN name", Database: "system", Mode: "read"})
	if r.Summary.Database != "system" {
		t.Fatal("explicit original system database ignored")
	}
}

// The relay forwards native RUN/PULL to the owned server but drains and withholds
// every response after the exact fault query. A separate native client observes
// the committed marker before the original response is lost/canceled/closed.
// This proves server mutation; local dispatch alone is never treated as commit.
type nativeRelay struct {
	listener   net.Listener
	fixture    nativeFixture
	needle     []byte
	mu         sync.Mutex
	pairs      []net.Conn
	closed     bool
	wg         sync.WaitGroup
	hold       atomic.Bool
	dispatches atomic.Int32
	observed   chan struct{}
}

func newNativeRelay(t *testing.T, f nativeFixture, needle string) *nativeRelay {
	t.Helper()
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	r := &nativeRelay{listener: ln, fixture: f, needle: []byte(needle), observed: make(chan struct{}, 1)}
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
func (r *nativeRelay) pipe(client net.Conn) {
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
func (r *nativeRelay) Close() {
	r.mu.Lock()
	r.closed = true
	_ = r.listener.Close()
	for _, c := range r.pairs {
		_ = c.Close()
	}
	r.mu.Unlock()
	r.wg.Wait()
}

func TestNeo4jNativeLostCanceledAndOriginalCloseNeverReplay(t *testing.T) {
	f := fixtureTest(t)
	admin := nativeOpenTest(t, f)
	for _, action := range []string{"lost", "cancel", "close"} {
		t.Run(action, func(t *testing.T) {
			marker := markerTest(f, "fault_"+action)
			t.Cleanup(func() { cleanupMarkerTest(t, admin, marker) })
			needle := "NyaFault_" + marker
			relay := newNativeRelay(t, f, needle)
			originalFixture := f
			originalFixture.Config.URI = "bolt://" + relay.listener.Addr().String()
			originalFixture.Config.CAFile = ""
			originalFixture.Config.ClientCertFile = ""
			originalFixture.Config.ClientKeyFile = ""
			original := nativeOpenTest(t, originalFixture)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p := prepareTest(t, original, Request{Query: "/*" + needle + "*/ CREATE (n:NyaFoundation {marker:$marker})", Parameters: map[string]any{"marker": marker}})
			type completion struct {
				r Response
				e error
			}
			done := make(chan completion, 1)
			go func() { r, e := original.Execute(ctx, p); done <- completion{r, e} }()
			select {
			case <-relay.observed:
			case <-time.After(10 * time.Second):
				t.Fatal("native fault request not observed")
			}
			deadline := time.Now().Add(10 * time.Second)
			committed := false
			for time.Now().Before(deadline) {
				r := nativeRunTest(t, admin, Request{Query: "MATCH (n:NyaFoundation {marker:$marker}) RETURN count(n) AS n", Parameters: map[string]any{"marker": marker}})
				if columnTest(t, r, 0, "n").Text == "1" {
					committed = true
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !committed {
				cancel()
				relay.Close()
				<-done
				t.Fatal("native write not independently observed committed")
			}
			select {
			case <-done:
				t.Fatal("withheld server response incorrectly acknowledged")
			default:
			}
			replacement := nativeOpenTest(t, f)
			closed := make(chan struct{})
			switch action {
			case "lost":
				relay.Close()
			case "cancel":
				cancel()
			case "close":
				go func() { _ = original.Close(); close(closed) }()
			}
			var result completion
			select {
			case result = <-done:
			case <-time.After(15 * time.Second):
				relay.Close()
				t.Fatal("original operation did not settle after interruption")
			}
			if action == "close" {
				select {
				case <-closed:
				case <-time.After(10 * time.Second):
					t.Fatal("original close did not acknowledge")
				}
			}
			if !errors.Is(result.e, ErrRetired) || !result.r.Attempted || result.r.Completed || !result.r.Uncertain || original.Alive() || relay.dispatches.Load() != 1 {
				t.Fatalf("unsafe original interruption: %s (%T), dispatches=%d", result.r.String(), result.e, relay.dispatches.Load())
			}
			if action != "close" && !original.LastResponse().Uncertain {
				t.Fatal("original uncertain receipt discarded")
			}
			if _, e := original.Execute(context.Background(), p); !errors.Is(e, ErrRetired) {
				t.Fatal("retired original replay accepted")
			}
			_ = original.Close()
			read := nativeRunTest(t, replacement, Request{Query: "MATCH (n:NyaFoundation {marker:$marker}) RETURN count(n) AS n", Mode: "read", Parameters: map[string]any{"marker": marker}})
			if columnTest(t, read, 0, "n").Text != "1" || !replacement.Alive() {
				t.Fatal("native write replayed or original cleanup closed replacement")
			}
			t.Log(fmt.Sprintf("independently observed one committed write; %s response remained uncertain; no replay", action))
		})
	}
}

func TestNeo4jNativeBlockedCancellationTerminationAndCleanup(t *testing.T) {
	f := fixtureTest(t)
	admin := nativeOpenTest(t, f)
	for _, action := range []string{"cancel", "close", "terminate"} {
		t.Run(action, func(t *testing.T) {
			marker := markerTest(f, "blocked_"+action)
			t.Cleanup(func() { cleanupMarkerTest(t, admin, marker) })
			nativeRunTest(t, admin, Request{Query: "CREATE (n:NyaFoundation {marker:$marker,value:0})", Parameters: map[string]any{"marker": marker}})
			holder := nativeOpenTest(t, f)
			session := holder.driver.NewSession(context.Background(), nd.SessionConfig{DatabaseName: "neo4j"})
			t.Cleanup(func() { _ = session.Close(context.Background()) })
			tx, e := session.BeginTransaction(context.Background())
			if e != nil {
				t.Fatalf("cannot hold native lock (%T)", e)
			}
			t.Cleanup(func() { _ = tx.Close(context.Background()) })
			r, e := tx.Run(context.Background(), "MATCH (n:NyaFoundation {marker:$marker}) SET n.value=1", map[string]any{"marker": marker})
			if e != nil {
				t.Fatalf("cannot acquire native lock (%T)", e)
			}
			if _, e = r.Consume(context.Background()); e != nil {
				t.Fatalf("cannot observe held native lock (%T)", e)
			}
			original := nativeOpenTest(t, f)
			query := "/*NyaBlocked_" + marker + "*/ MATCH (n:NyaFoundation {marker:$marker}) SET n.value=2 RETURN n.value"
			p := prepareTest(t, original, Request{Query: query, Parameters: map[string]any{"marker": marker}, TimeoutSeconds: 4})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type completion struct {
				r Response
				e error
			}
			done := make(chan completion, 1)
			go func() { r, e := original.Execute(ctx, p); done <- completion{r, e} }()
			show := func() Response {
				return nativeRunTest(t, admin, Request{Query: "SHOW TRANSACTIONS YIELD currentQuery,transactionId,status WHERE currentQuery=$query RETURN transactionId,status", Database: "system", Mode: "read", Parameters: map[string]any{"query": query}})
			}
			deadline := time.Now().Add(3 * time.Second)
			transactionID := ""
			for time.Now().Before(deadline) {
				r := show()
				if len(r.Rows) > 0 && strings.Contains(columnTest(t, r, 0, "status").Text, "Blocked") {
					transactionID = columnTest(t, r, 0, "transactionId").Text
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if transactionID == "" {
				cancel()
				<-done
				t.Fatal("native original request not independently observed blocked")
			}
			closed := make(chan struct{})
			switch action {
			case "cancel":
				cancel()
			case "close":
				go func() { _ = original.Close(); close(closed) }()
			case "terminate":
				termination := nativeRunTest(t, admin, Request{Query: "TERMINATE TRANSACTIONS $id YIELD transactionId,message RETURN transactionId,message", Database: "system", Parameters: map[string]any{"id": transactionID}})
				if columnTest(t, termination, 0, "transactionId").Text != transactionID {
					t.Fatal("native termination targeted another request")
				}
			}
			var result completion
			select {
			case result = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("native blocked interruption did not acknowledge")
			}
			if action == "close" {
				select {
				case <-closed:
				case <-time.After(10 * time.Second):
					t.Fatal("native blocked close did not acknowledge")
				}
			}
			if !errors.Is(result.e, ErrRetired) || !result.r.Uncertain || result.r.Completed || !result.r.Attempted || original.Alive() {
				t.Fatalf("unsafe native blocked outcome: %s (%T), native code=%s", result.r.String(), result.e, result.r.FailureCode)
			}
			deadline = time.Now().Add(10 * time.Second)
			settled := false
			for time.Now().Before(deadline) {
				if len(show().Rows) == 0 {
					settled = true
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if !settled {
				t.Fatal("original native transaction remained active after bounded cleanup")
			}
			if e := tx.Rollback(context.Background()); e != nil {
				t.Fatalf("native lock rollback failed (%T)", e)
			}
			if _, e := original.Execute(context.Background(), p); !errors.Is(e, ErrRetired) {
				t.Fatal("interrupted native request replay accepted")
			}
			replacement := nativeOpenTest(t, f)
			value := nativeRunTest(t, replacement, Request{Query: "MATCH (n:NyaFoundation {marker:$marker}) RETURN n.value AS value", Parameters: map[string]any{"marker": marker}, Mode: "read"})
			if columnTest(t, value, 0, "value").Text != "0" {
				t.Fatal("blocked write applied after interruption or replayed")
			}
			t.Logf("native request observed blocked; %s settled; original transaction disappeared and value remained 0; native code=%s", action, result.r.FailureCode)
		})
	}
}

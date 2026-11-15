package rqlite

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

const nativeBinarySHA256 = "37b0be99b2e1d3ca2950c5d2d7432c444be8f3d7526331b2b85b205b8ee6933c"

// Every native fixture owns one exact official server process and one private
// database/auth directory. It never discovers or shuts down external servers.
func nativeFixture(t *testing.T, tlsMode string) connections.Profile {
	t.Helper()
	p, _ := nativeFixtureWithJoin(t, tlsMode, "")
	return p
}

func nativeFixtureWithJoin(t *testing.T, tlsMode, join string) (connections.Profile, string) {
	t.Helper()
	if os.Getenv("NYADB_RQLITE_IT") != "1" {
		t.Skip("set NYADB_RQLITE_IT=1 and NYADB_RQLITE_BINARY to the pinned official v10.7.1 server")
	}
	binary := os.Getenv("NYADB_RQLITE_BINARY")
	raw, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal("official RQLite fixture binary unavailable")
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != nativeBinarySHA256 {
		t.Fatal("native RQLite fixture must use the exact verified official server")
	}
	dir := t.TempDir()
	auth, _ := json.Marshal([]any{map[string]any{"username": "fixture", "password": "original-fixture-password", "perms": []string{"all"}}, map[string]any{"username": "reader", "password": "reader-password", "perms": []string{"query"}}})
	authPath := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(authPath, auth, 0600); err != nil {
		t.Fatal(err)
	}
	port := func() string {
		l, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		addr := l.Addr().String()
		_ = l.Close()
		return addr
	}
	httpAddr, raftAddr := port(), port()
	args := []string{"-node-id", filepath.Base(dir), "-http-addr", httpAddr, "-raft-addr", raftAddr, "-auth", authPath}
	if join != "" {
		args = append(args, "-join", join, "-join-as", "fixture")
	}
	p := testProfile(t, "http://"+httpAddr)
	p.RQLiteOptions.Auth = "basic"
	p.Username = "fixture"
	if tlsMode != "" {
		certFile, keyFile := nativeCertificate(t, dir)
		args = append(args, "-http-cert", certFile, "-http-key", keyFile)
		if tlsMode == "mtls" {
			args = append(args, "-http-verify-client", "-http-ca-cert", certFile)
			p.RQLiteOptions.ClientCertFile, p.RQLiteOptions.ClientKeyFile = certFile, keyFile
		}
		p.TLSMode, p.RQLiteOptions.CAFile = "verify-full", certFile
	}
	args = append(args, filepath.Join(dir, "data"))
	log, err := os.OpenFile(filepath.Join(dir, "server.log"), os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, args...)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		t.Fatal("owned RQLite server failed to start")
	}
	t.Logf("owned native RQLite original process pid=%d join=%t", cmd.Process.Pid, join != "")
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("original owned RQLite process did not exit")
		}
		_ = log.Close()
		if output := os.Getenv("NYADB_RQLITE_EVIDENCE"); output != "" {
			if err := os.MkdirAll(output, 0700); err != nil {
				t.Error("native evidence directory unavailable")
				return
			}
			raw, err := os.ReadFile(filepath.Join(dir, "server.log"))
			if err == nil {
				_ = os.WriteFile(filepath.Join(output, fmt.Sprintf("server-%d.log", cmd.Process.Pid)), raw, 0600)
			}
		}
	})
	// Retry only the original constant handshake while the owned leader starts.
	deadline := time.Now().Add(20 * time.Second)
	for {
		select {
		case processError := <-done:
			done <- processError
			t.Fatalf("owned original RQLite server exited before readiness: %v", processError)
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		opened, e := OpenSession(ctx, p, "original-fixture-password")
		cancel()
		if e == nil {
			_ = opened.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("owned RQLite leader did not become ready: %v", e)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Logf("official native RQLite fixture pid=%d artifact=%s TLS=%s", cmd.Process.Pid, nativeBinarySHA256, tlsMode)
	return p, raftAddr
}

type originalEndpointTransport struct {
	base      http.RoundTripper
	host      string
	forwarded bool
	t         *testing.T
}

func (p *originalEndpointTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != p.host || r.URL.Query().Get("retries") != "0" || r.URL.Query().Has("redirect") {
		p.t.Error("cluster request left its original endpoint or enabled replay")
	}
	res, err := p.base.RoundTrip(r)
	if err == nil && res.Header.Get("X-RQLITE-SERVED-BY") != "" {
		p.forwarded = true
	}
	return res, err
}

func TestNativeRQLiteThreeNodeForwardingAndReadConsistency(t *testing.T) {
	p, join := nativeFixtureWithJoin(t, "", "")
	second, _ := nativeFixtureWithJoin(t, "", join)
	second.ID = 82
	second.RQLiteOptions.BatchTransaction = true
	third, _ := nativeFixtureWithJoin(t, "", join)
	third.ID = 83
	follower := openNative(t, second, "original-fixture-password")
	transport := &originalEndpointTransport{base: follower.transport, host: net.JoinHostPort(second.Host, fmt.Sprint(second.Port)), t: t}
	follower.client.Transport = transport
	l := lease(t, follower, 1)
	nativeExecute(t, l, "CREATE TABLE cluster_rows(id INTEGER PRIMARY KEY,note TEXT);INSERT INTO cluster_rows VALUES(9007199254740993,'one native cluster write')")
	if !transport.forwarded {
		t.Fatal("native follower did not forward its original write to the cluster leader")
	}
	for _, settings := range []struct {
		profile connections.Profile
		level   string
	}{{p, "strong"}, {third, "weak"}, {second, "linearizable"}, {third, "none"}} {
		q := settings.profile
		q.RQLiteOptions.Consistency = settings.level
		if settings.level == "none" {
			q.RQLiteOptions.FreshnessMilliseconds, q.RQLiteOptions.FreshnessStrict = 10000, true
		}
		s := openNative(t, q, "original-fixture-password")
		ql := lease(t, s, 1)
		deadline := time.Now().Add(10 * time.Second)
		for {
			r := nativeExecute(t, ql, "SELECT id,note FROM cluster_rows")
			if len(r.Sets[0].Rows) == 1 {
				if r.Sets[0].Rows[0][0].Text != "9007199254740993" || r.Sets[0].Rows[0][1].Text != "one native cluster write" {
					t.Fatal("native cluster value changed")
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("native cluster read did not observe its replicated original row")
			}
			time.Sleep(20 * time.Millisecond)
		}
		ns, _ := s.Namespaces(context.Background())
		objects, err := s.Browse(context.Background(), ns[0].Ref)
		if err != nil || len(objects) != 1 || objects[0].Ref.ProfileID != q.ID {
			t.Fatal("native cluster metadata lost original profile identity")
		}
	}
}

func nativeCertificate(t *testing.T, dir string) (string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "NyaDB owned RQLite fixture"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath := filepath.Join(dir, "root.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}
func openNative(t *testing.T, p connections.Profile, password string) *Session {
	t.Helper()
	opened, err := OpenSession(context.Background(), p, password)
	if err != nil {
		t.Fatal(err)
	}
	s := opened.(*Session)
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func nativeExecute(t *testing.T, l contract.QueryLease, sql string) contract.SQLResult {
	t.Helper()
	r, err := l.Execute(context.Background(), "main", sql)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestNativeRQLiteSQLMetadataAtomicRollbackAndQueryOnlyAuth(t *testing.T) {
	p := nativeFixture(t, "")
	p.RQLiteOptions.BatchTransaction = true
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 1)
	nativeExecute(t, l, `CREATE TABLE probe(id INTEGER PRIMARY KEY,note TEXT,raw BLOB);INSERT INTO probe VALUES(9007199254740993,'original',X'00ff5c27');CREATE TABLE composite(a TEXT NOT NULL,b BLOB NOT NULL,v,PRIMARY KEY(b,a));CREATE TABLE nullable(a TEXT UNIQUE,v);CREATE TABLE expression(a TEXT NOT NULL,v);CREATE UNIQUE INDEX expr ON expression(lower(a));CREATE UNIQUE INDEX partial ON expression(a) WHERE v IS NOT NULL;CREATE VIEW v AS SELECT id,note FROM probe;CREATE TRIGGER probe_update AFTER UPDATE ON probe BEGIN SELECT CASE WHEN new.id<0 THEN RAISE(ABORT,'private') END; END;CREATE TABLE generated(a INTEGER NOT NULL,b INTEGER GENERATED ALWAYS AS (a+1) STORED NOT NULL UNIQUE,value TEXT DEFAULT '');CREATE TABLE linked(id INTEGER PRIMARY KEY,parent INTEGER,FOREIGN KEY(parent) REFERENCES probe(id) ON UPDATE CASCADE ON DELETE SET NULL)`)
	r := nativeExecute(t, l, `WITH input(x) AS (VALUES(2)) INSERT INTO probe(id,note) SELECT x,'WITH write' FROM input;SELECT id,note,raw FROM probe ORDER BY id DESC`)
	if len(r.Sets) != 1 || len(r.Sets[0].Rows) != 2 || r.Sets[0].Rows[0][0].Text != "9007199254740993" || string(r.Sets[0].Rows[0][2].Bytes) != "\x00\xff\\'" || r.AffectedRows != 1 || !strings.Contains(r.NativeJSON, "last_insert_id") {
		t.Fatal("native mixed SQL/exact integer/BLOB receipt changed")
	}
	r, err := l.Execute(context.Background(), "main", `INSERT INTO probe VALUES(3,'must roll back',NULL);INSERT INTO probe VALUES(2,'conflict',NULL);SELECT * FROM probe`)
	if err == nil || r.AffectedRows != 0 || r.Steps[0].State != "rolled_back" || !strings.Contains(r.NativeJSON, `"rows_affected":1`) {
		t.Fatal("native transaction rollback receipt changed")
	}
	count := nativeExecute(t, l, "SELECT count(*) FROM probe")
	if count.Sets[0].Rows[0][0].Text != "2" {
		t.Fatal("failed native transaction partially committed")
	}
	ns, err := s.Namespaces(context.Background())
	if err != nil || len(ns) != 1 || ns[0].Ref.Catalog != "main" || ns[0].Ref.Schema != "" {
		t.Fatal("native main identity changed")
	}
	objects, err := s.Browse(context.Background(), ns[0].Ref)
	if err != nil || len(objects) != 7 {
		t.Fatalf("native browse failed: objects=%d err=%v", len(objects), err)
	}
	for _, item := range []struct {
		name, kind string
		key        []string
	}{{"probe", "table", []string{"id"}}, {"composite", "table", []string{"b", "a"}}, {"nullable", "table", nil}, {"expression", "table", nil}, {"v", "view", nil}, {"generated", "table", nil}, {"linked", "table", []string{"id"}}} {
		snapshot, err := s.table(context.Background(), ns[0].Ref.Object(item.kind, item.name))
		if err != nil {
			t.Fatalf("table %s: %v", item.name, err)
		}
		if strings.Join(snapshot.key, "|") != strings.Join(item.key, "|") || snapshot.structure.Definition == "" {
			t.Fatalf("native %s key/definition changed", item.name)
		}
		if item.name == "probe" && (snapshot.structure.Columns[0].Nullable || len(snapshot.structure.Definitions) != 1) {
			t.Fatal("rowid primary or trigger definition lost")
		}
		if item.name == "generated" && (!snapshot.structure.Columns[1].Generated || snapshot.structure.Columns[2].Default == nil || *snapshot.structure.Columns[2].Default != "''") {
			t.Fatal("native generated/default metadata lost")
		}
		if item.name == "linked" {
			fk := snapshot.structure.ForeignKeys
			if len(fk) != 1 || fk[0].ReferencedDatabase != "main" || fk[0].ReferencedTable != "probe" || fk[0].OnUpdate != "CASCADE" || fk[0].OnDelete != "SET NULL" || len(fk[0].Columns) != 1 || fk[0].Columns[0].Name != "parent" || fk[0].Columns[0].ReferencedName != "id" {
				t.Fatal("native qualified foreign-key metadata lost")
			}
		}
	}
	foreign := ns[0].Ref
	foreign.ProfileID++
	if _, err := s.Browse(context.Background(), foreign); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("foreign profile admitted")
	}
	readerProfile := p
	readerProfile.Username = "reader"
	readerProfile.RQLiteOptions.ReadOnly = true
	reader := openNative(t, readerProfile, "reader-password")
	rl := lease(t, reader, 1)
	if _, err := reader.Browse(context.Background(), ns[0].Ref); err != nil {
		t.Fatal("query-only credentials unexpectedly require status/execute permission")
	}
	if _, err := rl.Execute(context.Background(), "main", "INSERT INTO probe VALUES(4,'forbidden',NULL)"); err == nil {
		t.Fatal("query-only native API admitted a write")
	}
	if _, err := OpenSession(context.Background(), p, "wrong-password"); err == nil || strings.Contains(err.Error(), "wrong-password") {
		t.Fatal("authentication rejection missing or credential leaked")
	}
}

func TestNativeRQLiteTLSAndMutualTLS(t *testing.T) {
	for _, mode := range []string{"tls", "mtls"} {
		t.Run(mode, func(t *testing.T) {
			p := nativeFixture(t, mode)
			s := openNative(t, p, "original-fixture-password")
			nativeExecute(t, lease(t, s, 1), "SELECT 42")
			untrusted := p
			untrusted.RQLiteOptions.CAFile = ""
			if _, err := OpenSession(context.Background(), untrusted, "original-fixture-password"); err == nil {
				t.Fatal("untrusted native server certificate accepted")
			}
			wrongHost := p
			wrongHost.RQLiteOptions.ServerName = "wrong.invalid"
			if _, err := OpenSession(context.Background(), wrongHost, "original-fixture-password"); err == nil {
				t.Fatal("native TLS hostname mismatch accepted")
			}
			if mode == "mtls" {
				noClient := p
				noClient.RQLiteOptions.ClientCertFile, noClient.RQLiteOptions.ClientKeyFile = "", ""
				if _, err := OpenSession(context.Background(), noClient, "original-fixture-password"); err == nil {
					t.Fatal("native mTLS admitted no client identity")
				}
			}
		})
	}
}

func TestNativeRQLiteDatabaseTimeoutReturnsOriginalAcknowledgement(t *testing.T) {
	p := nativeFixture(t, "")
	p.RQLiteOptions.DatabaseTimeoutMilliseconds = 1
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 1)
	r, err := l.Execute(context.Background(), "main", "WITH RECURSIVE x(v) AS (SELECT 1 UNION ALL SELECT v+1 FROM x WHERE v<1000000000) SELECT sum(v) FROM x")
	if !errors.Is(err, context.DeadlineExceeded) || len(r.Steps) != 1 || r.Steps[0].State != "failed" || !strings.Contains(r.NativeJSON, "query timeout") || l.CheckQueryLease() != nil {
		t.Fatalf("native timeout receipt: %v", err)
	}
	nativeExecute(t, l, "SELECT 42")
}

func TestNativeRQLiteCancelledWriteKeepsItsOriginalReceipt(t *testing.T) {
	p := nativeFixture(t, "")
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 1)
	nativeExecute(t, l, "CREATE TABLE original(id INTEGER PRIMARY KEY,note TEXT)")
	// The proxy dispatches to the original native executor and holds its full
	// response. This proves receipt ownership against an actual applied write.
	started, acknowledge := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-acknowledge:
		default:
			close(acknowledge)
		}
	}()
	proxy := http.Server{}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	config, _ := connections.RQLiteProfileConfig(p)
	proxy.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forward, err := http.NewRequest(http.MethodPost, config.Endpoint+r.URL.RequestURI(), r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		forward.Header = r.Header.Clone()
		res, err := http.DefaultClient.Do(forward)
		if err != nil {
			t.Error(err)
			w.WriteHeader(502)
			return
		}
		defer res.Body.Close()
		var body bytes.Buffer
		_, err = body.ReadFrom(res.Body)
		if err != nil {
			t.Error(err)
			return
		}
		if r.URL.Path == "/db/request" {
			close(started)
			<-acknowledge
		}
		for k, v := range res.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(res.StatusCode)
		_, _ = w.Write(body.Bytes())
	})
	go func() { _ = proxy.Serve(listener) }()
	t.Cleanup(func() { _ = proxy.Close() })
	pProxy := testProfile(t, "http://"+listener.Addr().String())
	pProxy.Username, pProxy.RQLiteOptions.Auth = p.Username, "basic"
	proxied := openNative(t, pProxy, "original-fixture-password")
	ql := lease(t, proxied, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		r, err := ql.Execute(ctx, "main", "INSERT INTO original VALUES(1,'one original write')")
		if err == nil && r.AffectedRows != 1 {
			err = fmt.Errorf("original native write receipt lost")
		}
		done <- err
	}()
	<-started
	cancel()
	select {
	case <-done:
		t.Fatal("cancel finished before held native receipt")
	case <-time.After(30 * time.Millisecond):
	}
	close(acknowledge)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	r := nativeExecute(t, l, "SELECT count(*) FROM original")
	if r.Sets[0].Rows[0][0].Text != "1" {
		t.Fatal("original native write was lost or replayed")
	}
}

func TestNativeRQLiteLostAppliedWriteRequiresRecoveryWithoutReplay(t *testing.T) {
	p := nativeFixture(t, "")
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 1)
	nativeExecute(t, l, "CREATE TABLE lost_receipt(id INTEGER PRIMARY KEY,note TEXT)")
	config, _ := connections.RQLiteProfileConfig(p)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forward, err := http.NewRequest(http.MethodPost, config.Endpoint+r.URL.RequestURI(), r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		forward.Header = r.Header.Clone()
		res, err := http.DefaultClient.Do(forward)
		if err != nil {
			t.Error(err)
			w.WriteHeader(502)
			return
		}
		defer res.Body.Close()
		var body bytes.Buffer
		if _, err := body.ReadFrom(res.Body); err != nil {
			t.Error(err)
			return
		}
		if r.URL.Path == "/db/request" {
			// The original server has applied the write; deliberately lose only
			// its client receipt. No later read may resolve this original receipt.
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = connection.Close()
			return
		}
		for k, v := range res.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(res.StatusCode)
		_, _ = w.Write(body.Bytes())
	}))
	t.Cleanup(proxy.Close)
	profile := testProfile(t, proxy.URL)
	profile.ID, profile.Username, profile.RQLiteOptions.Auth = 84, "fixture", "basic"
	proxied := openNative(t, profile, "original-fixture-password")
	ql := lease(t, proxied, 1)
	r, err := ql.Execute(context.Background(), "main", "INSERT INTO lost_receipt VALUES(1,'applied once')")
	if !errors.Is(err, contract.ErrSQLSessionLost) || len(r.Steps) != 1 || r.Steps[0].State != "uncertain" || r.AffectedRows != 0 {
		t.Fatal("lost applied native write was declared failed or committed")
	}
	if _, err := ql.Execute(context.Background(), "main", "INSERT INTO lost_receipt VALUES(1,'applied once')"); !errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatal("uncertain original native SQL was replayed")
	}
	replacement, err := ql.Replace()
	if err != nil {
		t.Fatal(err)
	}
	if err := replacement.Preconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	actual := nativeExecute(t, l, "SELECT count(*) FROM lost_receipt")
	if actual.Sets[0].Rows[0][0].Text != "1" || r.Steps[0].State != "uncertain" {
		t.Fatal("recovery replayed SQL or overwrote its original uncertain receipt")
	}
}

package manticore

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/credentials"
	"github.com/nyakang/nyadb/internal/transport"
	"golang.org/x/crypto/ssh"
)

type countedNativeSSH struct {
	SessionTransport
	dials, closes atomic.Int32
}

type nativeSSHGate struct {
	SessionTransport
	address, target string
}

func (g *nativeSSHGate) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if network != "tcp" || addr != g.target {
		return nil, connections.ErrInvalid
	}
	return (&net.Dialer{}).DialContext(ctx, network, g.address)
}

func gatedNativeSSHConfig(t *testing.T) (connections.ManticoreConfig, string, *countedNativeSSH, *packetGate, SessionTransport) {
	t.Helper()
	cfg, password, owned := nativeSSHTransport(t)
	target := net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port)))
	gate := newPacketGateWithDial(t, target, owned.DialContext)
	return cfg, password, owned, gate, &nativeSSHGate{SessionTransport: owned, address: gate.listener.Addr().String(), target: target}
}

func gatedNativeSSH(t *testing.T) (*Session, *countedNativeSSH, *packetGate) {
	t.Helper()
	cfg, password, owned, gate, routed := gatedNativeSSHConfig(t)
	s, err := OpenWithTransport(context.Background(), 69, cfg, password, routed)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, owned, gate
}

func closeNativeSSHTransport(t *testing.T, owned *countedNativeSSH) {
	t.Helper()
	err := owned.SessionTransport.Close()
	// The SSH peer and the session cancellation watcher can finish a channel
	// before the scope's physical-close loop reaches it. Only already-closed
	// channel errors are acceptable; terminal admission is still mandatory.
	var closedError func(error) bool
	closedError = func(e error) bool {
		if joined, ok := e.(interface{ Unwrap() []error }); ok {
			for _, child := range joined.Unwrap() {
				if !closedError(child) {
					return false
				}
			}
			return true
		}
		return e == nil || errors.Is(e, io.EOF) || errors.Is(e, net.ErrClosed)
	}
	if !closedError(err) {
		t.Fatal(err)
	}
	select {
	case <-owned.Done():
	default:
		t.Fatal("explicit transport close did not revoke original owner")
	}
	if err != nil {
		t.Logf("original transport terminal after close: %v", err)
	}
}

func stopNativeSSHOperation(t *testing.T, mode string, s *Session, owned *countedNativeSSH, cancel context.CancelFunc) <-chan struct{} {
	t.Helper()
	closed := make(chan struct{})
	switch mode {
	case "cancel":
		cancel()
		close(closed)
	case "transport-close":
		closeNativeSSHTransport(t, owned)
		close(closed)
	case "session-close":
		go func() { s.Close(); close(closed) }()
	default:
		t.Fatal("invalid stop mode")
	}
	return closed
}

func waitNativeSSHClose(t *testing.T, closed <-chan struct{}) {
	t.Helper()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("original close did not settle")
	}
}

func TestNativeSSHTransportOriginalSQLLossCancellationAndNoReplay(t *testing.T) {
	for _, mode := range []string{"transport-close", "cancel", "session-close"} {
		t.Run(mode, func(t *testing.T) {
			s, owned, gate := gatedNativeSSH(t)
			q := nativeLease(t, s, 8420)
			table := fmt.Sprintf("nyadb_ssh_sql84_%d", time.Now().UnixNano())
			runNative(t, q, "CREATE TABLE "+table+" (qty integer)")
			runNative(t, q, "INSERT INTO "+table+" (id,qty) VALUES (1,7)")
			cfg, password := sshNativeDatabaseConfig(t)
			control := authOpen(t, cfg, password)
			reader := nativeLease(t, control, 8421)
			t.Cleanup(func() { runNative(t, reader, "DROP TABLE IF EXISTS "+table) })
			source := "UPDATE " + table + " SET qty=8 WHERE id=1"
			observed := gate.arm(source, "hold")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type result struct {
				value contract.SQLResult
				err   error
			}
			done := make(chan result, 1)
			go func() { r, e := q.Execute(ctx, "Manticore", source); done <- result{r, e} }()
			observedPacket(t, gate, observed)
			closed := stopNativeSSHOperation(t, mode, s, owned, cancel)
			var out result
			select {
			case out = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("original SQL executor did not settle")
			}
			waitNativeSSHClose(t, closed)
			var original nativeSQLReceipt
			if err := json.Unmarshal([]byte(out.value.NativeJSON), &original); err != nil {
				t.Fatal(err)
			}
			if out.err == nil || len(original.Statements) != 1 || original.Statements[0].State != "uncertain" || !original.Statements[0].Dispatched || original.Statements[0].Acknowledged {
				t.Fatal("lost ACK was synthesized", out.value, out.err)
			}
			if _, err := q.Execute(context.Background(), "Manticore", source); err == nil {
				t.Fatal("original SQL lease replayed")
			}
			if gate.calls.Load() != 1 {
				t.Fatal("original SQL dispatched more than once")
			}
			actual := runNative(t, reader, "SELECT id,qty FROM "+table)
			if fmt.Sprint(actual.Rows) != "[[1 8]]" {
				t.Fatal("independent original commit absent", actual)
			}
			fresh, _ := openNativeSSH(t)
			recovered := nativeLease(t, fresh, 8422)
			if err := recovered.Preconnect(context.Background()); err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(runNative(t, recovered, "SELECT id,qty FROM "+table).Rows) != "[[1 8]]" {
				t.Fatal("recovery replayed SQL")
			}
			objectEvidence(t, "ssh-sql-"+mode+".json", map[string]any{"original": out.value, "actual": actual, "calls": gate.calls.Load(), "error": out.err.Error()})
		})
	}
}

func TestNativeSSHTransportOriginalDataLossCancellationAndNoReplay(t *testing.T) {
	for _, mode := range []string{"transport-close", "cancel", "session-close"} {
		t.Run(mode, func(t *testing.T) {
			s, owned, gate := gatedNativeSSH(t)
			q := nativeLease(t, s, 8430)
			table := fmt.Sprintf("nyadb_ssh_data84_%d", time.Now().UnixNano())
			runNative(t, q, "CREATE TABLE "+table+" (qty integer)")
			runNative(t, q, "INSERT INTO "+table+" (id,qty) VALUES (1,7)")
			cfg, password := sshNativeDatabaseConfig(t)
			control := authOpen(t, cfg, password)
			reader := nativeLease(t, control, 8431)
			t.Cleanup(func() { runNative(t, reader, "DROP TABLE IF EXISTS "+table) })
			_, runtime := nativeData(t, s, table)
			page, err := runtime.Loader(context.Background(), 69, "Manticore", table, 0, 10)
			if err != nil || len(page.RowSnapshots) != 1 {
				t.Fatal(page, err)
			}
			plan := previewNative(t, runtime, table, contract.DataEdit{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{"qty": {Kind: contract.Integer, Text: "8"}}})
			preview := plan.Snapshot()
			if len(preview.Items) != 1 {
				t.Fatal(preview)
			}
			observed := gate.arm(plan.(*dataWritePlan).statements[0].SQL, "hold")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type result struct {
				value *contract.DataEditResult
				err   error
			}
			done := make(chan result, 1)
			go func() { r, e := runtime.SubmitEdits(ctx, 69, "Manticore", table, plan); done <- result{r, e} }()
			select {
			case packet := <-observed:
				t.Logf("original held packet=%x", packet)
			case early := <-done:
				objectEvidence(t, "ssh-data-before-packet.json", map[string]any{"error": fmt.Sprint(early.err), "result": early.value, "alive": s.Alive(), "dials": owned.dials.Load(), "calls": gate.calls.Load()})
				t.Fatal("write completed before original acknowledgement reached gate", early.err)
			case <-time.After(10 * time.Second):
				t.Fatal("original data acknowledgement not observed")
			}
			closed := stopNativeSSHOperation(t, mode, s, owned, cancel)
			var out result
			select {
			case out = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("original data executor did not settle")
			}
			waitNativeSSHClose(t, closed)
			if out.err == nil || out.value == nil || out.value.Sequential == nil || len(out.value.Sequential.Statements) != 1 || out.value.Sequential.Statements[0].State != contract.DataWriteUnknown || !out.value.Incomplete {
				t.Fatal("lost data ACK was synthesized", out.value, out.err)
			}
			if _, err = runtime.SubmitEdits(context.Background(), 69, "Manticore", table, plan); err == nil {
				t.Fatal("original data plan replayed")
			}
			actual := runNative(t, reader, "SELECT id,qty FROM "+table)
			if gate.calls.Load() != 1 || fmt.Sprint(actual.Rows) != "[[1 8]]" {
				t.Fatal("write replayed or independent commit absent", actual)
			}
			fresh, _ := openNativeSSH(t)
			recovered := nativeLease(t, fresh, 8432)
			if fmt.Sprint(runNative(t, recovered, "SELECT id,qty FROM "+table).Rows) != "[[1 8]]" {
				t.Fatal("explicit recovery changed original commit")
			}
			objectEvidence(t, "ssh-data-"+mode+".json", map[string]any{"original": out.value, "actual": actual, "calls": gate.calls.Load(), "error": out.err.Error()})
		})
	}
}

func TestNativeSSHTransportOriginalSchemaLossCancellationAndRetainedReceipt(t *testing.T) {
	for _, mode := range []string{"transport-close", "cancel", "session-close"} {
		t.Run(mode, func(t *testing.T) {
			s, owned, gate := gatedNativeSSH(t)
			q := nativeLease(t, s, 8440)
			table := fmt.Sprintf("nyadb_ssh_schema84_%d", time.Now().UnixNano())
			runNative(t, q, "CREATE TABLE "+table+" (qty integer)")
			cfg, password := sshNativeDatabaseConfig(t)
			control := authOpen(t, cfg, password)
			reader := nativeLease(t, control, 8441)
			t.Cleanup(func() { runNative(t, reader, "DROP TABLE IF EXISTS "+table) })
			d, target := nativeSchema(t, s, table, "ssh-unknown")
			request := schemaAddRequest(schemaOriginal(t, d, target), "added", "integer")
			plan, err := d.Operations().Plan(request)
			if err != nil || len(plan.SQL) != 1 {
				t.Fatal(plan, err)
			}
			observed := gate.arm(plan.SQL[0], "hold")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan contract.SchemaDesignOutcome, 1)
			go func() { done <- d.Operations().Apply(ctx, request) }()
			observedPacket(t, gate, observed)
			closed := stopNativeSSHOperation(t, mode, s, owned, cancel)
			var out contract.SchemaDesignOutcome
			select {
			case out = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("original schema executor did not settle")
			}
			waitNativeSSHClose(t, closed)
			if out.Error == nil || out.State != "uncertain" || out.Verified || out.Actual != nil || out.Receipt == nil {
				t.Fatal("lost schema ACK was synthesized", out)
			}
			var native nativeWriteReceipt
			if err = json.Unmarshal([]byte(out.Receipt.NativeJSON), &native); err != nil || !native.Dispatched || native.Acknowledged {
				t.Fatal(native, err)
			}
			if mode != "session-close" && !reflect.DeepEqual(&out, d.OriginalOutcome()) {
				t.Fatal("transport loss discarded original schema receipt")
			}
			ops := d.Operations()
			if ops.Apply != nil {
				again := ops.Apply(context.Background(), request)
				if again.Error == nil || again.Receipt != nil {
					t.Fatal("unknown schema proof replayed", again)
				}
			}
			actual, err := control.TableMetadata(context.Background(), control.Namespace("Manticore").Object("TABLE", table))
			if err != nil || len(actual.Columns) != 3 || actual.Columns[2].Name != "added" {
				t.Fatal(actual, err)
			}
			fresh, _ := openNativeSSH(t)
			newDoc, newTarget := nativeSchema(t, fresh, table, "ssh-recovered")
			readback := schemaOriginal(t, newDoc, newTarget)
			if len(readback.Schema.Table.Clauses) != 3 || gate.calls.Load() != 1 {
				t.Fatal("schema recovery replayed mutation")
			}
			if mode != "session-close" && !reflect.DeepEqual(&out, d.OriginalOutcome()) {
				t.Fatal("recovery overwrote original schema receipt")
			}
			objectEvidence(t, "ssh-schema-"+mode+".json", map[string]any{"original": out.Receipt, "actual": actual, "recovered": readback, "calls": gate.calls.Load(), "receipt_retained": mode != "session-close"})
		})
	}
}

func TestNativeSSHTransportCanceledAndLostOpenNeverPublishesOrClosesSibling(t *testing.T) {
	for _, mode := range []string{"cancel", "transport-close"} {
		t.Run(mode, func(t *testing.T) {
			sibling, _ := openNativeSSH(t)
			q := nativeLease(t, sibling, 8450)
			cfg, password, owned, gate, routed := gatedNativeSSHConfig(t)
			observed := gate.arm("SHOW DATABASES", "hold")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type result struct {
				s   *Session
				err error
			}
			done := make(chan result, 1)
			go func() { s, e := OpenWithTransport(ctx, 69, cfg, password, routed); done <- result{s, e} }()
			observedPacket(t, gate, observed)
			if mode == "cancel" {
				cancel()
			} else {
				closeNativeSSHTransport(t, owned)
			}
			var out result
			select {
			case out = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("original open did not settle")
			}
			if out.s != nil || out.err == nil || owned.closes.Load() != 1 || gate.calls.Load() != 1 {
				t.Fatal("revoked original open published or retried", out.err)
			}
			if !sibling.Alive() || fmt.Sprint(runNative(t, q, "SELECT 1").Rows) != "[[1]]" {
				t.Fatal("late original open closed sibling")
			}
			objectEvidence(t, "ssh-open-"+mode+".json", map[string]any{"published": false, "original_closes": owned.closes.Load(), "original_calls": gate.calls.Load(), "sibling_alive": sibling.Alive()})
		})
	}
}

func (c *countedNativeSSH) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	c.dials.Add(1)
	return c.SessionTransport.DialContext(ctx, network, addr)
}
func (c *countedNativeSSH) Close() error {
	c.closes.Add(1)
	return c.SessionTransport.Close()
}

// SSH authentication is independent of native database authentication. Older
// anonymous fixtures still require a real SSH handshake and original wire work.
func sshNativeDatabaseConfig(t *testing.T) (connections.ManticoreConfig, string) {
	t.Helper()
	if os.Getenv("NYADB_MANTICORE_AUTH_IT") == "1" {
		return authNativeConfig(t)
	}
	return nativeConfig(t), ""
}

func nativeSSHTransport(t *testing.T) (connections.ManticoreConfig, string, *countedNativeSSH) {
	return nativeSSHTransportOptions(t, nil)
}

func nativeSSHTransportOptions(t *testing.T, configure func(*connections.TransportOptions, *transport.Options)) (connections.ManticoreConfig, string, *countedNativeSSH) {
	t.Helper()
	if os.Getenv("NYADB_MANTICORE_SSH_IT") != "1" {
		t.Skip("requires owned native database and isolated OpenSSH fixture")
	}
	cfg, password := sshNativeDatabaseConfig(t)
	host, port, err := net.SplitHostPort(os.Getenv("NYADB_SSH_ENDPOINT"))
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	opts := connections.TransportOptions{SSH: connections.SSHOptions{Enabled: true, Host: host, Port: n, Username: "nyadb_fixture", AuthMethod: os.Getenv("NYADB_SSH_AUTH_METHOD"), PrivateKey: os.Getenv("NYADB_SSH_CLIENT_KEY")}}
	secrets := transport.Options{
		Secrets: transport.Secrets{SSHPassword: os.Getenv("NYADB_SSH_PASSWORD"), SSHPassphrase: os.Getenv("NYADB_SSH_PASSPHRASE")},
		Hosts:   &credentials.MemoryHostStore{},
		ConfirmHost: func(_ context.Context, h transport.HostIdentity) (bool, error) {
			return h.Fingerprint == os.Getenv("NYADB_SSH_FINGERPRINT"), nil
		},
	}
	if configure != nil {
		configure(&opts, &secrets)
	}
	dialer, err := transport.NewWithConfig(opts, secrets)
	if err != nil {
		t.Fatal(err)
	}
	owned := &countedNativeSSH{SessionTransport: dialer.(SessionTransport)}
	t.Cleanup(func() { dialer.Close() })
	cfg.Host, port, err = net.SplitHostPort(os.Getenv("NYADB_SSH_DB_TARGET"))
	if err != nil {
		t.Fatal(err)
	}
	n, err = strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Port = uint16(n)
	return cfg, password, owned
}

func TestNativeSSHTransportRejectsHostIdentityAndAuthenticationWithoutAffectingSibling(t *testing.T) {
	sibling, _ := openNativeSSH(t)
	q := nativeLease(t, sibling, 8460)
	for _, mode := range []string{"unknown-host", "rejected-host", "changed-host", "wrong-password", "wrong-key"} {
		t.Run(mode, func(t *testing.T) {
			var hosts *credentials.MemoryHostStore
			confirmations := 0
			cfg, password, owned := nativeSSHTransportOptions(t, func(opts *connections.TransportOptions, security *transport.Options) {
				hosts = security.Hosts.(*credentials.MemoryHostStore)
				security.ConfirmHost = func(_ context.Context, h transport.HostIdentity) (bool, error) {
					confirmations++
					return mode != "rejected-host" && h.Fingerprint == os.Getenv("NYADB_SSH_FINGERPRINT"), nil
				}
				switch mode {
				case "unknown-host":
					security.ConfirmHost = nil
				case "changed-host":
					if err := hosts.Remember(os.Getenv("NYADB_SSH_ENDPOINT"), "SHA256:original-different-host"); err != nil {
						t.Fatal(err)
					}
				case "wrong-password":
					opts.SSH.AuthMethod = connections.MySQLSSHPassword
					security.Secrets.SSHPassword = "wrong_ssh_fixture_password"
				case "wrong-key":
					_, key, err := ed25519.GenerateKey(rand.Reader)
					if err != nil {
						t.Fatal(err)
					}
					block, err := ssh.MarshalPrivateKey(key, "untrusted fixture key")
					if err != nil {
						t.Fatal(err)
					}
					path := filepath.Join(t.TempDir(), "untrusted")
					if err = os.WriteFile(path, pem.EncodeToMemory(block), 0600); err != nil {
						t.Fatal(err)
					}
					opts.SSH.AuthMethod, opts.SSH.PrivateKey = connections.MySQLSSHPublicKey, path
					security.Secrets.SSHPassphrase = ""
				}
			})
			s, err := OpenWithTransport(context.Background(), 69, cfg, password, owned)
			if s != nil || err == nil || owned.closes.Load() != 1 || owned.dials.Load() != 1 {
				t.Fatal("untrusted original channel admitted", err)
			}
			for _, secret := range []string{password, os.Getenv("NYADB_SSH_PASSWORD"), os.Getenv("NYADB_SSH_PASSPHRASE")} {
				if secret != "" && strings.Contains(err.Error(), secret) {
					t.Fatal("transport rejection exposed secret")
				}
			}
			known, found, lookupErr := hosts.Lookup(os.Getenv("NYADB_SSH_ENDPOINT"))
			if lookupErr != nil {
				t.Fatal(lookupErr)
			}
			switch mode {
			case "unknown-host", "rejected-host":
				if found {
					t.Fatal("unconfirmed host was remembered")
				}
			case "changed-host":
				if !found || known != "SHA256:original-different-host" || confirmations != 0 {
					t.Fatal("changed host replaced original identity")
				}
			}
			if !sibling.Alive() || fmt.Sprint(runNative(t, q, "SELECT 1").Rows) != "[[1]]" {
				t.Fatal("rejected original channel affected sibling")
			}
			objectEvidence(t, "ssh-rejected-"+mode+".json", map[string]any{"published": false, "original_dials": owned.dials.Load(), "original_closes": owned.closes.Load(), "confirmations": confirmations, "host_remembered": found, "error": err.Error()})
		})
	}
}

func TestNativeSSHTransportNormalMetadataClosePreservesOriginalTunnel(t *testing.T) {
	s, owned := openNativeSSH(t)
	q := nativeLease(t, s, 8470)
	runNative(t, q, "SELECT 1")
	for i := 0; i < 200; i++ {
		namespaces, err := s.Namespaces(context.Background())
		if err != nil || len(namespaces) != 1 || !s.Alive() {
			objectEvidence(t, "ssh-normal-close-failure.json", map[string]any{"iteration": i, "dials": owned.dials.Load(), "alive": s.Alive(), "error": fmt.Sprint(err)})
			t.Fatal("normal metadata close revoked original tunnel", i, err)
		}
	}
	if fmt.Sprint(runNative(t, q, "SELECT 1").Rows) != "[[1]]" {
		t.Fatal("normal metadata close lost original SQL owner")
	}
	objectEvidence(t, "ssh-normal-close.json", map[string]any{"iterations": 200, "dials": owned.dials.Load(), "alive": s.Alive()})
}

type delayedNativeQuit struct {
	net.Conn
	terminal <-chan struct{}
}

func (c *delayedNativeQuit) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if err == nil && n == len(p) && len(p) == 5 && p[0] == 1 && p[4] == 1 {
		// Let the real server finish COM_QUIT before the driver can close its
		// socket. This models preemption after dispatch, without fake replies.
		select {
		case <-c.terminal:
		case <-time.After(200 * time.Millisecond):
		}
	}
	return n, err
}

type delayedNativeQuitTransport struct{ SessionTransport }

func (c *delayedNativeQuitTransport) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	conn, err := c.SessionTransport.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	return &delayedNativeQuit{Conn: conn, terminal: c.Done()}, nil
}

func TestNativeSSHTransportScheduledGracefulCloseCannotRevokeSession(t *testing.T) {
	cfg, password, owned := nativeSSHTransport(t)
	s, err := OpenWithTransport(context.Background(), 69, cfg, password, &delayedNativeQuitTransport{SessionTransport: owned})
	if err != nil || s == nil {
		objectEvidence(t, "ssh-scheduled-close-failure.json", map[string]any{"published": false, "error": fmt.Sprint(err), "dials": owned.dials.Load()})
		t.Fatal("normal native close was mistaken for tunnel failure", err)
	}
	t.Cleanup(func() { s.Close() })
	if !s.Alive() {
		t.Fatal("normal close revoked original session")
	}
	q := nativeLease(t, s, 8471)
	for i := 0; i < 5; i++ {
		if _, err = s.Namespaces(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if fmt.Sprint(runNative(t, q, "SELECT 1").Rows) != "[[1]]" {
		t.Fatal("scheduled normal close lost original SQL channel")
	}
	objectEvidence(t, "ssh-scheduled-close.json", map[string]any{"published": true, "alive": s.Alive(), "dials": owned.dials.Load()})
}

func openNativeSSH(t *testing.T) (*Session, *countedNativeSSH) {
	t.Helper()
	cfg, password, owned := nativeSSHTransport(t)
	s, err := OpenWithTransport(context.Background(), 69, cfg, password, owned)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if !strings.HasPrefix(s.Version(), os.Getenv("NYADB_MANTICORE_VERSION")+" ") {
		t.Fatal("wrong fixture version")
	}
	return s, owned
}

func TestNativeSSHTransportOriginalFeaturesReceiptsAndOwnerClose(t *testing.T) {
	s, owned := openNativeSSH(t)
	q := nativeLease(t, s, 8401)
	table := fmt.Sprintf("nyadb_ssh84_%d", time.Now().UnixNano())
	runNative(t, q, "CREATE TABLE "+table+" (title text, qty integer)")
	runNative(t, q, "INSERT INTO "+table+" (id,title,qty) VALUES (1,'original',7)")
	cfg, password := sshNativeDatabaseConfig(t)
	control := authOpen(t, cfg, password)
	reader := nativeLease(t, control, 8402)
	t.Cleanup(func() { runNative(t, reader, "DROP TABLE IF EXISTS "+table) })
	objects, err := s.Browse(context.Background(), s.Namespace("Manticore"))
	if err != nil {
		t.Fatal(err)
	}
	_, runtime := nativeData(t, s, table)
	page, err := runtime.Loader(context.Background(), 69, "Manticore", table, 0, 10)
	if err != nil || len(page.RowSnapshots) != 1 {
		t.Fatal(page, err)
	}
	plan := previewNative(t, runtime, table, contract.DataEdit{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{"qty": {Kind: contract.Integer, Text: "8"}}})
	write := saveNative(t, runtime, table, plan, 1)
	d, target := nativeSchema(t, s, table, "ssh-original")
	original := schemaOriginal(t, d, target)
	out := d.Operations().Apply(context.Background(), schemaAddRequest(original, "added", "integer"))
	if out.Error != nil || out.State != "committed" || !out.Verified || out.Rows != 0 {
		t.Fatal(out)
	}
	retained := d.OriginalOutcome()
	// Real SSH and all channels terminate together. Receipt ownership remains
	// with the original document until its explicit close.
	closeNativeSSHTransport(t, owned)
	if s.Alive() {
		t.Fatal("terminal idle transport remains alive")
	}
	dials := owned.dials.Load()
	if _, err = s.ReserveQuery(8403); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal(err)
	}
	if _, err = s.ReserveDataDocument(s.Namespace("Manticore").Object("TABLE", table)); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal(err)
	}
	if _, err = s.ReserveSchemaDesignDocument(contract.SchemaDesignDocumentRequest{DocumentID: "revoked", Target: target}); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal(err)
	}
	if _, err = q.Execute(context.Background(), "Manticore", "UPDATE "+table+" SET qty=99 WHERE id=1"); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal(err)
	}
	if _, err = q.Replace(); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal(err)
	}
	if dials != owned.dials.Load() || !reflect.DeepEqual(retained, d.OriginalOutcome()) {
		t.Fatal("loss redialed or discarded original outcome")
	}
	fresh, _ := openNativeSSH(t)
	recovered := nativeLease(t, fresh, 8404)
	actual := runNative(t, recovered, "SELECT id,qty FROM "+table)
	if fmt.Sprint(actual.Rows) != "[[1 8]]" {
		t.Fatal("fresh owner replayed unsent write", actual)
	}
	if !reflect.DeepEqual(retained, d.OriginalOutcome()) {
		t.Fatal("fresh owner changed original receipt")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if owned.closes.Load() != 1 || d.OriginalOutcome() != nil || s.profileSecret() != "" {
		t.Fatal("explicit original close incomplete")
	}
	objectEvidence(t, "ssh-original-features.json", map[string]any{"objects": objects, "data": write, "schema": retained, "actual": actual, "original_dials": dials, "final_dials": owned.dials.Load(), "original_closes": owned.closes.Load()})
}

func TestNativeSSHTransportFailedAttemptsCloseOnlyOriginalOwner(t *testing.T) {
	s, _ := openNativeSSH(t)
	q := nativeLease(t, s, 8410)
	for _, mode := range []string{"invalid", "canceled", "credential", "revoked"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "credential" && os.Getenv("NYADB_MANTICORE_AUTH_IT") != "1" {
				t.Skip("anonymous native fixture does not enforce database credentials")
			}
			cfg, password, owned := nativeSSHTransport(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			id := int64(69)
			switch mode {
			case "invalid":
				id = 0
			case "canceled":
				cancel()
			case "credential":
				password = "wrong_fixture_password"
			case "revoked":
				closeNativeSSHTransport(t, owned)
			}
			failed, err := OpenWithTransport(ctx, id, cfg, password, owned)
			if failed != nil || err == nil || owned.closes.Load() != 1 {
				t.Fatal("failed attempt retained or replaced owner", err)
			}
			if mode != "credential" && owned.dials.Load() != 0 {
				t.Fatal("invalid/canceled/revoked attempt dialed")
			}
			if mode == "credential" && !strings.Contains(err.Error(), "[1045]") {
				t.Fatal("original authentication error lost", err)
			}
			if !s.Alive() || fmt.Sprint(runNative(t, q, "SELECT 1").Rows) != "[[1]]" {
				t.Fatal("failed attempt revoked sibling")
			}
			objectEvidence(t, "ssh-attempt-"+mode+".json", map[string]any{"published": false, "dials": owned.dials.Load(), "closes": owned.closes.Load(), "error": err.Error()})
		})
	}
}

func TestNativeSSHTransportAnonymousDatabaseCredentialsDoNotEstablishAuthentication(t *testing.T) {
	if os.Getenv("NYADB_MANTICORE_AUTH_IT") == "1" {
		t.Skip("requires anonymous native fixture with independent SSH authentication")
	}
	cfg, _, owned := nativeSSHTransport(t)
	cfg.Username = "unprovisioned_native_fixture_user"
	s, err := OpenWithTransport(context.Background(), 69, cfg, "unprovisioned_native_fixture_password", owned)
	if err != nil || s == nil {
		t.Fatal("anonymous native fixture did not ignore credentials", err)
	}
	t.Cleanup(func() { s.Close() })
	q := nativeLease(t, s, 8504)
	actual := runNative(t, q, "SELECT 1")
	if fmt.Sprint(actual.Rows) != "[[1]]" || !s.Alive() {
		t.Fatal(actual)
	}
	objectEvidence(t, "ssh-anonymous-credentials.json", map[string]any{"credentials_ignored": true, "native_authentication_enforced": false, "ssh_authenticated": true, "actual": actual, "version": os.Getenv("NYADB_MANTICORE_VERSION")})
}

func TestNativeSSHTransportExternalServerLossAndExplicitRecovery(t *testing.T) {
	if os.Getenv("NYADB_MANTICORE_SSH_SERVER_CONTROL_IT") != "1" {
		t.Skip("requires supervisor owning the disposable SSH server")
	}
	cfg, password := sshNativeDatabaseConfig(t)
	control := authOpen(t, cfg, password)
	reader := nativeLease(t, control, 8500)
	table := fmt.Sprintf("nyadb_ssh_loss85_%d", time.Now().UnixNano())
	runNative(t, reader, "CREATE TABLE "+table+" (qty integer)")
	runNative(t, reader, "INSERT INTO "+table+" (id,qty) VALUES (1,7),(2,7)")
	t.Cleanup(func() { runNative(t, reader, "DROP TABLE IF EXISTS "+table) })

	sqlSession, sqlOwner, sqlGate := gatedNativeSSH(t)
	dataSession, dataOwner, dataGate := gatedNativeSSH(t)
	schemaSession, schemaOwner, schemaGate := gatedNativeSSH(t)
	idleSession, idleOwner := openNativeSSH(t)
	query := nativeLease(t, sqlSession, 8501)
	idleQuery := nativeLease(t, idleSession, 8502)
	runNative(t, idleQuery, "SELECT 1")
	_, data := nativeData(t, dataSession, table)
	page, err := data.Loader(context.Background(), 69, "Manticore", table, 0, 10)
	if err != nil || len(page.RowSnapshots) != 2 {
		t.Fatal(page, err)
	}
	// Each original write has a distinct row/schema target. The real server
	// acknowledgement is held at its gate, after native mutation dispatch.
	plan := previewNative(t, data, table, contract.DataEdit{Kind: "update", Original: page.RowSnapshots[1], Values: map[string]contract.DataValue{"qty": {Kind: contract.Integer, Text: "9"}}})
	doc, target := nativeSchema(t, schemaSession, table, "ssh-external-loss")
	request := schemaAddRequest(schemaOriginal(t, doc, target), "added", "integer")
	schemaPlan, err := doc.Operations().Plan(request)
	if err != nil || len(schemaPlan.SQL) != 1 {
		t.Fatal(schemaPlan, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type sqlResult struct {
		value contract.SQLResult
		err   error
	}
	type dataResult struct {
		value *contract.DataEditResult
		err   error
	}
	sqlDone := make(chan sqlResult, 1)
	dataDone := make(chan dataResult, 1)
	schemaDone := make(chan contract.SchemaDesignOutcome, 1)
	sqlSource := "UPDATE " + table + " SET qty=8 WHERE id=1"
	observed := sqlGate.arm(sqlSource, "hold")
	go func() { r, e := query.Execute(ctx, "Manticore", sqlSource); sqlDone <- sqlResult{r, e} }()
	observedPacket(t, sqlGate, observed)
	observed = dataGate.arm(plan.(*dataWritePlan).statements[0].SQL, "hold")
	go func() { r, e := data.SubmitEdits(ctx, 69, "Manticore", table, plan); dataDone <- dataResult{r, e} }()
	observedPacket(t, dataGate, observed)
	observed = schemaGate.arm(schemaPlan.SQL[0], "hold")
	go func() { schemaDone <- doc.Operations().Apply(ctx, request) }()
	observedPacket(t, schemaGate, observed)
	owners := []*countedNativeSSH{sqlOwner, dataOwner, schemaOwner, idleOwner}
	sessions := []*Session{sqlSession, dataSession, schemaSession, idleSession}
	dials := make([]int32, len(owners))
	for i, owner := range owners {
		dials[i] = owner.dials.Load()
	}
	objectEvidence(t, "stop-ssh.json", map[string]any{"reason": "withheld three real native acknowledgements; terminate only supervisor-owned SSH container"})
	for i, owner := range owners {
		select {
		case <-owner.Done():
		case <-time.After(15 * time.Second):
			t.Fatal("external server loss did not terminate original transport", i)
		}
		if sessions[i].Alive() || owner.closes.Load() != 0 {
			t.Fatal("external loss must revoke without caller Close", i)
		}
	}
	var sqlOut sqlResult
	var dataOut dataResult
	var schemaOut contract.SchemaDesignOutcome
	select {
	case sqlOut = <-sqlDone:
	case <-time.After(10 * time.Second):
		t.Fatal("SQL loss did not settle")
	}
	select {
	case dataOut = <-dataDone:
	case <-time.After(10 * time.Second):
		t.Fatal("data loss did not settle")
	}
	select {
	case schemaOut = <-schemaDone:
	case <-time.After(10 * time.Second):
		t.Fatal("schema loss did not settle")
	}
	var sqlReceipt nativeSQLReceipt
	if err = json.Unmarshal([]byte(sqlOut.value.NativeJSON), &sqlReceipt); err != nil || sqlOut.err == nil || len(sqlReceipt.Statements) != 1 || sqlReceipt.Statements[0].State != "uncertain" || !sqlReceipt.Statements[0].Dispatched || sqlReceipt.Statements[0].Acknowledged {
		t.Fatal("external loss synthesized SQL receipt", sqlOut, err)
	}
	if dataOut.err == nil || dataOut.value == nil || !dataOut.value.CommitUncertain || !dataOut.value.Incomplete || dataOut.value.Applied != 0 || len(dataOut.value.Sequential.Statements) != 1 || dataOut.value.Sequential.Statements[0].State != "unknown" {
		t.Fatal("external loss synthesized data receipt", dataOut)
	}
	if schemaOut.Error == nil || schemaOut.State != "uncertain" || schemaOut.Verified || schemaOut.Actual != nil || schemaOut.Receipt == nil || !reflect.DeepEqual(&schemaOut, doc.OriginalOutcome()) {
		t.Fatal("external loss synthesized or discarded schema receipt", schemaOut)
	}
	if _, err = query.Execute(context.Background(), "Manticore", sqlSource); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal(err)
	}
	if _, err = idleQuery.Execute(context.Background(), "Manticore", "SELECT 1"); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal(err)
	}
	if again, err := data.SubmitEdits(context.Background(), 69, "Manticore", table, plan); err == nil {
		t.Fatal("external loss replayed data", again, err)
	}
	if again := doc.Operations().Apply(context.Background(), request); again.Error == nil || again.Receipt != nil {
		t.Fatal("external loss replayed schema", again)
	}
	actual := runNative(t, reader, "SELECT id,qty FROM "+table+" ORDER BY id ASC")
	if fmt.Sprint(actual.Rows) != "[[1 8] [2 9]]" {
		t.Fatal("actual native commits absent", actual)
	}
	objectEvidence(t, "start-ssh.json", map[string]any{"reason": "original receipts settled; explicit fresh transport only"})
	var ready struct {
		Endpoint    string `json:"endpoint"`
		Fingerprint string `json:"fingerprint"`
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		b, e := os.ReadFile(filepath.Join(os.Getenv("NYADB_MANTICORE_EVIDENCE"), "restart-ready.json"))
		if e == nil && json.Unmarshal(b, &ready) == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("owned SSH server did not restart")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if ready.Fingerprint != os.Getenv("NYADB_SSH_FINGERPRINT") || ready.Endpoint == "" {
		t.Fatal("restarted server changed original host identity")
	}
	t.Setenv("NYADB_SSH_ENDPOINT", ready.Endpoint)
	fresh, _ := openNativeSSH(t)
	freshQuery := nativeLease(t, fresh, 8503)
	recovered := runNative(t, freshQuery, "SELECT id,qty FROM "+table+" ORDER BY id ASC")
	newDoc, newTarget := nativeSchema(t, fresh, table, "ssh-external-recovered")
	snapshot := schemaOriginal(t, newDoc, newTarget)
	if !reflect.DeepEqual(actual.Rows, recovered.Rows) || len(snapshot.Schema.Table.Clauses) != 3 || snapshot.Schema.Table.Clauses[2].Name != "added" || !reflect.DeepEqual(&schemaOut, doc.OriginalOutcome()) {
		t.Fatal("explicit recovery replaced receipt or replayed write", recovered, snapshot)
	}
	for i, owner := range owners {
		if sessions[i].Alive() || owner.dials.Load() != dials[i] || owner.closes.Load() != 0 {
			t.Fatal("server restart resurrected original owner", i)
		}
	}
	for _, gate := range []*packetGate{sqlGate, dataGate, schemaGate} {
		if gate.calls.Load() != 1 {
			t.Fatal("original mutation replayed")
		}
	}
	objectEvidence(t, "ssh-external-server-loss.json", map[string]any{"sql": sqlOut.value, "data": dataOut.value, "schema": schemaOut.Receipt, "actual": actual, "recovered": recovered, "schema_recovered": snapshot, "original_dials": dials, "mutation_calls": []int64{sqlGate.calls.Load(), dataGate.calls.Load(), schemaGate.calls.Load()}, "receipt_retained": reflect.DeepEqual(&schemaOut, doc.OriginalOutcome()), "original_owner_closes": []int32{sqlOwner.closes.Load(), dataOwner.closes.Load(), schemaOwner.closes.Load(), idleOwner.closes.Load()}, "version": os.Getenv("NYADB_MANTICORE_VERSION")})
}

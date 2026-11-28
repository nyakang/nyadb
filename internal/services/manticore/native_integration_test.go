package manticore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func nativeConfig(t *testing.T) connections.ManticoreConfig {
	t.Helper()
	if os.Getenv("NYADB_MANTICORE_IT") != "1" {
		t.Skip("requires owned real Manticore fixture")
	}
	host, port, e := net.SplitHostPort(os.Getenv("NYADB_MANTICORE_ENDPOINT"))
	if e != nil {
		t.Fatal(e)
	}
	n, e := strconv.ParseUint(port, 10, 16)
	if e != nil {
		t.Fatal(e)
	}
	return connections.ManticoreConfig{Host: host, Port: uint16(n), Username: "root", QueryTimeoutSeconds: 20, SQLMaxConnections: 2}
}
func openNative(t *testing.T, c connections.ManticoreConfig) *Session {
	t.Helper()
	s, e := Open(context.Background(), 69, c, "")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := s.Close(); e != nil {
			t.Error(e)
		}
	})
	expected := os.Getenv("NYADB_MANTICORE_VERSION")
	if expected == "" || !strings.HasPrefix(s.Version(), expected+" ") {
		t.Fatalf("version %q expected %q", s.Version(), expected)
	}
	return s
}
func nativeLease(t *testing.T, s *Session, id uint64) contract.QueryLease {
	t.Helper()
	l, e := s.ReserveQuery(id)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(l.Release)
	return l
}
func runNative(t *testing.T, l contract.QueryLease, q string) contract.SQLResult {
	t.Helper()
	r, e := l.Execute(context.Background(), "Manticore", q)
	if e != nil {
		t.Fatalf("%s: %v (%+v)", q, e, r.Steps)
	}
	raw, _ := json.Marshal(map[string]any{"sql": q, "result": r})
	t.Log(string(raw))
	return r
}
func createNativeTable(t *testing.T, l contract.QueryLease) string {
	t.Helper()
	name := fmt.Sprintf("nyadb_native69_%d", time.Now().UnixNano())
	runNative(t, l, "CREATE TABLE "+name+" (title text, price float, qty integer, tag string, metadata json)")
	t.Cleanup(func() {
		_, e := l.Execute(context.Background(), "Manticore", "DROP TABLE "+name)
		if e != nil && !errors.Is(e, contract.ErrRevoked) {
			t.Error("original table cleanup:", e)
		}
	})
	return name
}
func TestNativeMetadataQueriesWritesAndExplicitLeases(t *testing.T) {
	s := openNative(t, nativeConfig(t))
	l := nativeLease(t, s, 1)
	table := createNativeTable(t, l)
	if caps := s.Capabilities(); !caps.Query || !caps.Metadata || caps.Transactions || caps.EditableTables || caps.Maintenance {
		t.Fatalf("capabilities %+v", caps)
	}
	ns, e := s.Namespaces(context.Background())
	if e != nil || len(ns) != 1 || ns[0].Ref.Catalog != "Manticore" {
		t.Fatal(ns, e)
	}
	objects, e := s.Browse(context.Background(), ns[0].Ref)
	if e != nil {
		t.Fatal(e)
	}
	var object contract.ObjectRef
	for _, o := range objects {
		if o.Name == table {
			object = o.Ref
			if o.Engine != "rt" {
				t.Fatal(o)
			}
		}
	}
	if object.Name == "" {
		t.Fatal("original table absent")
	}
	metadata, e := s.TableMetadata(context.Background(), object)
	if e != nil {
		t.Fatal(e)
	}
	if metadata.Editable || len(metadata.Columns) != 6 || metadata.Columns[0].Key != "" || metadata.Columns[0].Name != "id" || !strings.Contains(metadata.Columns[1].Extra, "indexed") || !strings.Contains(metadata.Definition, table) {
		t.Fatalf("metadata %+v", metadata)
	}
	raw, _ := json.Marshal(metadata)
	t.Log("original metadata", string(raw))
	wrong := object
	wrong.ProfileID++
	if _, e = s.TableMetadata(context.Background(), wrong); !errors.Is(e, connections.ErrInvalid) {
		t.Fatal("foreign object", e)
	}
	for _, sql := range []string{
		"INSERT INTO " + table + " (id,title,price,qty,tag,metadata) VALUES (9223372036854775806,'猫; original',1.25,7,'','{\"x\":1}')",
		"UPDATE " + table + " SET qty=8 WHERE id=9223372036854775806",
	} {
		r := runNative(t, l, sql)
		if r.AffectedRows != 1 || len(r.Steps) != 1 || r.Steps[0].State != "completed" {
			t.Fatal(r)
		}
	}
	r := runNative(t, l, "SELECT id,title,price,qty,tag,metadata FROM "+table+" WHERE id=9223372036854775806")
	if len(r.Sets) != 1 || len(r.Sets[0].Rows) != 1 {
		t.Fatal(r)
	}
	row := r.Sets[0].Rows[0]
	if row[0].Kind != contract.Integer || row[0].Text != "9223372036854775806" || row[1].Text != "猫; original" || row[2].Text != "1.25" || row[3].Text != "8" || row[4].Kind != contract.Text || row[4].Text != "" || row[5].Text != "{\"x\":1}" {
		t.Fatal(row)
	}
	// Sinks cannot mutate retained cells or columns.
	streamed := 0
	r, e = l.ExecuteStream(context.Background(), "Manticore", "SELECT id,title FROM "+table, func(_ int, cols []contract.Column, row []contract.Cell) error {
		streamed++
		cols[0].Name = "mutated"
		row[0].Text = "mutated"
		return nil
	})
	if e != nil || streamed != 1 || r.Sets[0].Columns[0].Name != "id" || r.Sets[0].Rows[0][0].Text == "mutated" {
		t.Fatal(r, e)
	}
	r, e = l.Execute(context.Background(), "Manticore", "SELECT impossible_column FROM "+table)
	if e == nil || len(r.Steps) != 1 || r.Steps[0].State != "failed" || errors.Is(e, contract.ErrSQLSessionLost) {
		t.Fatal(r, e)
	}
	runNative(t, l, "SELECT id FROM "+table)
	r, e = l.Execute(context.Background(), "Manticore", "INSERT INTO "+table+" (id) VALUES (17); SELECT 'unterminated")
	if !errors.Is(e, connections.ErrInvalid) || len(r.Steps) != 0 {
		t.Fatal(r, e)
	}
	r = runNative(t, l, "SELECT id FROM "+table+" WHERE id=17")
	if len(r.Sets[0].Rows) != 0 {
		t.Fatal("malformed script partially executed")
	}
	r = runNative(t, l, "DELETE FROM "+table+" WHERE id=9223372036854775806")
	if r.AffectedRows != 1 {
		t.Fatal(r)
	}
	// Physical capacity is independent of metadata reads and queued cancellation.
	second := nativeLease(t, s, 2)
	if e = second.Preconnect(context.Background()); e != nil {
		t.Fatal(e)
	}
	third := nativeLease(t, s, 3)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if e = third.Preconnect(ctx); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
	second.Release()
	if e = third.Preconnect(context.Background()); e != nil {
		t.Fatal(e)
	}
	third.Release()
	if _, e = third.Execute(context.Background(), "Manticore", "SELECT 1"); !errors.Is(e, contract.ErrRevoked) {
		t.Fatal(e)
	}
	// Old physical leases never become usable after the session is closed.
	runNative(t, l, "DROP TABLE "+table)
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	if _, e = l.Execute(context.Background(), "Manticore", "SELECT 1"); !errors.Is(e, contract.ErrRevoked) {
		t.Fatal(e)
	}
}

// packetGate forwards real MySQL frames. Only an original server response to
// the exact armed COM_QUERY is held/dropped; no server replies are fabricated.
type packetGate struct {
	listener net.Listener
	target   string
	dial     func(context.Context, string, string) (net.Conn, error)
	mu       sync.Mutex
	conns    map[net.Conn]bool
	query    string
	mode     string
	observed chan []byte
	resume   chan struct{}
	calls    atomic.Int64
	closed   chan struct{}
	wg       sync.WaitGroup
}

func newPacketGate(t *testing.T, target string) *packetGate {
	return newPacketGateWithDial(t, target, (&net.Dialer{Timeout: 3 * time.Second}).DialContext)
}

func newPacketGateWithDial(t *testing.T, target string, dial func(context.Context, string, string) (net.Conn, error)) *packetGate {
	t.Helper()
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	g := &packetGate{listener: ln, target: target, dial: dial, conns: map[net.Conn]bool{}, closed: make(chan struct{})}
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		for {
			front, e := ln.Accept()
			if e != nil {
				return
			}
			g.mu.Lock()
			select {
			case <-g.closed:
				g.mu.Unlock()
				front.Close()
				return
			default:
			}
			g.conns[front] = true
			g.wg.Add(1)
			g.mu.Unlock()
			go g.forward(front)
		}
	}()
	t.Cleanup(func() {
		close(g.closed)
		ln.Close()
		g.mu.Lock()
		for c := range g.conns {
			c.Close()
		}
		g.mu.Unlock()
		g.wg.Wait()
	})
	return g
}
func frame(c net.Conn) ([]byte, error) {
	h := make([]byte, 4)
	if _, e := io.ReadFull(c, h); e != nil {
		return nil, e
	}
	n := int(h[0]) | int(h[1])<<8 | int(h[2])<<16
	b := make([]byte, 4+n)
	copy(b, h)
	_, e := io.ReadFull(c, b[4:])
	return b, e
}
func (g *packetGate) forward(front net.Conn) {
	defer g.wg.Done()
	defer front.Close()
	defer func() { g.mu.Lock(); delete(g.conns, front); g.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	back, e := g.dial(ctx, "tcp", g.target)
	cancel()
	if e != nil {
		return
	}
	defer back.Close()
	g.mu.Lock()
	g.conns[back] = true
	g.mu.Unlock()
	defer func() { g.mu.Lock(); delete(g.conns, back); g.mu.Unlock() }()
	var armed atomic.Bool
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		defer back.Close()
		defer front.Close()
		for {
			b, e := frame(front)
			if e != nil {
				return
			}
			g.mu.Lock()
			match := len(b) > 5 && b[4] == 3 && string(b[5:]) == g.query && g.query != ""
			g.mu.Unlock()
			if match {
				g.calls.Add(1)
				armed.Store(true)
			}
			if _, e = back.Write(b); e != nil {
				return
			}
		}
	}()
	defer func() { front.Close(); back.Close(); <-finished }()
	for {
		b, e := frame(back)
		if e != nil {
			return
		}
		if armed.Swap(false) {
			g.mu.Lock()
			mode, observed, resume := g.mode, g.observed, g.resume
			g.mu.Unlock()
			observed <- append([]byte(nil), b...)
			if mode == "drop" {
				return
			}
			select {
			case <-resume:
			case <-g.closed:
				return
			case <-finished:
				return
			}
		}
		if _, e = front.Write(b); e != nil {
			return
		}
	}
}
func (g *packetGate) arm(query, mode string) <-chan []byte {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.query = query
	g.mode = mode
	g.observed = make(chan []byte, 4)
	g.resume = make(chan struct{})
	return g.observed
}
func TestNativeOriginalReceiptsCancellationLossAndNoReplay(t *testing.T) {
	cfg := nativeConfig(t)
	original := openNative(t, cfg)
	control := nativeLease(t, original, 101)
	table := createNativeTable(t, control)
	gate := newPacketGate(t, net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port))))
	host, port, _ := net.SplitHostPort(gate.listener.Addr().String())
	n, _ := strconv.Atoi(port)
	cfg.Host = host
	cfg.Port = uint16(n)
	s := openNative(t, cfg)
	for i, mode := range []string{"drop", "cancel", "release"} {
		t.Run(mode, func(t *testing.T) {
			l := nativeLease(t, s, uint64(i+1))
			if e := l.Preconnect(context.Background()); e != nil {
				t.Fatal(e)
			}
			q := fmt.Sprintf("INSERT INTO %s (id,qty) VALUES (%d,7)", table, i+1)
			observed := gate.arm(q, mode)
			before := gate.calls.Load()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type response struct {
				r contract.SQLResult
				e error
			}
			done := make(chan response, 1)
			go func() { r, e := l.Execute(ctx, "Manticore", q); done <- response{r, e} }()
			var ack []byte
			select {
			case ack = <-observed:
			case <-time.After(10 * time.Second):
				t.Fatal("no original response")
			}
			if len(ack) < 6 || ack[4] != 0 {
				t.Fatalf("expected original server OK, got %x", ack)
			}
			hash := sha256.Sum256(ack)
			t.Logf("original OK packet=%s sha256=%x", hex.EncodeToString(ack), hash)
			if mode == "cancel" {
				cancel()
			}
			if mode == "release" {
				l.Release()
			}
			select {
			case out := <-done:
				if out.e == nil || len(out.r.Steps) != 1 || out.r.Steps[0].State != "uncertain" || !errors.Is(out.e, contract.ErrSQLSessionLost) {
					t.Fatal(out.r, out.e)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("original executor did not acknowledge cancellation/loss")
			}
			if _, e := l.Execute(context.Background(), "Manticore", q); e == nil {
				t.Fatal("lost lease executed")
			}
			if gate.calls.Load() != before+1 {
				t.Fatal("request replayed")
			}
			// Explicit recovery reserves a new physical owner; it does not execute SQL.
			fresh, e := l.Replace()
			if e != nil {
				t.Fatal(e)
			}
			defer fresh.Release()
			if e = fresh.Preconnect(context.Background()); e != nil {
				t.Fatal(e)
			}
			if gate.calls.Load() != before+1 {
				t.Fatal("recovery replayed")
			}
			r := runNative(t, fresh, fmt.Sprintf("SELECT id,qty FROM %s WHERE id=%d", table, i+1))
			if len(r.Sets[0].Rows) != 1 || r.Sets[0].Rows[0][1].Text != "7" {
				t.Fatal("original committed write absent", r)
			}
		})
	}
	// A canceled context cannot dispatch any statement, even on a fresh owner.
	l := nativeLease(t, s, 99)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := l.Execute(ctx, "Manticore", "INSERT INTO "+table+" (id) VALUES (99)"); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if len(s.leases) != 1 {
		t.Fatalf("leases not released: %d", len(s.leases))
	}
}

func TestNativeCompleteStreamBoundedPreviewAndReadCancellation(t *testing.T) {
	cfg := nativeConfig(t)
	s := openNative(t, cfg)
	l := nativeLease(t, s, 1)
	table := createNativeTable(t, l)
	// More than the preview limit proves that streaming keeps complete original
	// rows while the retained editor preview remains bounded.
	const total = previewRows + 3
	for start := 1; start <= total; start += 500 {
		var values []string
		for id := start; id < start+500 && id <= total; id++ {
			values = append(values, fmt.Sprintf("(%d,'猫 row %d')", id, id))
		}
		r, e := l.Execute(context.Background(), "Manticore", "INSERT INTO "+table+" (id,title) VALUES "+strings.Join(values, ","))
		if e != nil || r.AffectedRows != int64(len(values)) {
			t.Fatal(r, e)
		}
	}
	count := 0
	q := fmt.Sprintf("SELECT id,title FROM %s ORDER BY id ASC LIMIT %d OPTION max_matches=%d", table, total, total)
	r, e := l.ExecuteStream(context.Background(), "Manticore", q, func(_ int, cols []contract.Column, row []contract.Cell) error {
		count++
		if row[0].Text != strconv.Itoa(count) || row[1].Text != fmt.Sprintf("猫 row %d", count) {
			return errors.New("original stream changed order or content")
		}
		return nil
	})
	if e != nil || count != total || len(r.Sets) != 1 || len(r.Sets[0].Rows) != previewRows || !r.Truncated {
		t.Fatalf("count=%d retained=%d truncated=%v err=%v", count, len(r.Rows), r.Truncated, e)
	}
	t.Logf("original stream rows=%d preview=%d truncated=%v", count, len(r.Sets[0].Rows), r.Truncated)
	gate := newPacketGate(t, net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port))))
	host, port, _ := net.SplitHostPort(gate.listener.Addr().String())
	n, _ := strconv.Atoi(port)
	cfg.Host = host
	cfg.Port = uint16(n)
	bound := openNative(t, cfg)
	reader := nativeLease(t, bound, 2)
	if e = reader.Preconnect(context.Background()); e != nil {
		t.Fatal(e)
	}
	observed := gate.arm(q, "hold")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, e := reader.Execute(ctx, "Manticore", q); done <- e }()
	select {
	case packet := <-observed:
		hash := sha256.Sum256(packet)
		t.Logf("original result header=%x sha256=%x", packet, hash)
	case <-time.After(10 * time.Second):
		t.Fatal("no original query result")
	}
	cancel()
	select {
	case e := <-done:
		if !errors.Is(e, contract.ErrSQLSessionLost) {
			t.Fatal(e)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("read cancellation not acknowledged")
	}
	if e = reader.Preconnect(context.Background()); !errors.Is(e, contract.ErrSQLSessionLost) {
		t.Fatal("implicit read recovery", e)
	}
	if gate.calls.Load() != 1 {
		t.Fatal("read replayed")
	}
}

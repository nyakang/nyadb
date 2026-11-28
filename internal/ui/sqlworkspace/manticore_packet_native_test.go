package sqlworkspace

import (
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Transparent gate preserves real original driver/server exchanges. It only
// withholds or drops a selected original response, never fabricating outcomes.
type sqlPacketGate struct {
	listener net.Listener
	target   string
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

func newSQLPacketGate(t *testing.T, target string) *sqlPacketGate {
	t.Helper()
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	g := &sqlPacketGate{listener: ln, target: target, conns: map[net.Conn]bool{}, closed: make(chan struct{})}
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
func sqlFrame(c net.Conn) ([]byte, error) {
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
func (g *sqlPacketGate) forward(front net.Conn) {
	defer g.wg.Done()
	defer front.Close()
	defer func() { g.mu.Lock(); delete(g.conns, front); g.mu.Unlock() }()
	back, e := net.DialTimeout("tcp", g.target, 3*time.Second)
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
			b, e := sqlFrame(front)
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
		b, e := sqlFrame(back)
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
func (g *sqlPacketGate) arm(query, mode string) <-chan []byte {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.query = query
	g.mode = mode
	g.observed = make(chan []byte, 4)
	g.resume = make(chan struct{})
	return g.observed
}

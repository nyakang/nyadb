package h2

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

var errTLSTrust = errors.New("H2 TLS certificate or original host verification failed")

// tlsProxy carries the original JDBC wire stream over verified TLS. Native H2
// sockets do not enable hostname identification. Every physical connection,
// including cancellation sockets, verifies the same immutable original target.
// The proxy belongs to one captured runtime and never reconnects a wire stream.
type tlsProxy struct {
	listener    net.Listener
	target      string
	config      *tls.Config
	timeout     time.Duration
	ctx         context.Context
	cancel      context.CancelFunc
	mu          sync.Mutex
	connections map[net.Conn]struct{}
	wg          sync.WaitGroup
	once        sync.Once
}

func startTLSProxy(ctx context.Context, p connections.Profile) (*tlsProxy, error) {
	config := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: p.Host}
	if path := p.H2Options.TLSCAFile; path != "" {
		file, err := os.Open(path)
		if err != nil {
			return nil, errTLSTrust
		}
		raw, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
		file.Close()
		if err != nil || len(raw) > 1<<20 {
			return nil, errTLSTrust
		}
		config.RootCAs = x509.NewCertPool()
		if !config.RootCAs.AppendCertsFromPEM(raw) {
			return nil, errTLSTrust
		}
	}
	target := net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port)))
	timeout := time.Duration(p.H2Options.ConnectTimeoutSeconds) * time.Second
	// Check trust before launching a JDBC executor. Each later dial verifies
	// again, so the check does not authorize a changed server certificate.
	probeCtx, stop := context.WithTimeout(ctx, timeout)
	probe, err := (&tls.Dialer{NetDialer: &net.Dialer{}, Config: config}).DialContext(probeCtx, "tcp", target)
	stop()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errTLSTrust
	}
	probe.Close()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, errTLSTrust
	}
	root, cancel := context.WithCancel(context.Background())
	proxy := &tlsProxy{listener: listener, target: target, config: config, timeout: timeout, ctx: root, cancel: cancel, connections: make(map[net.Conn]struct{})}
	proxy.wg.Add(1)
	go proxy.accept()
	return proxy, nil
}

func (p *tlsProxy) endpoint() (string, uint16) {
	address := p.listener.Addr().(*net.TCPAddr)
	return "127.0.0.1", uint16(address.Port)
}

func (p *tlsProxy) track(conn net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ctx.Err() != nil {
		closeWire(conn)
		return false
	}
	p.connections[conn] = struct{}{}
	return true
}

func (p *tlsProxy) forget(conn net.Conn) {
	closeWire(conn)
	p.mu.Lock()
	delete(p.connections, conn)
	p.mu.Unlock()
}

func (p *tlsProxy) accept() {
	defer p.wg.Done()
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			return
		}
		if !p.track(conn) {
			continue
		}
		p.wg.Add(1)
		go p.forward(conn)
	}
}

func (p *tlsProxy) forward(local net.Conn) {
	defer p.wg.Done()
	defer p.forget(local)
	ctx, cancel := context.WithTimeout(p.ctx, p.timeout)
	remote, err := (&tls.Dialer{NetDialer: &net.Dialer{}, Config: p.config}).DialContext(ctx, "tcp", p.target)
	cancel()
	if err != nil || !p.track(remote) {
		return
	}
	defer p.forget(remote)
	done := make(chan struct{}, 1)
	go func() { _, _ = io.Copy(remote, local); closeWire(remote); closeWire(local); done <- struct{}{} }()
	_, _ = io.Copy(local, remote)
	closeWire(local)
	closeWire(remote)
	<-done
}

func (p *tlsProxy) Close() {
	if p == nil {
		return
	}
	p.once.Do(func() {
		p.cancel()
		p.listener.Close()
		p.mu.Lock()
		for conn := range p.connections {
			closeWire(conn)
		}
		p.mu.Unlock()
		p.wg.Wait()
	})
}

func closeWire(conn net.Conn) { _ = conn.SetDeadline(time.Now()); _ = conn.Close() }

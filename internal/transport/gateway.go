package transport

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

type GatewayEvent struct {
	State          string
	Duration       time.Duration
	Sent, Received int64
}
type GatewayConfig struct {
	Targets     map[string]string
	Token       string
	DialContext func(context.Context, string, string) (net.Conn, error)
	Log         func(GatewayEvent)
}

// Gateway accepts only configured opaque target IDs. It never accepts a
// client-supplied database host, and never interprets or logs database data.
type Gateway struct {
	ctx      context.Context
	cancel   context.CancelFunc
	config   GatewayConfig
	mu       sync.Mutex
	channels map[net.Conn]struct{}
	closed   bool
}

func NewGateway(c GatewayConfig) (*Gateway, error) {
	if c.Token == "" || len(c.Token) > 4096 || strings.ContainsAny(c.Token, "\r\n") {
		return nil, errors.New("gateway token required")
	}
	if len(c.Targets) == 0 {
		return nil, errors.New("gateway targets required")
	}
	copyTargets := make(map[string]string, len(c.Targets))
	for id, a := range c.Targets {
		if id == "" || len(id) > 128 || strings.ContainsAny(id, "\r\n/") {
			return nil, errors.New("invalid target ID")
		}
		if _, _, e := net.SplitHostPort(a); e != nil {
			return nil, e
		}
		copyTargets[id] = a
	}
	c.Targets = copyTargets
	if c.DialContext == nil {
		c.DialContext = (&net.Dialer{Timeout: 10 * time.Second}).DialContext
	}
	lifetime, cancel := context.WithCancel(context.Background())
	return &Gateway{ctx: lifetime, cancel: cancel, config: c, channels: make(map[net.Conn]struct{})}, nil
}
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	expected := "Bearer " + g.config.Token
	if len(r.Header.Get("Authorization")) != len(expected) || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(expected)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	target, ok := g.config.Targets[r.Header.Get("X-NyaDB-Target-ID")]
	if !ok || r.URL.RawQuery != "" {
		http.Error(w, "unknown target", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	srv := websocket.Server{Handshake: func(c *websocket.Config, _ *http.Request) error {
		for _, p := range c.Protocol {
			if p == Protocol {
				c.Protocol = []string{Protocol}
				return nil
			}
		}
		return errors.New("nyadb-tcp-v1 required")
	}, Handler: func(ws *websocket.Conn) {
		start := time.Now()
		requestCtx, stopRequest := context.WithCancel(r.Context())
		stopLifetime := context.AfterFunc(g.ctx, stopRequest)
		defer stopLifetime()
		defer stopRequest()
		ctx, cancel := context.WithTimeout(requestCtx, 10*time.Second)
		db, err := g.config.DialContext(ctx, "tcp", target)
		cancel()
		if err != nil {
			_ = ws.Close()
			g.log(GatewayEvent{State: "dial_failed", Duration: time.Since(start)})
			return
		}
		client := frameSocket(ws)
		g.mu.Lock()
		if g.closed {
			g.mu.Unlock()
			_ = client.Close()
			_ = db.Close()
			return
		}
		g.channels[client] = struct{}{}
		g.channels[db] = struct{}{}
		g.mu.Unlock()
		g.log(GatewayEvent{State: "connected"})
		defer func() {
			_ = client.Close()
			_ = db.Close()
			g.mu.Lock()
			delete(g.channels, client)
			delete(g.channels, db)
			g.mu.Unlock()
		}()
		out := make(chan int64, 1)
		go func() { n, _ := io.Copy(db, client); out <- n; _ = db.Close(); _ = client.Close() }()
		received, _ := io.Copy(client, db)
		_ = db.Close()
		_ = client.Close()
		sent := <-out
		g.log(GatewayEvent{State: "closed", Duration: time.Since(start), Sent: sent, Received: received})
	}}
	srv.ServeHTTP(w, r)
}
func (g *Gateway) log(e GatewayEvent) {
	if g.config.Log != nil {
		g.config.Log(e)
	}
}
func (g *Gateway) Close() error {
	g.mu.Lock()
	g.closed = true
	all := g.channels
	g.channels = make(map[net.Conn]struct{})
	g.mu.Unlock()
	g.cancel()
	var err error
	for c := range all {
		err = errors.Join(err, c.Close())
	}
	return err
}

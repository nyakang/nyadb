// nyadb-gateway forwards authenticated NyaDB binary WebSocket channels only to
// targets named in its operator-owned configuration. TLS is mandatory.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/nyakang/nyadb/internal/transport"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type config struct {
	Listen           string            `json:"listen"`
	Certificate      string            `json:"certificate"`
	Key              string            `json:"key"`
	ClientCA         string            `json:"clientCA,omitempty"`
	TokenEnvironment string            `json:"tokenEnvironment"`
	Targets          map[string]string `json:"targets"`
}

func main() {
	if err := run(); err != nil {
		log.Print("gateway startup or shutdown failed")
		os.Exit(1)
	}
}
func run() error {
	file := flag.String("config", "gateway.json", "configuration path")
	flag.Parse()
	f, e := os.Open(*file)
	if e != nil {
		return e
	}
	defer f.Close()
	var c config
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if e = decoder.Decode(&c); e != nil {
		return e
	}
	if c.Listen == "" || c.Certificate == "" || c.Key == "" || c.TokenEnvironment == "" {
		return fmt.Errorf("incomplete gateway configuration")
	}
	g, e := transport.NewGateway(transport.GatewayConfig{Targets: c.Targets, Token: os.Getenv(c.TokenEnvironment), Log: func(e transport.GatewayEvent) {
		log.Printf("state=%s duration_ms=%d sent=%d received=%d", e.State, e.Duration.Milliseconds(), e.Sent, e.Received)
	}})
	if e != nil {
		return e
	}
	defer g.Close()
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if c.ClientCA != "" {
		b, e := os.ReadFile(c.ClientCA)
		if e != nil {
			return e
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(b) {
			return fmt.Errorf("invalid client CA")
		}
		tlsConfig.ClientCAs = pool
		tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
	}
	server := &http.Server{Addr: c.Listen, Handler: g, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 16 << 10, TLSConfig: tlsConfig, ErrorLog: log.New(ioDiscard{}, "", 0)}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, os.Interrupt)
	defer signal.Stop(signals)
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServeTLS(c.Certificate, c.Key) }()
	select {
	case e := <-done:
		if e == http.ErrServerClosed {
			return nil
		}
		return e
	case <-signals:
		_ = g.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(ctx)
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }

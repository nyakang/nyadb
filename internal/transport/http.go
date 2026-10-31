package transport

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
	"golang.org/x/net/websocket"
)

func readSmallFile(path string) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return nil, e
	}
	if info.Size() > 1<<20 {
		return nil, errors.New("certificate/key exceeds 1 MiB")
	}
	b, e := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if e != nil {
		return nil, e
	}
	if len(b) > 1<<20 {
		return nil, errors.New("certificate/key exceeds 1 MiB")
	}
	return b, nil
}
func newHTTPDial(a connections.MySQLHTTP, s Secrets) (func(context.Context, string, string) (net.Conn, error), error) {
	u, err := url.Parse(a.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("NyaDB gateway requires an HTTPS URL without credentials or query")
	}
	if a.TargetID == "" || len(a.TargetID) > 128 || strings.ContainsAny(a.TargetID, "\r\n/") {
		return nil, errors.New("NyaDB gateway target ID required")
	}
	if s.HTTPToken == "" || len(s.HTTPToken) > 4096 || strings.ContainsAny(s.HTTPToken, "\r\n") {
		return nil, errors.New("NyaDB gateway token required")
	}
	if a.Base64Queries {
		return nil, errors.New("base64 query tunneling is unsupported; disable this legacy option")
	}
	t := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname()}
	if a.CACertificate != "" {
		b, e := readSmallFile(a.CACertificate)
		if e != nil {
			return nil, e
		}
		roots, e := x509.SystemCertPool()
		if e != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(b) {
			return nil, errors.New("invalid gateway CA certificate")
		}
		t.RootCAs = roots
	}
	if a.CertificateAuth {
		cert, e := readSmallFile(a.ClientCertificate)
		if e != nil {
			return nil, e
		}
		key, e := readSmallFile(a.ClientKey)
		if e != nil {
			return nil, e
		}
		defer clear(key)
		if block, _ := pem.Decode(key); block != nil && x509.IsEncryptedPEMBlock(block) {
			plain, e := x509.DecryptPEMBlock(block, []byte(s.HTTPKeyPassphrase))
			if e != nil {
				return nil, errors.New("unable to decrypt gateway client key")
			}
			defer clear(plain)
			key = pem.EncodeToMemory(&pem.Block{Type: block.Type, Bytes: plain})
			defer clear(key)
		}
		pair, e := tls.X509KeyPair(cert, key)
		if e != nil {
			return nil, errors.New("invalid gateway client certificate/key")
		}
		t.Certificates = []tls.Certificate{pair}
	}
	endpoint := u.Host
	if u.Port() == "" {
		endpoint = net.JoinHostPort(u.Hostname(), "443")
	}
	wsURL := *u
	wsURL.Scheme = "wss"
	return func(ctx context.Context, _, _ string) (net.Conn, error) {
		config, e := websocket.NewConfig(wsURL.String(), a.URL)
		if e != nil {
			return nil, e
		}
		config.Protocol = []string{Protocol}
		config.Header = http.Header{"Authorization": []string{"Bearer " + s.HTTPToken}, "X-NyaDB-Target-ID": []string{a.TargetID}}
		dialAddr := endpoint
		if a.ProxyEnabled {
			if a.ProxyHost == "" || strings.ContainsAny(a.ProxyHost, "\r\n") {
				return nil, errors.New("invalid proxy host")
			}
			dialAddr = net.JoinHostPort(a.ProxyHost, fmt.Sprint(a.ProxyPort))
		}
		raw, e := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", dialAddr)
		if e != nil {
			return nil, e
		}
		success := false
		defer func() {
			if !success {
				_ = raw.Close()
			}
		}()
		stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
		defer stop()
		if a.ProxyEnabled {
			h := http.Header{}
			if a.ProxyUsername != "" {
				h.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(a.ProxyUsername+":"+s.ProxyPassword)))
			}
			req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: endpoint}, Host: endpoint, Header: h}
			if e = req.Write(raw); e != nil {
				return nil, e
			}
			resp, e := http.ReadResponse(bufio.NewReaderSize(raw, 4096), req)
			if e != nil {
				return nil, errors.New("proxy handshake failed")
			}
			if resp.StatusCode != http.StatusOK {
				return nil, errors.New("proxy rejected CONNECT")
			}
		}
		outer := tls.Client(raw, t.Clone())
		if e = outer.HandshakeContext(ctx); e != nil {
			return nil, e
		}
		ws, e := websocket.NewClient(config, outer)
		if e != nil {
			return nil, errors.New("gateway WebSocket handshake failed")
		}
		if ctx.Err() != nil {
			_ = ws.Close()
			return nil, ctx.Err()
		}
		if len(ws.Config().Protocol) != 1 || ws.Config().Protocol[0] != Protocol {
			_ = ws.Close()
			return nil, errors.New("gateway subprotocol mismatch")
		}
		success = true
		return frameSocket(ws), nil
	}, nil
}

package tdengine

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestWebSocketTLSRequiresTrustedOriginalCertificateAndHost(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(rw, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		kind, raw, err := c.ReadMessage()
		if err != nil {
			return
		}
		var request struct {
			Action string
			Args   struct {
				Request  uint64 `json:"req_id"`
				Username string `json:"user"`
				Password string `json:"password"`
			}
		}
		if kind != websocket.TextMessage || json.Unmarshal(raw, &request) != nil || request.Action != "conn" || request.Args.Username != "root" || request.Args.Password != "temporary-secret" {
			return
		}
		_ = c.WriteJSON(nativeResponse{Action: "conn", Request: request.Args.Request})
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	port, _ := strconv.ParseUint(u.Port(), 10, 16)
	p, _ := connections.Defaults(connections.TDengine)
	p.Host, p.Port, p.TLSMode = u.Hostname(), uint16(port), "verify-full"
	if w, err := openWire(context.Background(), p, "temporary-secret"); err == nil || w != nil {
		t.Fatal("untrusted native TLS certificate accepted")
	}
	ca := filepath.Join(t.TempDir(), "original-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	p.TDengineOptions.CAFile = ca
	w, err := openWire(context.Background(), p, "temporary-secret")
	if err != nil {
		t.Fatal("original trusted TLS connection failed", err)
	}
	w.close()
	p.Host = "localhost"
	if w, err := openWire(context.Background(), p, "temporary-secret"); err == nil || w != nil {
		t.Fatal("CA trust bypassed the original hostname")
	}
	if err := os.WriteFile(ca, []byte("not a certificate"), 0600); err != nil {
		t.Fatal(err)
	}
	p.Host = u.Hostname()
	if w, err := openWire(context.Background(), p, "temporary-secret"); err == nil || w != nil {
		t.Fatal("invalid original CA accepted")
	}
}

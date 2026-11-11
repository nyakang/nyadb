package dbruntime_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

func TestConsulBuiltinFailedHandshakeNeverPublishesSession(t *testing.T) {
	for _, fault := range []string{"http500", "lost", "canceled"} {
		t.Run(fault, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if fault == "lost" {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err == nil {
						conn.Close()
					}
					return
				}
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer server.Close()
			u, _ := url.Parse(server.URL)
			port, _ := strconv.Atoi(u.Port())
			p, _ := connections.Defaults(connections.Consul)
			p.ID, p.Name, p.Host, p.Port = 71, "failed original Consul", u.Hostname(), uint16(port)
			m := dbruntime.New()
			dbruntime.RegisterBuiltins(m, nil)
			defer m.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if fault == "canceled" {
				cancel()
			}
			session, err := m.ConnectSession(ctx, p, "")
			if err == nil || session != nil || m.IsConnected(p.ID) {
				t.Fatal("failed handshake published an original session", err)
			}
		})
	}
}

package mysql

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/nyakang/nyadb/internal/services/credentials"
	"golang.org/x/crypto/ssh"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/transport"
)

func TestMySQLGatewayDisposablePhysicalLeasesTransactionsAndRevocation(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_CORE_IT") != "1" {
		t.Skip("requires explicitly enabled disposable MySQL core fixture")
	}
	port, e := strconv.Atoi(os.Getenv("NYADB_MYSQL_CORE_PORT"))
	if e != nil || port < 1024 || port > 65535 {
		t.Fatal("invalid loopback fixture port")
	}
	gateway, e := transport.NewGateway(transport.GatewayConfig{Targets: map[string]string{"fixture": net.JoinHostPort("127.0.0.1", fmt.Sprint(port))}, Token: "ephemeral-fixture-token"})
	if e != nil {
		t.Fatal(e)
	}
	defer gateway.Close()
	server := httptest.NewTLSServer(gateway)
	defer server.Close()
	path := filepath.Join(t.TempDir(), "ca.pem")
	os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600)
	a := connections.DefaultMySQLAdvanced()
	a.HTTP.Enabled = true
	a.HTTP.URL = server.URL
	a.HTTP.TargetID = "fixture"
	a.HTTP.CACertificate = path
	settings := connections.ActiveSettings{ProfileID: 904, Host: "gateway-target.invalid", Port: 3306, Username: "nyadb_core", Database: "nyadb_core_it", MySQLAdvanced: a}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	manager := NewMySQLSessionManager()
	defer manager.Close()
	manager.TransportProvider = func(context.Context, connections.ActiveSettings) (transport.Dialer, error) {
		return transport.New(settings, transport.Options{Secrets: transport.Secrets{HTTPToken: "ephemeral-fixture-token"}})
	}
	if e = manager.Connect(ctx, settings, ""); e != nil {
		t.Fatal(e)
	}
	token, e := manager.ReserveSQLTabToken(904, 991)
	if e != nil {
		t.Fatal(e)
	}
	defer manager.ReleaseSQLTabToken(token)
	if _, e = manager.ExecuteSQLTabToken(ctx, token, "nyadb_core_it", "CREATE TEMPORARY TABLE tunnel_tmp(id int); INSERT INTO tunnel_tmp VALUES(41); SET @tunnel_state=42;"); e != nil {
		t.Fatal(e)
	}
	r, e := manager.ExecuteSQLTabToken(ctx, token, "nyadb_core_it", "SELECT id,@tunnel_state FROM tunnel_tmp")
	if e != nil || len(r.Rows) != 1 || r.Rows[0][0] != "41" || r.Rows[0][1] != "42" {
		t.Fatalf("physical lease state lost: %+v %v", r, e)
	}
	if _, e = manager.ExecuteSQLTabToken(ctx, token, "nyadb_core_it", "CREATE TABLE tunnel_tx(id int PRIMARY KEY, value varchar(20)) ENGINE=InnoDB;"); e != nil {
		t.Fatal(e)
	}
	defer func() {
		if manager.IsConnected(904) {
			manager.ExecuteSQLTabToken(context.Background(), token, "nyadb_core_it", "DROP TABLE tunnel_tx")
		}
	}()
	tx, e := manager.BeginMySQLDataTransaction(ctx, 904, "nyadb_core_it", "tunnel_tx")
	if e != nil {
		t.Fatal(e)
	}
	defer manager.RollbackMySQLDataTransaction(context.Background(), tx)
	edits := []MySQLDataEdit{{Kind: "insert", Values: map[string]MySQLDataValue{"id": {Text: "1"}, "value": {Text: "held"}}}}
	if _, e = manager.ApplyMySQLDataEditsInTransaction(ctx, tx, 904, "nyadb_core_it", "tunnel_tx", edits); e != nil {
		t.Fatal(e)
	}
	page, e := manager.TableDataPage(ctx, 904, "nyadb_core_it", "tunnel_tx", 0, 100)
	if e != nil || len(page.Rows) != 0 {
		t.Fatalf("uncommitted row visible to metadata lease: %+v %v", page, e)
	}
	if e = manager.CommitMySQLDataTransaction(ctx, tx); e != nil {
		t.Fatal(e)
	}
	page, e = manager.TableDataPage(ctx, 904, "nyadb_core_it", "tunnel_tx", 0, 100)
	if e != nil || len(page.Rows) != 1 {
		t.Fatalf("transaction lost across channels: %+v %v", page, e)
	}
	if _, e = manager.ExecuteSQLTabToken(ctx, token, "nyadb_core_it", "DROP TABLE tunnel_tx"); e != nil {
		t.Fatal(e)
	}
	slow, cancelSlow := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		_, e := manager.ExecuteSQLTabToken(slow, token, "nyadb_core_it", "SELECT SLEEP(10)")
		done <- e
	}()
	time.Sleep(40 * time.Millisecond)
	gateway.Close()
	cancelSlow()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("disconnected long query succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not acknowledge driver")
	}
	deadline := time.Now().Add(2 * time.Second)
	for manager.IsConnected(904) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if manager.IsConnected(904) || !manager.TunnelRevoked(904) {
		t.Fatal("manager did not revoke original tunnel")
	}
	if _, e = manager.ExecuteSQLTabToken(ctx, token, "nyadb_core_it", "SELECT 1"); e == nil || (!errors.Is(e, ErrMySQLSQLSessionLost) && !errors.Is(e, ErrMySQLNotConnected) && !errors.Is(e, context.Canceled)) {
		t.Fatalf("old lease recreated: %v", e)
	}
}

func TestMySQLSSHDisposablePhysicalLeaseAndTransaction(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_CORE_IT") != "1" {
		t.Skip("requires explicitly enabled disposable MySQL core fixture")
	}
	port, e := strconv.Atoi(os.Getenv("NYADB_MYSQL_CORE_PORT"))
	if e != nil || port < 1024 || port > 65535 {
		t.Fatal("invalid loopback fixture port")
	}
	target := net.JoinHostPort("127.0.0.1", fmt.Sprint(port))
	_, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	signer, e := ssh.NewSignerFromKey(key)
	if e != nil {
		t.Fatal(e)
	}
	config := &ssh.ServerConfig{PasswordCallback: func(m ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
		if m.User() == "fixture" && string(p) == "ssh-fixture-token" {
			return nil, nil
		}
		return nil, errors.New("refused")
	}}
	config.AddHostKey(signer)
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	var mu sync.Mutex
	var clients []net.Conn
	closeClients := func() {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range clients {
			c.Close()
		}
	}
	defer closeClients()
	go func() {
		for {
			raw, e := listener.Accept()
			if e != nil {
				return
			}
			mu.Lock()
			clients = append(clients, raw)
			mu.Unlock()
			go func() {
				_, channels, requests, e := ssh.NewServerConn(raw, config)
				if e != nil {
					raw.Close()
					return
				}
				go ssh.DiscardRequests(requests)
				for ch := range channels {
					var req struct {
						Host       string
						Port       uint32
						Origin     string
						OriginPort uint32
					}
					if ch.ChannelType() != "direct-tcpip" || ssh.Unmarshal(ch.ExtraData(), &req) != nil || net.JoinHostPort(req.Host, fmt.Sprint(req.Port)) != target {
						ch.Reject(ssh.Prohibited, "fixture target only")
						continue
					}
					db, e := net.Dial("tcp", target)
					if e != nil {
						ch.Reject(ssh.ConnectionFailed, "unavailable")
						continue
					}
					c, r, e := ch.Accept()
					if e != nil {
						db.Close()
						continue
					}
					go ssh.DiscardRequests(r)
					go func() {
						defer db.Close()
						defer c.Close()
						done := make(chan struct{})
						go func() { io.Copy(db, c); db.Close(); close(done) }()
						io.Copy(c, db)
						c.Close()
						<-done
					}()
				}
			}()
		}
	}()
	host, ptext, _ := net.SplitHostPort(listener.Addr().String())
	sshPort, _ := strconv.Atoi(ptext)
	a := connections.DefaultMySQLAdvanced()
	a.SSH = connections.MySQLSSH{Enabled: true, Host: host, Port: sshPort, Username: "fixture", AuthMethod: connections.MySQLSSHPassword}
	settings := connections.ActiveSettings{ProfileID: 905, Host: "127.0.0.1", Port: uint16(port), Username: "nyadb_core", Database: "nyadb_core_it", MySQLAdvanced: a}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	manager := NewMySQLSessionManager()
	defer manager.Close()
	hosts := &credentials.MemoryHostStore{}
	manager.TransportProvider = func(context.Context, connections.ActiveSettings) (transport.Dialer, error) {
		return transport.New(settings, transport.Options{Secrets: transport.Secrets{SSHPassword: "ssh-fixture-token"}, Hosts: hosts, ConfirmHost: func(_ context.Context, h transport.HostIdentity) (bool, error) {
			if h.Fingerprint != ssh.FingerprintSHA256(signer.PublicKey()) {
				return false, errors.New("wrong host fingerprint")
			}
			return true, nil
		}})
	}
	if e = manager.Connect(ctx, settings, ""); e != nil {
		t.Fatal(e)
	}
	token, e := manager.ReserveSQLTabToken(905, 992)
	if e != nil {
		t.Fatal(e)
	}
	defer manager.ReleaseSQLTabToken(token)
	if _, e = manager.ExecuteSQLTabToken(ctx, token, "nyadb_core_it", "CREATE TEMPORARY TABLE ssh_tmp(id int) ENGINE=InnoDB; START TRANSACTION; INSERT INTO ssh_tmp VALUES(1); SET @ssh_state=43;"); e != nil {
		t.Fatal(e)
	}
	r, e := manager.ExecuteSQLTabToken(ctx, token, "nyadb_core_it", "SELECT COUNT(*),@ssh_state FROM ssh_tmp")
	if e != nil || r.Rows[0][0] != "1" || r.Rows[0][1] != "43" {
		t.Fatalf("SSH physical state lost: %+v %v", r, e)
	}
	if _, e = manager.ExecuteSQLTabToken(ctx, token, "nyadb_core_it", "ROLLBACK;"); e != nil {
		t.Fatal(e)
	}
	r, e = manager.ExecuteSQLTabToken(ctx, token, "nyadb_core_it", "SELECT COUNT(*) FROM ssh_tmp")
	if e != nil || r.Rows[0][0] != "0" {
		t.Fatalf("SSH transaction did not roll back: %+v %v", r, e)
	}
	closeClients()
	deadline := time.Now().Add(2 * time.Second)
	for manager.IsConnected(905) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if manager.IsConnected(905) || !manager.TunnelRevoked(905) {
		t.Fatal("idle SSH disconnect retained session")
	}
	if _, e = manager.ExecuteSQLTabToken(ctx, token, "nyadb_core_it", "SELECT 1"); e == nil {
		t.Fatal("SSH lease recreated automatically")
	}
}

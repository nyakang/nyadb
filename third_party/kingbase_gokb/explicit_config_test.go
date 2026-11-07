package gokb

import (
	"bufio"
	"context"
	"crypto/tls"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func TestExplicitSettingsIgnoreEnvironmentAndCopyRuntimeParams(t *testing.T) {
	// Unsupported KBHOSTADDR panics in the legacy environment constructor.
	for _, key := range []string{"KBHOSTADDR", "KBSERVICE", "KBSSLCRL", "KBHOST", "KBUSER", "KBPASSWORD", "KBSSLCERT", "KBSSLKEY", "KBPASSFILE"} {
		t.Setenv(key, "ambient-unwanted-value")
	}
	params := map[string]string{"application_name": "owned", "search_path": `"schema"`}
	tlsConfig := &tls.Config{ServerName: "owned", MinVersion: tls.VersionTLS12}
	c, err := NewConnectorConfig(Config{Host: "::1", Port: 54321, User: "explicit-user", Database: "test", Password: "", ConnectTimeout: time.Second, RuntimeParams: params, TLSConfig: tlsConfig})
	if err != nil {
		t.Fatal(err)
	}
	params["application_name"] = "changed"
	tlsConfig.ServerName = "changed"
	if c.opts["host"] != "::1" || c.opts["user"] != "explicit-user" || c.opts["dbname"] != "test" || c.opts["password"] != "" || c.opts["application_name"] != "owned" || c.tlsConfig.ServerName != "owned" {
		t.Fatal("explicit connector used ambient or subsequently changed settings")
	}
	if _, ok := c.opts["password"]; !ok {
		t.Fatal("empty explicit password permits .kbpass fallback")
	}
}

func TestAlreadyCancelledOperationsPerformNoProtocolIO(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	socket := &observedSocket{Conn: client}
	defer socket.Close()
	cn := &conn{c: socket, buf: bufio.NewReader(socket)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cn.BeginTx(ctx, driver.TxOptions{}); err != context.Canceled {
		t.Fatal(err)
	}
	if _, err := cn.QueryContext(ctx, "SELECT 1", nil); err != context.Canceled {
		t.Fatal(err)
	}
	if _, err := cn.ExecContext(ctx, "UPDATE fixture SET n=1", nil); err != context.Canceled {
		t.Fatal(err)
	}
	if _, err := cn.PrepareContext(ctx, "SELECT 1"); err != context.Canceled {
		t.Fatal(err)
	}
	st := &stmt{cn: cn}
	if _, err := st.QueryContext(ctx, nil); err != context.Canceled {
		t.Fatal(err)
	}
	if _, err := st.ExecContext(ctx, nil); err != context.Canceled {
		t.Fatal(err)
	}
	if err := cn.Ping(ctx); err != context.Canceled {
		t.Fatal(err)
	}
	if socket.writes.Load() != 0 {
		t.Fatal("already cancelled request wrote to socket")
	}
}

func TestLostWriteAcknowledgementDoesNotReplayThroughSQLPool(t *testing.T) {
	var opens atomic.Int32
	connector, err := NewConnectorConfig(Config{Host: "127.0.0.1", Port: 54321, User: "fixture", Database: "test", ConnectTimeout: time.Second,
		DialFunc: func(context.Context, string, string) (net.Conn, error) {
			opens.Add(1)
			client, server := net.Pipe()
			go func() {
				defer server.Close()
				if _, err := readPacket(server); err != nil {
					return
				}
				messages := backendMessage('R', []byte{0, 0, 0, 0})
				messages = append(messages, backendMessage('Z', []byte{'I'})...)
				if _, err := server.Write(messages); err != nil {
					return
				}
				kind := make([]byte, 1)
				if _, err := io.ReadFull(server, kind); err != nil {
					return
				}
				// The full write request was received. Lose its acknowledgement.
				_, _ = readPacket(server)
			}()
			return client, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err = db.ExecContext(ctx, "UPDATE fixture SET n=n+1"); !errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, driver.ErrBadConn) {
		t.Fatal("lost acknowledgement did not retain an unreplayable unknown outcome", err)
	}
	if opens.Load() != 1 {
		t.Fatal("uncertain write replayed on a new physical session")
	}
}

func TestLostCommitAcknowledgementRetainsUnknownOutcomeAndRetiresSession(t *testing.T) {
	var opens atomic.Int32
	connector, err := NewConnectorConfig(Config{Host: "127.0.0.1", Port: 54321, User: "fixture", Database: "test", ConnectTimeout: time.Second,
		DialFunc: func(context.Context, string, string) (net.Conn, error) {
			opens.Add(1)
			client, server := net.Pipe()
			go func() {
				defer server.Close()
				if _, err := readPacket(server); err != nil {
					return
				}
				messages := backendMessage('R', []byte{0, 0, 0, 0})
				messages = append(messages, backendMessage('Z', []byte{'I'})...)
				if _, err := server.Write(messages); err != nil {
					return
				}
				for _, tag := range []string{"BEGIN", "UPDATE 1", "COMMIT"} {
					kind := make([]byte, 1)
					if _, err := io.ReadFull(server, kind); err != nil {
						return
					}
					if _, err := readPacket(server); err != nil {
						return
					}
					if tag == "COMMIT" {
						return
					}
					messages = backendMessage('C', append([]byte(tag), 0))
					messages = append(messages, backendMessage('Z', []byte{'T'})...)
					if _, err := server.Write(messages); err != nil {
						return
					}
				}
			}()
			return client, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	original, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	tx, err := original.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, "UPDATE fixture SET n=n+1"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); !errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, driver.ErrBadConn) {
		t.Fatal("unacknowledged commit lost its unknown status", err)
	}
	if _, err = original.ExecContext(ctx, "SELECT 1"); err == nil {
		t.Fatal("original physical session reused after uncertain commit")
	}
	if opens.Load() != 1 {
		t.Fatal("uncertain transaction opened a replacement session")
	}
}

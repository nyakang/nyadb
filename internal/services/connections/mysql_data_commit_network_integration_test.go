package connections

import (
	"context"
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
)

// This packet relay uses the real MySQL server and driver. It forwards all
// traffic unchanged until an armed COMMIT, then either disconnects before
// forwarding COMMIT or receives and discards the server's successful reply.
// It is deliberately limited to disposable, plaintext loopback test traffic.
type mysqlCommitFaultProxy struct {
	listener        net.Listener
	upstream        string
	dropReply       bool
	armed           atomic.Bool
	commits, writes atomic.Int32
	receivedOK      atomic.Bool
	mu              sync.Mutex
	connections     []net.Conn
	wg              sync.WaitGroup
}

func newMySQLCommitFaultProxy(t *testing.T, upstream string, dropReply bool) *mysqlCommitFaultProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &mysqlCommitFaultProxy{listener: ln, upstream: upstream, dropReply: dropReply}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			p.connections = append(p.connections, client)
			p.mu.Unlock()
			p.wg.Add(1)
			go p.relay(client)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		p.mu.Lock()
		for _, conn := range p.connections {
			_ = conn.Close()
		}
		p.mu.Unlock()
		p.wg.Wait()
	})
	return p
}

func (p *mysqlCommitFaultProxy) relay(client net.Conn) {
	defer p.wg.Done()
	defer client.Close()
	server, err := net.DialTimeout("tcp", p.upstream, 3*time.Second)
	if err != nil {
		return
	}
	defer server.Close()
	// Bound both transport pumps even if a failed test leaves the peer idle.
	_ = client.SetDeadline(time.Now().Add(20 * time.Second))
	_ = server.SetDeadline(time.Now().Add(20 * time.Second))
	var discardAck atomic.Bool
	done := make(chan struct{}, 2)
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			header, payload, err := mysqlCommitFaultPacket(client)
			if err != nil {
				return
			}
			if header[3] == 0 && len(payload) > 1 && (payload[0] == 3 || payload[0] == 0x16) {
				query := strings.ToUpper(strings.TrimSpace(string(payload[1:])))
				if strings.HasPrefix(query, "UPDATE ") || strings.HasPrefix(query, "INSERT ") || strings.HasPrefix(query, "DELETE ") {
					p.writes.Add(1)
				}
				if payload[0] == 3 && query == "COMMIT" {
					p.commits.Add(1)
					if p.armed.CompareAndSwap(true, false) {
						if !p.dropReply {
							return
						}
						discardAck.Store(true)
					}
				}
			}
			if _, err = server.Write(append(header[:], payload...)); err != nil {
				return
			}
		}
	}()
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			header, payload, err := mysqlCommitFaultPacket(server)
			if err != nil {
				return
			}
			if discardAck.Load() {
				p.receivedOK.Store(len(payload) > 0 && payload[0] == 0)
				return
			}
			if _, err = client.Write(append(header[:], payload...)); err != nil {
				return
			}
		}
	}()
	<-done
	_ = client.Close()
	_ = server.Close()
	<-done
}

func mysqlCommitFaultPacket(r io.Reader) ([4]byte, []byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return header, nil, err
	}
	size := int(header[0]) | int(header[1])<<8 | int(header[2])<<16
	if size > 1<<20 {
		return header, nil, errors.New("fault fixture packet exceeds 1 MiB")
	}
	payload := make([]byte, size)
	_, err := io.ReadFull(r, payload)
	return header, payload, err
}

func TestMySQLDataUnknownCommitNetworkIntegration(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_DATA_EDIT_IT") != "1" {
		t.Skip("requires disposable loopback MySQL edit fixture")
	}
	port, err := strconv.Atoi(os.Getenv("NYADB_MYSQL_DATA_EDIT_PORT"))
	if err != nil || port < 1024 || port > 65535 {
		t.Fatal("explicit disposable loopback port required")
	}
	database := os.Getenv("NYADB_MYSQL_DATA_EDIT_DATABASE")
	if database == "" {
		database = "table_edit_it"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	settings := ActiveSettings{ProfileID: 1, Host: "127.0.0.1", Port: uint16(port), Username: "data_probe", MySQLAdvanced: DefaultMySQLAdvanced()}
	password := os.Getenv("NYADB_MYSQL_DATA_EDIT_PASSWORD")
	observer, err := OpenDirectMySQL(ctx, settings, password)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()

	for _, dropReply := range []bool{false, true} {
		mode := "commit_not_forwarded"
		if dropReply {
			mode = "successful_commit_reply_lost"
		}
		for _, kind := range []string{"update", "insert", "delete"} {
			t.Run(mode+"/"+kind, func(t *testing.T) {
				table := fmt.Sprintf("network_edit_%d", time.Now().UnixNano())
				target, err := mysqlEditTable(database, table)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := observer.ExecContext(ctx, "CREATE TABLE "+target+" (id BIGINT PRIMARY KEY AUTO_INCREMENT, note VARCHAR(80)) ENGINE=InnoDB"); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
					defer stop()
					if _, err := observer.ExecContext(cleanup, "DROP TABLE IF EXISTS "+target); err != nil {
						t.Error(err)
					}
				})
				if _, err := observer.ExecContext(ctx, "INSERT INTO "+target+" VALUES (41,'before')"); err != nil {
					t.Fatal(err)
				}
				proxy := newMySQLCommitFaultProxy(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), dropReply)
				proxyPort := proxy.listener.Addr().(*net.TCPAddr).Port
				managed := NewMySQLSessionManager()
				t.Cleanup(func() { _ = managed.Close() })
				proxiedSettings := settings
				proxiedSettings.Port = uint16(proxyPort)
				if err := managed.Connect(ctx, proxiedSettings, password); err != nil {
					t.Fatal(err)
				}
				page, err := managed.TableDataPage(ctx, 1, database, table, 0, 100)
				if err != nil {
					t.Fatal(err)
				}
				edit := MySQLDataEdit{Kind: kind, Original: page.RowSnapshots[0]}
				if kind != "delete" {
					edit.Values = map[string]MySQLDataValue{"note": {Text: "after"}}
				}
				if kind == "insert" {
					edit.Original = nil
				}
				proxy.armed.Store(true)
				result, err := managed.ApplyMySQLDataEdits(ctx, 1, database, table, []MySQLDataEdit{edit})
				if !errors.Is(err, ErrMySQLDataCommitUncertain) || result == nil || !result.CommitUncertain || len(result.Outcomes) != 1 {
					t.Fatalf("network failure concealed: result=%+v error=%v", result, err)
				}
				if proxy.armed.Load() || proxy.receivedOK.Load() != dropReply {
					t.Fatal("declared transport fault did not occur")
				}
				writesBefore, commitsBefore := proxy.writes.Load(), proxy.commits.Load()
				verification, err := managed.VerifyMySQLDataEdits(ctx, 1, database, table, result)
				if err != nil {
					t.Fatal(err)
				}
				expectedStatus := "not_applied"
				if dropReply {
					expectedStatus = "applied"
				}
				if verification.Status != expectedStatus {
					t.Fatalf("verification=%+v expected=%s", verification, expectedStatus)
				}
				if writesBefore != 1 || commitsBefore != 1 || proxy.writes.Load() != writesBefore || proxy.commits.Load() != commitsBefore {
					t.Fatalf("verification replayed write: writes=%d->%d commits=%d->%d", writesBefore, proxy.writes.Load(), commitsBefore, proxy.commits.Load())
				}
				var beforeCount, afterCount int
				if err := observer.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+target+" WHERE note='before'").Scan(&beforeCount); err != nil {
					t.Fatal(err)
				}
				if err := observer.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+target+" WHERE note='after'").Scan(&afterCount); err != nil {
					t.Fatal(err)
				}
				expectedBefore, expectedAfter := 1, 0
				if dropReply {
					switch kind {
					case "update":
						expectedBefore, expectedAfter = 0, 1
					case "insert":
						expectedBefore, expectedAfter = 1, 1
					case "delete":
						expectedBefore, expectedAfter = 0, 0
					}
				}
				if beforeCount != expectedBefore || afterCount != expectedAfter {
					t.Fatalf("independent server observation before=%d after=%d", beforeCount, afterCount)
				}
				// The caller's draft remains available; neither failure nor verification
				// consumes/mutates it. The UI separately tests its pending-state lock.
				if edit.Kind != kind || (kind != "delete" && edit.Values["note"].Text != "after") {
					t.Fatal("failure consumed caller draft")
				}
				t.Logf("table=%s server_OK_discarded=%t verification=%s writes=%d commits=%d independent_before=%d independent_after=%d generated_id=%d", table, proxy.receivedOK.Load(), verification.Status, proxy.writes.Load(), proxy.commits.Load(), beforeCount, afterCount, result.Outcomes[0].GeneratedID)
			})
		}
	}
}

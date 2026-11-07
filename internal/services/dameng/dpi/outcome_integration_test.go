package dpi

import (
	"bytes"
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// A single owned server receives the actual operation. Its first response after
// arming is discarded before reaching the original client; bytes are not logged.
type lostReply struct {
	listener  net.Listener
	armed     atomic.Bool
	requested atomic.Bool
	dropped   chan struct{}
	done      chan struct{}
	stop      chan struct{}
}

func dropNativeReply(t *testing.T, endpoint, marker string) *lostReply {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &lostReply{listener: listener, dropped: make(chan struct{}), done: make(chan struct{}), stop: make(chan struct{})}
	go func() {
		defer close(p.done)
		client, err := listener.Accept()
		if err != nil {
			return
		}
		defer client.Close()
		server, err := net.DialTimeout("tcp", endpoint, time.Second)
		if err != nil {
			return
		}
		defer server.Close()
		watchDone := make(chan struct{})
		go func() {
			select {
			case <-p.stop:
				client.Close()
				server.Close()
			case <-watchDone:
			}
		}()
		defer close(watchDone)
		forwardDone := make(chan struct{})
		go func() {
			defer close(forwardDone)
			buffer := make([]byte, 64<<10)
			var tail []byte
			for {
				n, err := client.Read(buffer)
				if n > 0 {
					combined := append(tail, buffer[:n]...)
					if p.armed.Load() && bytes.Contains(combined, []byte(marker)) {
						p.requested.Store(true)
					}
					tail = append([]byte(nil), combined[max(0, len(combined)-len(marker)):]...)
					if _, err = io.Copy(server, bytes.NewReader(buffer[:n])); err != nil {
						break
					}
				}
				if err != nil {
					break
				}
			}
			server.Close()
		}()
		buffer := make([]byte, 64<<10)
		for {
			n, err := server.Read(buffer)
			if n > 0 {
				if p.requested.Load() {
					close(p.dropped)
					break
				}
				if _, err = client.Write(buffer[:n]); err != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
		client.Close()
		server.Close()
		<-forwardDone
	}()
	t.Cleanup(func() { close(p.stop); listener.Close(); <-p.done })
	return p
}

func TestNativeDPILostWriteAndCommitRepliesAreNeverReplayed(t *testing.T) {
	o, password := nativeFixture(t)
	observer := nativeDB(t, o, password)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, commit := range []bool{false, true} {
		t.Run(map[bool]string{false: "autocommit", true: "commit"}[commit], func(t *testing.T) {
			name := "NYADB_DPI_OUTCOME_" + suffix(t)
			if _, err := observer.ExecContext(ctx, "CREATE TABLE "+name+" (ID INT)"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { observer.ExecContext(context.Background(), "DROP TABLE "+name) })
			marker := "INSERT INTO " + name
			if commit {
				marker = "COMMIT"
			}
			proxy := dropNativeReply(t, net.JoinHostPort(o.Host, strconv.Itoa(int(o.Port))), marker)
			opts := o
			opts.Port = uint16(proxy.listener.Addr().(*net.TCPAddr).Port)
			db := nativeDB(t, opts, password)
			original, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer original.Close()
			if err = original.PingContext(ctx); err != nil {
				t.Fatal(err)
			}
			if commit {
				tx, err := original.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = tx.ExecContext(ctx, "INSERT INTO "+name+" VALUES (1)"); err != nil {
					tx.Rollback()
					t.Fatal(err)
				}
				proxy.armed.Store(true)
				err = tx.Commit()
				if !errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, driver.ErrBadConn) {
					t.Fatalf("lost commit outcome: %v", err)
				}
			} else {
				proxy.armed.Store(true)
				_, err = original.ExecContext(ctx, "INSERT INTO "+name+" VALUES (1)")
				if !errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, driver.ErrBadConn) {
					t.Fatalf("lost autocommit outcome: %v", err)
				}
			}
			select {
			case <-proxy.dropped:
			default:
				t.Fatal("did not discard an actual server reply")
			}
			if err = original.PingContext(ctx); err == nil {
				t.Fatal("uncertain physical session was reused")
			}
			var count int
			if err = observer.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+name).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("original single write left %d rows", count)
			}
		})
	}
}

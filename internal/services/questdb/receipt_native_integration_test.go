package questdb

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// The owned proxy forwards the original query unchanged, then withholds its
// complete CommandComplete/ReadyForQuery frames after the server has committed.
func holdOriginalPGReceipt(t *testing.T, upstream, marker string) (uint16, <-chan []byte, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stop, release, accepted := make(chan struct{}), make(chan struct{}), make(chan []byte, 1)
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	var workers sync.WaitGroup
	var conns sync.Map
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			conns.Store(client, true)
			go func() {
				defer workers.Done()
				defer conns.Delete(client)
				defer client.Close()
				server, err := net.DialTimeout("tcp", upstream, 3*time.Second)
				if err != nil {
					return
				}
				conns.Store(server, true)
				defer conns.Delete(server)
				defer server.Close()
				var held atomic.Bool
				var pipes sync.WaitGroup
				pipes.Add(2)
				go func() {
					defer pipes.Done()
					defer client.Close()
					defer server.Close()
					buf := make([]byte, 4096)
					var tail []byte
					for {
						n, err := client.Read(buf)
						if n > 0 {
							tail = append(tail, buf[:n]...)
							if bytes.Contains(tail, []byte(marker)) {
								held.Store(true)
							}
							if len(tail) > 16384 {
								tail = bytes.Clone(tail[len(tail)-8192:])
							}
							if _, e := server.Write(buf[:n]); e != nil {
								return
							}
						}
						if err != nil {
							return
						}
					}
				}()
				go func() {
					defer pipes.Done()
					defer client.Close()
					defer server.Close()
					var captured []byte
					for {
						header := make([]byte, 5)
						if _, err := io.ReadFull(server, header); err != nil {
							return
						}
						n := binary.BigEndian.Uint32(header[1:])
						if n < 4 || n > 4<<20 {
							return
						}
						payload := make([]byte, int(n)-4)
						if _, err := io.ReadFull(server, payload); err != nil {
							return
						}
						packet := append(header, payload...)
						if held.Load() {
							captured = append(captured, packet...)
							if header[0] != 'Z' {
								continue
							}
							select {
							case accepted <- bytes.Clone(captured):
							case <-stop:
								return
							}
							select {
							case <-release:
							case <-stop:
								return
							}
							if _, err := client.Write(captured); err != nil {
								return
							}
							captured = nil
							held.Store(false)
						} else {
							if _, err := client.Write(packet); err != nil {
								return
							}
						}
					}
				}()
				pipes.Wait()
			}()
		}
	}()
	t.Cleanup(func() {
		close(stop)
		unblock()
		_ = listener.Close()
		<-acceptDone
		conns.Range(func(key, value any) bool { _ = key.(net.Conn).Close(); return true })
		workers.Wait()
	})
	return uint16(listener.Addr().(*net.TCPAddr).Port), accepted, unblock
}

func TestNativeQuestDBLostOriginalWriteReceiptCannotReplay(t *testing.T) {
	p, password := nativeProfile(t)
	verifier := nativeSession(t, p, password)
	setup := nativeLease(t, verifier, 81)
	table := nativeTableName()
	nativeSQL(t, setup, "CREATE TABLE "+quote(table)+"(id LONG)")
	statement := "INSERT INTO " + quote(table) + " VALUES(9007199254740993)"
	port, held, release := holdOriginalPGReceipt(t, net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))), statement)
	p.Port = port
	s := nativeSession(t, p, password)
	original := nativeLease(t, s, 82)
	if err := original.Preconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	type completion struct {
		result contract.SQLResult
		err    error
	}
	done := make(chan completion, 1)
	go func() { r, e := original.Execute(ctx, "qdb", statement); done <- completion{r, e} }()
	var frames []byte
	select {
	case frames = <-held:
	case <-time.After(3 * time.Second):
		t.Fatal("original native terminal frames were not captured")
	}
	if !bytes.Contains(frames, []byte("INSERT 0 1")) {
		t.Fatal("proxy did not retain original complete write acknowledgement")
	}
	var out completion
	select {
	case out = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("original driver did not acknowledge cancellation/connection termination")
	}
	if !errors.Is(out.err, contract.ErrSQLSessionLost) || len(out.result.Steps) != 1 || out.result.Steps[0].State != "uncertain" || out.result.Steps[0].SQL != statement {
		t.Fatal("lost original write receipt was falsely settled", out.err, out.result)
	}
	release()
	if _, err := original.Execute(context.Background(), "qdb", statement); !errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatal("uncertain original write admitted replay")
	}
	count, err := verifier.count(context.Background(), verifier.ProfileID(), "qdb", table)
	if err != nil || count != 1 {
		t.Fatal("original write was lost or replayed", err, count)
	}
	nativeEvidence(t, "lost-original-write", struct {
		OriginalFrames []byte
		ObservedRows   int64
		Result         contract.SQLResult
	}{frames, count, out.result})
}

func TestNativeQuestDBQueuedCancellationDoesNotDispatchOrLoseOriginalSession(t *testing.T) {
	p, password := nativeProfile(t)
	verifier := nativeSession(t, p, password)
	setup := nativeLease(t, verifier, 92)
	table := nativeTableName()
	nativeSQL(t, setup, "CREATE TABLE "+quote(table)+"(id LONG)")
	heldSQL := "SELECT 812 AS original_held"
	port, held, release := holdOriginalPGReceipt(t, net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))), heldSQL)
	p.Port = port
	s := nativeSession(t, p, password)
	original := nativeLease(t, s, 93)
	if err := original.Preconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	type completion struct {
		result contract.SQLResult
		err    error
	}
	first := make(chan completion, 1)
	go func() { r, e := original.Execute(context.Background(), "qdb", heldSQL); first <- completion{r, e} }()
	select {
	case <-held:
	case <-time.After(3 * time.Second):
		t.Fatal("original read acknowledgement was not captured")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	queued := make(chan completion, 1)
	go func() {
		r, e := original.Execute(ctx, "qdb", "INSERT INTO "+quote(table)+" VALUES(99)")
		queued <- completion{r, e}
	}()
	release()
	select {
	case out := <-first:
		if out.err != nil || out.result.Sets[0].Rows[0][0].Text != "812" {
			t.Fatal("original read did not settle", out.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("original read remained pending")
	}
	var out completion
	select {
	case out = <-queued:
	case <-time.After(3 * time.Second):
		t.Fatal("queued cancellation remained pending")
	}
	if !errors.Is(out.err, context.Canceled) || len(out.result.Steps) != 0 || original.(*queryLease).CheckQueryLease() != nil {
		t.Fatal("undispatched cancellation lost original session", out.err, out.result)
	}
	if r := nativeSQL(t, original, "SELECT count(*) FROM "+quote(table)); r.Sets[0].Rows[0][0].Text != "0" {
		t.Fatal("cancelled queued write was dispatched")
	}
	nativeEvidence(t, "queued-original-cancel", out.result)
}

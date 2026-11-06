package redis

import (
	"bufio"
	"context"
	"errors"
	"github.com/nyakang/nyadb/internal/services/connections"
	redisdriver "github.com/redis/go-redis/v9"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestRedisValueIdentityAndStreamPreservation(t *testing.T) {
	for kind, text := range map[string]string{"string": `{"string":"AP8="}`, "hash": `{"hash":[["Yg==","Mg=="],["YQ==","MQ=="]]}`, "list": `{"list":["YQ==","AP8="]}`, "set": `{"set":["Yg==","YQ=="]}`, "zset": `{"zset":[{"member":"YQ==","score":"1.5"}]}`, "stream": `{"stream":[{"id":"1-0","fields":[["Zg==","dg=="]]}]}`} {
		v, e := decodeValue(kind, text)
		if e != nil {
			t.Fatal(kind, e)
		}
		if _, e = decodeValue(kind, encodeValue(v)); e != nil {
			t.Fatal(kind, e)
		}
	}
	for _, text := range []string{`{"set":["YQ==","YQ=="]}`, `{"set":[]}`, `{"set":["!"]}`} {
		if _, e := decodeValue("set", text); e == nil {
			t.Fatal("invalid set accepted")
		}
	}
	s := &Session{}
	s.key[0] = 1
	o := contract.ObjectRef{ProfileID: 1, Catalog: "0", Kind: "keyspace", Name: "keys"}
	r := keyRecord{key: "\x00\xff", kind: "string", dump: "bytes", value: `{"string":"YQ=="}`, expiry: -1, complete: true}
	snap := s.snapshot(o, r)
	if e := s.validateSnapshot(o, snap); e != nil || !matches(r, snap, true) {
		t.Fatal("valid snapshot rejected", e)
	}
	r.expiry = 10
	if matches(r, snap, true) {
		t.Fatal("expiry conflict ignored")
	}
	snap.Ref.Values[0][0] = 1
	if e := s.validateSnapshot(o, snap); e == nil {
		t.Fatal("forged identity accepted")
	}
	original := `{"stream":[{"id":"1-0","fields":[["Zg==","dg=="]]}]}`
	desired, _ := decodeValue("stream", `{"stream":[{"id":"2-0","fields":[["Zg==","eA=="]]}]}`)
	commands, e := makeCommands("temp", keyRecord{kind: "stream", dump: "serialized groups", value: original}, desired, true)
	if e != nil || commands[0][0] != "RESTORE" || commands[0][3] != "serialized groups" || len(commands) != 3 {
		t.Fatal("stream state not preserved", e)
	}
	desired, _ = decodeValue("stream", `{"stream":[{"id":"1-0","fields":[["Zg==","eA=="]]}]}`)
	if _, e = makeCommands("temp", keyRecord{kind: "stream", dump: "bytes", value: original}, desired, true); e == nil {
		t.Fatal("stream entry rewrite accepted")
	}
}
func TestRedisReplyBounds(t *testing.T) {
	for _, raw := range []string{"$1048577\r\n", "*1001\r\n", "$-2\r\n", "?bad\r\n"} {
		budget := 1 << 20
		if _, e := readReply(bufio.NewReader(strings.NewReader(raw)), &budget, 0); e == nil {
			t.Fatal("unbounded reply accepted")
		}
	}
	budget := 100
	v, e := readReply(bufio.NewReader(strings.NewReader("*2\r\n$2\r\n\x00\xff\r\n:1\r\n")), &budget, 0)
	if e != nil || v.([]any)[0].(string) != "\x00\xff" {
		t.Fatal("binary reply damaged", e)
	}
}

func TestRedisValueRejectsAmbiguousFieldPairs(t *testing.T) {
	for _, text := range []string{`{"hash":[["YQ=="]]}`, `{"hash":[["YQ==","Yg==","Yw=="]]}`, `{"hash":[["YQ==",null]]}`, `{"hash":[["YQ==","Yg=="]],"list":null}`} {
		if _, e := decodeValue("hash", text); e == nil {
			t.Fatal("ambiguous hash accepted")
		}
	}
	if _, e := decodeValue("stream", `{"stream":[{"id":"1-0","fields":[["YQ=="]]}]}`); e == nil {
		t.Fatal("ambiguous stream accepted")
	}
}

func TestRedisLargeListsBuildBoundedCommands(t *testing.T) {
	list := make([]string, 2000)
	for i := range list {
		list[i] = "YQ=="
	}
	commands, e := makeCommands("temp", keyRecord{kind: "list"}, valueDocument{List: list}, true)
	if e != nil || len(commands) != 8 {
		t.Fatal("list was not split into bounded commands", e)
	}
	count := 0
	for _, c := range commands {
		if len(c) > 258 {
			t.Fatal("oversized Lua command")
		}
		count += len(c) - 2
	}
	if count != 2000 {
		t.Fatal("list members lost")
	}
}

func TestRedisPreconnectCancellationClosesHandshake(t *testing.T) {
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, e := listener.Accept()
		if e != nil {
			return
		}
		defer c.Close()
		io.Copy(io.Discard, c)
	}()
	p, _ := connections.Defaults(connections.Redis)
	p.ID = 1
	sessionCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &Session{profile: p, options: redisdriver.Options{Addr: listener.Addr().String(), Password: "fixture-only", DialTimeout: 10 * time.Second}, ctx: sessionCtx, cancel: cancel, leases: map[*queryLease]bool{}}
	lease, e := s.ReserveQuery(1)
	if e != nil {
		t.Fatal(e)
	}
	defer lease.Release()
	ctx, stop := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer stop()
	start := time.Now()
	if e = lease.Preconnect(ctx); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal("handshake did not cancel", e)
	}
	if time.Since(start) > time.Second {
		t.Fatal("canceled handshake waited for the connection timeout")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handshake socket was retained")
	}
}

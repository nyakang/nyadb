package redis

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/rediscommand"
	redisdriver "github.com/redis/go-redis/v9"
)

// The peer owns no database. It returns an explicit redirect or drops an EVAL
// reply, so the test can distinguish a safe redirect from an ambiguous send.
func topologyPeer(t *testing.T, eval func(net.Conn, []any, bool)) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				r := bufio.NewReader(c)
				asking := false
				for {
					budget := 1 << 20
					value, err := readReply(r, &budget, 0)
					if err != nil {
						return
					}
					args, ok := value.([]any)
					if !ok || len(args) == 0 {
						return
					}
					switch strings.ToUpper(args[0].(string)) {
					case "HELLO":
						fmt.Fprint(c, "-ERR unknown command\r\n")
					case "ASKING":
						asking = true
						fmt.Fprint(c, "+OK\r\n")
					case "EVAL":
						eval(c, args, asking)
						asking = false
					case "CLUSTER":
						_, port, _ := net.SplitHostPort(listener.Addr().String())
						n, _ := strconv.Atoi(port)
						fmt.Fprintf(c, "*1\r\n*3\r\n:0\r\n:16383\r\n*3\r\n$9\r\n127.0.0.1\r\n:%d\r\n$5\r\nowner\r\n", n)
					default:
						fmt.Fprint(c, "+OK\r\n")
					}
				}
			}()
		}
	}()
	t.Cleanup(func() { listener.Close(); workers.Wait() })
	return listener.Addr().String()
}

func TestRedisClusterUnresponsiveSeedAllowsHealthyFallback(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, e := listener.Accept()
		if e != nil {
			return
		}
		defer c.Close()
		_, _ = io.Copy(io.Discard, c)
	}()
	t.Cleanup(func() { listener.Close(); <-done })
	healthy := topologyPeer(t, func(net.Conn, []any, bool) {})
	o := redisdriver.Options{Protocol: 2, MaxRetries: -1, DisableIdentity: true, PoolSize: 1, DialTimeout: 3 * time.Second, ReadTimeout: 3 * time.Second, ContextTimeoutEnabled: true}
	top := &topology{mode: "cluster", options: o, seeds: []string{listener.Addr().String(), healthy}, nodes: map[string]*redisdriver.Client{}, sentinels: map[string]*redisdriver.SentinelClient{}}
	t.Cleanup(func() { top.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ranges, err := top.refresh(ctx)
	if err != nil || len(ranges) != 1 || ranges[0].Addr != healthy {
		t.Fatal("dead seed exhausted the entire discovery timeout", err)
	}
}

func TestRedisClusterNetworkErrorNeverReplaysEval(t *testing.T) {
	var sends atomic.Int32
	addr := topologyPeer(t, func(c net.Conn, _ []any, _ bool) { sends.Add(1); c.Close() })
	o := redisdriver.Options{Addr: addr, Protocol: 2, MaxRetries: -1, DisableIdentity: true, PoolSize: 1, DialTimeout: time.Second}
	top := &topology{mode: "cluster", options: o, nodes: map[string]*redisdriver.Client{}, sentinels: map[string]*redisdriver.SentinelClient{}, ranges: []clusterRange{{0, 16383, "owner", addr}}}
	s := &Session{topology: top}
	t.Cleanup(func() { top.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := s.keyEval(ctx, nil, `return redis.call('INCR',KEYS[1])`, []string{"counter"}); err == nil {
		t.Fatal("dropped reply reported success")
	}
	if got := sends.Load(); got != 1 {
		t.Fatalf("ambiguous EVAL was sent %d times", got)
	}
}

func TestRedisClusterASKUsesTheEvalConnection(t *testing.T) {
	var sends atomic.Int32
	target := topologyPeer(t, func(c net.Conn, _ []any, asking bool) {
		sends.Add(1)
		if !asking {
			fmt.Fprint(c, "-ERR ASKING missing on this socket\r\n")
			return
		}
		fmt.Fprint(c, ":7\r\n")
	})
	key := "{routing}:counter"
	source := topologyPeer(t, func(c net.Conn, _ []any, _ bool) { fmt.Fprintf(c, "-ASK %d %s\r\n", rediscommand.Slot(key), target) })
	o := redisdriver.Options{Addr: source, Protocol: 2, MaxRetries: -1, DisableIdentity: true, PoolSize: 1, DialTimeout: time.Second}
	top := &topology{mode: "cluster", options: o, nodes: map[string]*redisdriver.Client{}, sentinels: map[string]*redisdriver.SentinelClient{}, ranges: []clusterRange{{0, 16383, "owner", source}}}
	s := &Session{topology: top}
	t.Cleanup(func() { top.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	value, err := s.keyEval(ctx, nil, `return redis.call('INCR',KEYS[1])`, []string{key})
	if err != nil || value != int64(7) || sends.Load() != 1 {
		t.Fatal("ASK did not route one EVAL", value, err)
	}
}

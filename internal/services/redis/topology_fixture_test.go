package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/rediscommand"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func topologyFixture(t *testing.T, mode string) (*Session, context.Context) {
	t.Helper()
	if os.Getenv("NYADB_REDIS_TOPOLOGY_IT") != "1" {
		t.Skip("requires explicitly enabled disposable topology fixtures")
	}
	ports := strings.Split(os.Getenv("NYADB_REDIS_CLUSTER_PORTS"), ",")
	if mode == "sentinel" {
		ports = strings.Split(os.Getenv("NYADB_REDIS_SENTINEL_PORTS"), ",")
	}
	p, _ := connections.Defaults(connections.Redis)
	p.ID, p.Name, p.Host, p.Username = 91, "Topology fixture", "127.0.0.1", "nyadb"
	p.RedisOptions.Topology = mode
	for i, port := range ports {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n < 1024 {
			t.Fatal("explicit loopback ports required")
		}
		if i == 0 {
			p.Port = uint16(n)
		} else {
			if p.RedisOptions.Seeds != "" {
				p.RedisOptions.Seeds += ","
			}
			p.RedisOptions.Seeds += "127.0.0.1:" + port
		}
	}
	if mode == "sentinel" {
		p.Database = "3"
		p.RedisOptions.MasterName = "mymaster"
		p.RedisOptions.SentinelUsername = "monitor"
	}
	if os.Getenv("NYADB_REDIS_TOPOLOGY_TLS") == "1" {
		p.TLSMode = "verify-full"
		p.RedisOptions.CAFile = os.Getenv("NYADB_REDIS_CA")
		if mode == "sentinel" {
			p.RedisOptions.SentinelTLSMode = "verify-full"
			p.RedisOptions.SentinelCAFile = p.RedisOptions.CAFile
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	ctx = connections.WithRedisSentinelPassword(ctx, os.Getenv("NYADB_REDIS_SENTINEL_PASSWORD"))
	var session contract.Session
	var err error
	for until := time.Now().Add(15 * time.Second); time.Now().Before(until); {
		session, err = OpenSession(ctx, p, os.Getenv("NYADB_REDIS_PASSWORD"))
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	s := session.(*Session)
	t.Cleanup(func() { s.Close() })
	return s, ctx
}

func topologyInsert(t *testing.T, s *Session, ctx context.Context, db, key string) {
	t.Helper()
	r, err := s.save(ctx, s.ProfileID(), db, "keys", []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"key": {Text: b64(key)}, "type": {Text: "string"}, "value": {Text: `{"string":"dmFsdWU="}`}, "ttl_ms": {Text: "60000"}}}})
	if err != nil || !r.CommitConfirmed {
		t.Fatal("insert", err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, done, c, err := s.request(cleanup, db)
		if err != nil {
			return
		}
		defer done()
		_, _ = s.keyEval(cleanup, c, `return redis.call('DEL',KEYS[1])`, []string{key})
	})
}

func topologyCommand(t *testing.T, l contract.QueryLease, ctx context.Context, db string, args ...string) contract.SQLResult {
	t.Helper()
	raw, _ := json.Marshal(args)
	r, err := l.Execute(ctx, db, string(raw))
	if err != nil {
		t.Fatalf("%s: %v", args[0], err)
	}
	return r
}

func TestRedisTopologyClusterRoutingGridAndTransactions(t *testing.T) {
	s, ctx := topologyFixture(t, "cluster")
	fallback := s.profile
	fallback.Port = 1
	if session, e := OpenSession(ctx, fallback, os.Getenv("NYADB_REDIS_PASSWORD")); e != nil {
		t.Fatal("Cluster seed fallback", e)
	} else {
		session.Close()
	}
	ns, err := s.Namespaces(ctx)
	if err != nil || len(ns) != 1 || ns[0].Ref.Catalog != "0" {
		t.Fatal("cluster database scope", err)
	}
	prefix := fmt.Sprintf("nyadb_cluster_%d_", time.Now().UnixNano())
	for i := range 36 {
		key := fmt.Sprintf("%s%d", prefix, i)
		if i == 3 {
			key += "\x00\xff"
		}
		topologyInsert(t, s, ctx, "0", key)
	}
	seen := map[string]bool{}
	options := contract.DataOptions{Limit: 5, FilterColumn: "key", FilterMode: "contains", FilterValue: b64(prefix)}
	for i := 0; i < 100; i++ {
		p, err := s.page(ctx, s.ProfileID(), "0", "keys", options)
		if err != nil {
			t.Fatal(err)
		}
		for _, ref := range p.RowRefs {
			seen[string(ref.Values[0])] = true
		}
		if !p.HasMore {
			break
		}
		options.After = p.NextCursor
	}
	if len(seen) != 36 {
		t.Fatalf("multi-master SCAN omitted keys: %d", len(seen))
	}
	if n, err := s.count(ctx, s.ProfileID(), "0", "keys"); err != nil || n < 36 {
		t.Fatal("cluster count", n, err)
	}
	l, err := s.ReserveQuery(0)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	key := "{" + prefix + "tx}:one"
	topologyCommand(t, l, ctx, "0", "MULTI")
	topologyCommand(t, l, ctx, "0", "PING") // buffered until a key selects its owner
	topologyCommand(t, l, ctx, "0", "SET", key, "once")
	topologyCommand(t, l, ctx, "0", "INCR", "{"+prefix+"tx}:counter")
	topologyCommand(t, l, ctx, "0", "EXEC")
	topologyCommand(t, l, ctx, "0", "DEL", key, "{"+prefix+"tx}:counter")
	if _, err = l.Execute(ctx, "0", "MGET a b"); err == nil {
		t.Fatal("cross-slot command accepted")
	}
	topologyCommand(t, l, ctx, "0", "MULTI")
	topologyCommand(t, l, ctx, "0", "SET", key, "must-not-apply")
	if _, err = l.Execute(ctx, "0", "SET other-slot should-not-apply"); err == nil {
		t.Fatal("cross-slot transaction accepted")
	}
	if _, err = l.Execute(ctx, "0", "EXEC"); err == nil {
		t.Fatal("rejected transaction executed its earlier writes")
	}
	if reply := topologyCommand(t, l, ctx, "0", "EXISTS", key); reply.Rows[0][0] != "0" {
		t.Fatal("aborted transaction mutated its key")
	}
	if _, err = l.Execute(ctx, "1", "PING"); err == nil {
		t.Fatal("nonzero cluster database accepted")
	}
	key = prefix + "0"
	_, done, c, err := s.request(ctx, "0")
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	r, err := s.read(ctx, c, key)
	if err != nil {
		t.Fatal(err)
	}
	other := key + "renamed"
	for rediscommand.Slot(other) == rediscommand.Slot(key) {
		other += "x"
	}
	if _, err = s.save(ctx, s.ProfileID(), "0", "keys", []contract.DataEdit{{Kind: "update", Original: s.snapshot(s.Namespace("0").Object("keyspace", "keys"), r), Values: map[string]contract.DataValue{"key": {Text: b64(other)}}}}); err == nil {
		t.Fatal("cross-slot grid rename accepted")
	}
}

func TestRedisTopologyClusterTypedEditsAndSameSlotRename(t *testing.T) {
	s, ctx := topologyFixture(t, "cluster")
	_, done, c, err := s.request(ctx, "0")
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	object := s.Namespace("0").Object("keyspace", "keys")
	prefix := fmt.Sprintf("{nyadb_types_%d}:", time.Now().UnixNano())
	for _, tc := range []struct{ kind, value, updated string }{
		{"string", `{"string":"AP8="}`, `{"string":"dmFsdWU="}`},
		{"hash", `{"hash":[["AP8=","dg=="]]}`, `{"hash":[["AP8=","eA=="]]}`},
		{"list", `{"list":["YQ==","Yg=="]}`, `{"list":["AP8=","Yg=="]}`},
		{"set", `{"set":["YQ==","Yg=="]}`, `{"set":["AP8="]}`},
		{"zset", `{"zset":[{"member":"AP8=","score":"1.5"}]}`, `{"zset":[{"member":"AP8=","score":"2.5"}]}`},
		{"stream", `{"stream":[{"id":"1-0","fields":[["AP8=","dg=="]]}]}`, `{"stream":[{"id":"2-0","fields":[["AP8=","eA=="]]}]}`},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			key := prefix + tc.kind + "\x00\xff"
			insert := contract.DataEdit{Kind: "insert", Values: map[string]contract.DataValue{"key": {Text: b64(key)}, "type": {Text: tc.kind}, "value": {Text: tc.value}, "ttl_ms": {Text: "60000"}}}
			result, e := s.save(ctx, s.ProfileID(), "0", "keys", []contract.DataEdit{insert})
			if e != nil || !result.CommitConfirmed {
				t.Fatal(e)
			}
			t.Cleanup(func() {
				cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				_, _ = s.keyEval(cleanup, c, `return redis.call('DEL',KEYS[1],KEYS[2])`, []string{key, key + "renamed"})
			})
			if tc.kind == "stream" {
				if _, e = s.keyEval(ctx, c, `return redis.call('XGROUP','CREATE',KEYS[1],'readers','0')`, []string{key}); e != nil {
					t.Fatal(e)
				}
			}
			r, e := s.read(ctx, c, key)
			if e != nil || !r.complete {
				t.Fatal(e)
			}
			expiry := r.expiry
			result, e = s.save(ctx, s.ProfileID(), "0", "keys", []contract.DataEdit{{Kind: "update", Original: s.snapshot(object, r), Values: map[string]contract.DataValue{"value": {Text: tc.updated}}}})
			if e != nil || !result.CommitConfirmed {
				t.Fatal("typed update", e)
			}
			r, e = s.read(ctx, c, key)
			if e != nil || r.expiry != expiry {
				t.Fatal("typed edit changed absolute expiry", e)
			}
			result, e = s.save(ctx, s.ProfileID(), "0", "keys", []contract.DataEdit{{Kind: "update", Original: s.snapshot(object, r), Values: map[string]contract.DataValue{"key": {Text: b64(key + "renamed")}}}})
			if e != nil || !result.CommitConfirmed {
				t.Fatal("same-slot rename", e)
			}
			verification, e := s.verify(ctx, s.ProfileID(), "0", "keys", result)
			if e != nil || verification.Status != "applied" {
				t.Fatal("rename verification", e)
			}
			if tc.kind == "stream" {
				groups, e := s.keyEval(ctx, c, `return redis.call('XINFO','GROUPS',KEYS[1])`, []string{key + "renamed"})
				if e != nil || len(groups.([]any)) != 1 {
					t.Fatal("stream rename lost consumer group", e)
				}
			}
		})
	}
}

func TestRedisTopologyClusterASKMOVEDAndFailover(t *testing.T) {
	s, ctx := topologyFixture(t, "cluster")
	ranges, err := s.topology.refresh(ctx)
	if err != nil {
		t.Fatal(err)
	}
	source := ranges[0]
	var target clusterRange
	for _, r := range ranges {
		if r.Addr != source.Addr {
			target = r
			break
		}
	}
	c, _ := s.topology.client(source.Addr, 0)
	dest, _ := s.topology.client(target.Addr, 0)
	slot := source.Start
	key := "{" + rediscommand.SlotTag(slot) + "}:nyadb_migration"
	if err = dest.Do(ctx, "CLUSTER", "SETSLOT", slot, "IMPORTING", source.ID).Err(); err != nil {
		t.Fatal(err)
	}
	if err = c.Do(ctx, "CLUSTER", "SETSLOT", slot, "MIGRATING", target.ID).Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_, _ = s.keyEval(cleanup, c, `return redis.call('DEL',KEYS[1])`, []string{key})
		_ = c.Do(cleanup, "CLUSTER", "SETSLOT", slot, "STABLE").Err()
		_ = dest.Do(cleanup, "CLUSTER", "SETSLOT", slot, "STABLE").Err()
		for _, node := range ranges {
			client, _ := s.topology.client(node.Addr, 0)
			_ = client.Do(cleanup, "CLUSTER", "SETSLOT", slot, "NODE", source.ID).Err()
		}
	})
	if _, err = s.keyEval(ctx, c, `return redis.call('SET',KEYS[1],'probe')`, []string{key}); err != nil {
		t.Fatal("ASK probe", err)
	}
	if _, err = s.keyEval(ctx, c, `return redis.call('DEL',KEYS[1])`, []string{key}); err != nil {
		t.Fatal(err)
	}
	// Redis can refuse multi-key scripts while a slot is migrating. Single-key
	// reads/commands must still follow ASK without replaying ambiguous errors.
	if _, err = s.keyEval(ctx, c, `return redis.call('SET',KEYS[1],'value')`, []string{key}); err != nil {
		t.Fatal(err)
	}
	l, err := s.ReserveQuery(0)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	topologyCommand(t, l, ctx, "0", "GET", key) // native ASK routing
	masters, err := s.topology.masters(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range masters {
		client, _ := s.topology.client(node.Addr, 0)
		if err = client.Do(ctx, "CLUSTER", "SETSLOT", slot, "NODE", target.ID).Err(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.keyEval(ctx, c, `return redis.call('DEL',KEYS[1])`, []string{key}); err != nil {
		t.Fatal(err)
	}
	topologyInsert(t, s, ctx, "0", key)
	// Deliberately retain an old slot map: only MOVED permits resending EVAL.
	s.topology.mu.Lock()
	s.topology.ranges = append([]clusterRange(nil), ranges...)
	s.topology.mu.Unlock()
	if _, err = s.keyEval(ctx, c, `return redis.call('GET',KEYS[1])`, []string{key}); err != nil {
		t.Fatal("MOVED routing", err)
	}
	// Watch another key and promote its replica. EXEC must revoke the old lease.
	watchKey := "{" + rediscommand.SlotTag(source.End) + "}:nyadb_watch"
	topologyInsert(t, s, ctx, "0", watchKey)
	for i := range 3 {
		topologyInsert(t, s, ctx, "0", fmt.Sprintf("%s%d", watchKey, i))
	}
	cursorOptions := contract.DataOptions{Limit: 1, FilterColumn: "key", FilterMode: "contains", FilterValue: b64(watchKey)}
	before, err := s.page(ctx, s.ProfileID(), "0", "keys", cursorOptions)
	if err != nil || before.NextCursor == nil {
		t.Fatal("missing pre-failover cursor", err)
	}
	cursorOptions.After = before.NextCursor
	topologyCommand(t, l, ctx, "0", "WATCH", watchKey)
	var replica string
	for until := time.Now().Add(15 * time.Second); time.Now().Before(until); {
		slots, e := c.ClusterSlots(ctx).Result()
		if e != nil {
			t.Fatal(e)
		}
		for _, r := range slots {
			if r.Start <= rediscommand.Slot(watchKey) && r.End >= rediscommand.Slot(watchKey) && len(r.Nodes) > 1 {
				replica = r.Nodes[1].Addr
				break
			}
		}
		if replica != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if replica == "" {
		t.Fatal("cluster replica fixture missing")
	}
	replicaClient, err := s.topology.client(replica, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = replicaClient.ClusterFailover(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	changed := false
	for until := time.Now().Add(15 * time.Second); time.Now().Before(until); {
		_, _ = s.topology.refresh(ctx)
		addr, e := s.topology.slot(ctx, rediscommand.Slot(watchKey))
		if e == nil && addr == replica {
			changed = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !changed {
		t.Fatal("replica was not promoted")
	}
	if _, err = s.page(ctx, s.ProfileID(), "0", "keys", cursorOptions); err == nil {
		t.Fatal("Cluster accepted a cursor from before owner change")
	}
	if _, err = l.Execute(ctx, "0", "EXEC"); !errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatal("transaction survived owner change", err)
	}
	fresh, err := l.Replace()
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Release()
	if reply := topologyCommand(t, fresh, ctx, "0", "GET", watchKey); reply.Rows[0][0] != `"value"` {
		t.Fatal("promoted Cluster replica lost the committed key")
	}
}

func TestRedisTopologySentinelDatabaseCredentialsAndFailover(t *testing.T) {
	s, ctx := topologyFixture(t, "sentinel")
	fallback := s.profile
	fallback.Port = 1
	if session, e := OpenSession(ctx, fallback, os.Getenv("NYADB_REDIS_PASSWORD")); e != nil {
		t.Fatal("Sentinel seed fallback", e)
	} else {
		session.Close()
	}
	if s.profile.TLSMode == "verify-full" {
		bad := s.profile
		bad.RedisOptions.SentinelServerName = "wrong.invalid"
		if session, e := OpenSession(ctx, bad, os.Getenv("NYADB_REDIS_PASSWORD")); e == nil {
			session.Close()
			t.Fatal("Sentinel hostname mismatch accepted")
		}
		bad = s.profile
		bad.RedisOptions.ServerName = "wrong.invalid"
		if session, e := OpenSession(ctx, bad, os.Getenv("NYADB_REDIS_PASSWORD")); e == nil {
			session.Close()
			t.Fatal("Redis hostname mismatch accepted")
		}
	}
	badCtx := connections.WithRedisSentinelPassword(ctx, "incorrect-sentinel-password")
	if session, err := OpenSession(badCtx, s.profile, os.Getenv("NYADB_REDIS_PASSWORD")); err == nil {
		session.Close()
		t.Fatal("incorrect Sentinel credential accepted")
	}
	key := fmt.Sprintf("nyadb_sentinel_%d", time.Now().UnixNano())
	topologyInsert(t, s, ctx, "3", key)
	topologyInsert(t, s, ctx, "3", key+"extra")
	cursorOptions := contract.DataOptions{Limit: 1, FilterColumn: "key", FilterMode: "contains", FilterValue: b64(key)}
	before, e := s.page(ctx, s.ProfileID(), "3", "keys", cursorOptions)
	if e != nil || before.NextCursor == nil {
		t.Fatal("missing pre-failover Sentinel cursor", e)
	}
	cursorOptions.After = before.NextCursor
	p, err := s.page(ctx, s.ProfileID(), "3", "keys", contract.DataOptions{Limit: 5, FilterColumn: "key", FilterMode: "equal", FilterValue: b64(key)})
	if err != nil || len(p.Rows) != 1 {
		t.Fatal("Sentinel SCAN selected wrong logical database", err)
	}
	l, err := s.ReserveQuery(0)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	topologyCommand(t, l, ctx, "3", "SET", key, "replicated-value")
	reply := topologyCommand(t, l, ctx, "3", "WAIT", "1", "5000")
	if reply.Rows[0][0] != "1" {
		t.Fatal("Sentinel replica fixture was not synchronized")
	}
	topologyCommand(t, l, ctx, "3", "WATCH", key)
	old, err := s.topology.master(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sc, err := s.topology.sentinel(s.topology.seeds[0])
	if err != nil {
		t.Fatal(err)
	}
	for until := time.Now().Add(15 * time.Second); time.Now().Before(until); {
		err = sc.Failover(ctx, s.profile.RedisOptions.MasterName).Err()
		if err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	changed := false
	for until := time.Now().Add(20 * time.Second); time.Now().Before(until); {
		addr, e := s.topology.master(ctx)
		if e == nil && addr != old {
			changed = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !changed {
		t.Fatal("Sentinel did not discover promoted master")
	}
	if _, err = s.page(ctx, s.ProfileID(), "3", "keys", cursorOptions); err == nil {
		t.Fatal("Sentinel accepted a cursor from the former master")
	}
	if _, err = l.Execute(ctx, "3", "EXEC"); !errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatal("old command session was replayed on promoted master", err)
	}
	fresh, err := l.Replace()
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Release()
	if reply := topologyCommand(t, fresh, ctx, "3", "GET", key); reply.Rows[0][0] != `"replicated-value"` {
		t.Fatal("promoted Sentinel master lost the replicated key")
	}
	p, err = s.page(ctx, s.ProfileID(), "3", "keys", contract.DataOptions{Limit: 5, FilterColumn: "key", FilterMode: "equal", FilterValue: b64(key)})
	if err != nil || len(p.Rows) != 1 {
		t.Fatal("browser failed to follow promoted master", err)
	}
	if n, err := s.count(ctx, s.ProfileID(), "3", "keys"); err != nil || n < 1 {
		t.Fatal("promoted master count", err)
	}
}

package redis

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func fixture(t *testing.T) (*Session, context.Context) {
	t.Helper()
	if os.Getenv("NYADB_REDIS_IT") != "1" {
		t.Skip("requires an explicitly enabled disposable Redis fixture")
	}
	port, e := strconv.Atoi(os.Getenv("NYADB_REDIS_PORT"))
	if e != nil || port < 1024 || port > 65535 {
		t.Fatal("explicit loopback port required")
	}
	p, _ := connections.Defaults(connections.Redis)
	p.ID = 89
	p.Name = "Redis fixture"
	p.Host = "127.0.0.1"
	p.Port = uint16(port)
	p.Username = os.Getenv("NYADB_REDIS_USERNAME")
	if os.Getenv("NYADB_REDIS_TLS") == "1" {
		p.TLSMode = "verify-full"
		p.RedisOptions.CAFile = os.Getenv("NYADB_REDIS_CA")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	session, e := OpenSession(ctx, p, os.Getenv("NYADB_REDIS_PASSWORD"))
	if e != nil {
		t.Fatal(e)
	}
	s := session.(*Session)
	t.Cleanup(func() { s.Close() })
	return s, ctx
}
func TestRedisFixtureTypesCASExpiryAndStreams(t *testing.T) {
	s, ctx := fixture(t)
	o := s.Namespace("0").Object("keyspace", "keys")
	prefix := fmt.Sprintf("nyadb_%d_", time.Now().UnixNano())
	_, cancel, c, e := s.request(ctx, "0")
	if e != nil {
		t.Fatal(e)
	}
	defer cancel()
	cases := []struct{ kind, text, updated string }{
		{"string", `{"string":"AP8="}`, `{"string":"YWZ0ZXI="}`},
		{"hash", `{"hash":[["AP8=","dg=="]]}`, `{"hash":[["AP8=","eA=="]]}`},
		{"list", `{"list":["YQ==","Yg=="]}`, `{"list":["Yg==","YQ==","AP8="]}`},
		{"set", `{"set":["Yg==","YQ=="]}`, `{"set":["Yw=="]}`},
		{"zset", `{"zset":[{"member":"AP8=","score":"1.5"},{"member":"aW5m","score":"+inf"}]}`, `{"zset":[{"member":"AP8=","score":"2.5"}]}`},
		{"stream", `{"stream":[{"id":"1-0","fields":[["AP8=","dg=="]]}]}`, `{"stream":[{"id":"2-0","fields":[["AP8=","eA=="]]}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			key := prefix + tc.kind + "\x00\xff"
			t.Cleanup(func() { c.Del(context.Background(), key, key+"new") })
			insert := contract.DataEdit{Kind: "insert", Values: map[string]contract.DataValue{"key": {Text: b64(key)}, "type": {Text: tc.kind}, "ttl_ms": {Text: "60000"}, "value": {Text: tc.text, Kind: contract.JSON}}}
			result, e := s.save(ctx, s.ProfileID(), "0", "keys", []contract.DataEdit{insert})
			if e != nil || !result.CommitConfirmed {
				t.Fatal("insert", e)
			}
			if tc.kind == "stream" {
				if e = c.XGroupCreate(ctx, key, "readers", "0").Err(); e != nil {
					t.Fatal(e)
				}
			}
			r, e := s.read(ctx, c, key)
			if e != nil || !r.complete || r.kind != tc.kind {
				t.Fatal("read", e)
			}
			expiry := r.expiry
			snap := s.snapshot(o, r)
			result, e = s.save(ctx, s.ProfileID(), "0", "keys", []contract.DataEdit{{Kind: "update", Original: snap, Values: map[string]contract.DataValue{"value": {Text: tc.updated, Kind: contract.JSON}}}})
			if e != nil || !result.CommitConfirmed {
				t.Fatal("update", e)
			}
			r, e = s.read(ctx, c, key)
			if !matches(r, result.Outcomes[0].Saved, true) {
				t.Fatal("saved snapshot does not represent committed bytes")
			}
			if e != nil || r.expiry != expiry {
				t.Fatal("TTL changed", e)
			}
			if tc.kind == "stream" {
				groups, e := c.XInfoGroups(ctx, key).Result()
				if e != nil || len(groups) != 1 || groups[0].Name != "readers" {
					t.Fatal("consumer group lost", e)
				}
			}
			if _, e = s.save(ctx, s.ProfileID(), "0", "keys", []contract.DataEdit{{Kind: "delete", Original: snap}}); !errors.Is(e, contract.ErrDataConflict) {
				t.Fatal("concurrent modification ignored", e)
			}
			snap = s.snapshot(o, r)
			result, e = s.save(ctx, s.ProfileID(), "0", "keys", []contract.DataEdit{{Kind: "update", Original: snap, Values: map[string]contract.DataValue{"key": {Text: b64(key + "new")}}}})
			if e != nil || !result.CommitConfirmed {
				t.Fatal("rename", e)
			}
			v, e := s.verify(ctx, s.ProfileID(), "0", "keys", result)
			if e != nil || v.Status != "applied" {
				t.Fatal("verify rename", e)
			}
			r, e = s.read(ctx, c, key+"new")
			if e != nil {
				t.Fatal(e)
			}
			if _, e = s.save(ctx, s.ProfileID(), "0", "keys", []contract.DataEdit{{Kind: "delete", Original: s.snapshot(o, r)}}); e != nil {
				t.Fatal("delete", e)
			}
		})
	}
	// A malformed later edit is rejected before the batch sends any writes.
	key := prefix + "not_written"
	_, e = s.save(ctx, s.ProfileID(), "0", "keys", []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"key": {Text: b64(key)}, "type": {Text: "string"}, "value": {Text: `{"string":"eA=="}`}}}, {Kind: "insert", Values: map[string]contract.DataValue{"key": {Text: b64(key + "2")}, "type": {Text: "string"}, "value": {Text: `{bad}`}}}})
	if e == nil || c.Exists(ctx, key).Val() != 0 {
		t.Fatal("invalid batch changed live data")
	}
	page, e := s.page(ctx, s.ProfileID(), "0", "keys", contract.DataOptions{Limit: 2})
	if e != nil {
		t.Fatal(e)
	}
	if page.NextCursor != nil {
		bad := *page.NextCursor
		bad.RuleSignature = "forged"
		if _, e = s.page(ctx, s.ProfileID(), "0", "keys", contract.DataOptions{Limit: 2, After: &bad}); e == nil {
			t.Fatal("forged SCAN cursor accepted")
		}
	}
}
func TestRedisFixturePinnedCommandsCancellationAndRevocation(t *testing.T) {
	s, ctx := fixture(t)
	lease, e := s.ReserveQuery(1)
	if e != nil {
		t.Fatal(e)
	}
	defer lease.Release()
	if e = lease.Preconnect(ctx); e != nil {
		t.Fatal(e)
	}
	id1, e := lease.Execute(ctx, "0", "CLIENT ID")
	if e != nil {
		t.Fatal(e)
	}
	id2, e := lease.Execute(ctx, "0", "CLIENT ID")
	if e != nil || id1.Rows[0][0] != id2.Rows[0][0] {
		t.Fatal("physical connection changed", e)
	}
	key := fmt.Sprintf("nyadb_tx_%d", time.Now().UnixNano())
	_, cancel, c, e := s.request(ctx, "0")
	if e != nil {
		t.Fatal(e)
	}
	defer cancel()
	t.Cleanup(func() { c.Del(context.Background(), key) })
	for _, text := range []string{"MULTI", "SET " + key + " value", "EXEC"} {
		if _, e = lease.Execute(ctx, "0", text); e != nil {
			t.Fatal(e)
		}
	}
	if c.Get(ctx, key).Val() != "value" {
		t.Fatal("transaction did not execute")
	}
	short, stop := context.WithTimeout(ctx, 50*time.Millisecond)
	defer stop()
	if _, e = lease.Execute(short, "0", "BLPOP "+key+"_missing 0"); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal("blocking command did not cancel", e)
	}
	if _, e = lease.Execute(ctx, "0", "PING"); !errors.Is(e, contract.ErrSQLSessionLost) {
		t.Fatal("canceled connection was replayed", e)
	}
	fresh, e := lease.Replace()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = fresh.Execute(ctx, "0", "PING"); e != nil {
		t.Fatal(e)
	}
	if e = c.Del(ctx, key).Err(); e != nil {
		t.Fatal(e)
	}
	s.Close()
	if _, e = fresh.Execute(ctx, "0", "PING"); !errors.Is(e, contract.ErrRevoked) {
		t.Fatal("revoked lease executed", e)
	}
}

func TestRedisFixtureSCANLargeValuesAndAuthentication(t *testing.T) {
	s, ctx := fixture(t)
	ctx, cancel, c, e := s.request(ctx, "0")
	if e != nil {
		t.Fatal(e)
	}
	defer cancel()
	prefix := fmt.Sprintf("nyadb_scan_%d_", time.Now().UnixNano())
	keys := make([]string, 0, 121)
	t.Cleanup(func() { c.Del(context.Background(), keys...) })
	pipe := c.Pipeline()
	for i := range 120 {
		key := fmt.Sprintf("%s%d", prefix, i) + "\x00\xff"
		keys = append(keys, key)
		pipe.Set(ctx, key, "value", 0)
	}
	if _, e = pipe.Exec(ctx); e != nil {
		t.Fatal(e)
	}
	seen := map[string]bool{}
	o := contract.DataOptions{Limit: 7, FilterColumn: "key", FilterMode: "contains", FilterValue: b64(prefix)}
	for pages := 0; pages < 100; pages++ {
		p, e := s.page(ctx, s.ProfileID(), "0", "keys", o)
		if e != nil {
			t.Fatal(e)
		}
		for _, ref := range p.RowRefs {
			seen[string(ref.Values[0])] = true
		}
		if !p.HasMore {
			break
		}
		if p.NextCursor == nil {
			t.Fatal("missing SCAN cursor")
		}
		o.After = p.NextCursor
	}
	if len(seen) != 120 {
		t.Fatalf("SCAN omitted fixture keys: %d", len(seen))
	}
	bad := *o.After
	bad.RuleSignature = "forged"
	if _, e = s.page(ctx, s.ProfileID(), "0", "keys", contract.DataOptions{Limit: 7, After: &bad}); e == nil {
		t.Fatal("forged cursor accepted")
	}
	big := prefix + "large"
	keys = append(keys, big)
	if e = c.Set(ctx, big, string(make([]byte, 2<<20)), 0).Err(); e != nil {
		t.Fatal(e)
	}
	r, e := s.read(ctx, c, big)
	if e != nil || r.complete || len(r.value) > 4096 {
		t.Fatal("large key read was not bounded", e)
	}
	ref := contract.RowRef{Object: s.Namespace("0").Object("keyspace", "keys"), Database: "0", Table: "keys", Columns: []string{"key"}, Values: [][]byte{[]byte(big)}}
	for _, offset := range []int64{0, 9, 10, 11, 1024, 2800000, 9999999} {
		chunk, e := s.chunk(ctx, s.ProfileID(), "0", "keys", "value", ref, offset, 1024)
		if e != nil || len(chunk.Text) > 1024 || chunk.NextOffset > chunk.TotalUnits {
			t.Fatal("unbounded field chunk", e)
		}
	}
	if _, e = s.row(ctx, s.ProfileID(), "0", "keys", ref); e == nil {
		t.Fatal("oversized complete editor accepted")
	}
	p := s.profile
	if _, e = OpenSession(ctx, p, "incorrect-fixture-password"); e == nil {
		t.Fatal("wrong ACL password accepted")
	}
	if p.TLSMode == "verify-full" {
		p.RedisOptions.ServerName = "wrong.example.invalid"
		if _, e = OpenSession(ctx, p, os.Getenv("NYADB_REDIS_PASSWORD")); e == nil {
			t.Fatal("TLS hostname mismatch accepted")
		}
	}
}

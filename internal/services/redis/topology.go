package redis

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/rediscommand"
	"github.com/nyakang/nyadb/internal/services/connections"
	redisdriver "github.com/redis/go-redis/v9"
)

type clusterRange struct {
	Start, End int
	ID, Addr   string
}
type topology struct {
	mu                                 sync.Mutex
	closed                             bool
	mode                               string
	options                            redisdriver.Options
	seeds                              []string
	masterName                         string
	sentinelUsername, sentinelPassword string
	sentinelTLS                        *tls.Config
	nodes                              map[string]*redisdriver.Client
	sentinels                          map[string]*redisdriver.SentinelClient
	ranges                             []clusterRange
}
type scanNode struct {
	ID, Addr string
	Cursor   uint64
	Started  bool
}

func redisTLS(mode, ca, serverName string) (*tls.Config, error) {
	if mode != "verify-full" {
		return nil, nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName}
	if ca != "" {
		raw, e := os.ReadFile(ca)
		roots, e2 := x509.SystemCertPool()
		if e2 != nil {
			roots = x509.NewCertPool()
		}
		if e != nil || len(raw) > 1<<20 || !roots.AppendCertsFromPEM(raw) {
			return nil, errors.New("Redis CA certificate could not be loaded")
		}
		cfg.RootCAs = roots
	}
	return cfg, nil
}
func newTopology(p connections.Profile, o redisdriver.Options, secret string) (*topology, error) {
	a := p.RedisOptions
	cfg, e := redisTLS(a.SentinelTLSMode, a.SentinelCAFile, a.SentinelServerName)
	if e != nil {
		return nil, e
	}
	seeds := []string{o.Addr}
	if a.Seeds != "" {
		seeds = append(seeds, strings.Split(a.Seeds, ",")...)
	}
	unique := seeds[:0]
	seen := map[string]bool{}
	for _, addr := range seeds {
		if !seen[addr] {
			unique = append(unique, addr)
			seen[addr] = true
		}
	}
	seeds = unique
	return &topology{mode: a.Topology, options: o, seeds: seeds, masterName: a.MasterName, sentinelUsername: a.SentinelUsername, sentinelPassword: secret, sentinelTLS: cfg, nodes: map[string]*redisdriver.Client{}, sentinels: map[string]*redisdriver.SentinelClient{}}, nil
}
func (t *topology) client(addr string, db int) (*redisdriver.Client, error) {
	if !validNode(addr) {
		return nil, errors.New("invalid Redis node endpoint")
	}
	key := strconv.Itoa(db) + "/" + addr
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, contract.ErrRevoked
	}
	if c := t.nodes[key]; c != nil {
		return c, nil
	}
	if len(t.nodes) >= 256 {
		return nil, errors.New("Redis node pool limit reached")
	}
	o := t.options
	o.Addr = addr
	o.DB = db
	c := redisdriver.NewClient(&o)
	t.nodes[key] = c
	return c, nil
}
func validNode(addr string) bool {
	host, port, e := net.SplitHostPort(addr)
	n, e2 := strconv.ParseUint(port, 10, 16)
	return e == nil && e2 == nil && n > 0 && host != "" && !strings.ContainsAny(host, "@/\\?#! \t\r\n")
}
func (t *topology) sentinel(addr string) (*redisdriver.SentinelClient, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, contract.ErrRevoked
	}
	if c := t.sentinels[addr]; c != nil {
		return c, nil
	}
	o := t.options
	c := redisdriver.NewSentinelClient(&redisdriver.Options{Addr: addr, Username: t.sentinelUsername, Password: t.sentinelPassword, Protocol: 2, MaxRetries: -1, DialTimeout: o.DialTimeout, ReadTimeout: o.ReadTimeout, WriteTimeout: o.WriteTimeout, ContextTimeoutEnabled: true, DisableIdentity: true, TLSConfig: t.sentinelTLS, PoolSize: 2})
	t.sentinels[addr] = c
	return c, nil
}

// A majority of configured Sentinels must agree. ROLE also fences a stale view
// of a master that has already been demoted. Discovery never executes user SQL.
func (t *topology) master(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, t.options.DialTimeout)
	defer cancel()
	votes := map[string]int{}
	for _, addr := range t.seeds {
		c, e := t.sentinel(addr)
		if e != nil {
			return "", e
		}
		nodeCtx, stop := context.WithTimeout(ctx, min(time.Second, t.options.DialTimeout))
		reply, e := c.GetMasterAddrByName(nodeCtx, t.masterName).Result()
		stop()
		if e != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			continue
		}
		if len(reply) != 2 {
			continue
		}
		node := net.JoinHostPort(reply[0], reply[1])
		if !validNode(node) {
			continue
		}
		votes[node]++
	}
	for addr, v := range votes {
		if v <= len(t.seeds)/2 {
			continue
		}
		c, e := t.client(addr, 0)
		if e != nil {
			return "", e
		}
		role, e := c.Do(ctx, "ROLE").Slice()
		if e == nil && len(role) > 0 && role[0] == "master" {
			return addr, nil
		}
	}
	return "", errors.New("Sentinels did not agree on an available Redis master")
}
func (t *topology) refresh(ctx context.Context) ([]clusterRange, error) {
	ctx, cancel := context.WithTimeout(ctx, t.options.DialTimeout)
	defer cancel()
	t.mu.Lock()
	candidates := append([]string(nil), t.seeds...)
	for _, r := range t.ranges {
		candidates = append(candidates, r.Addr)
	}
	t.mu.Unlock()
	seen := map[string]bool{}
	for _, addr := range candidates {
		if seen[addr] {
			continue
		}
		seen[addr] = true
		c, e := t.client(addr, 0)
		if e != nil {
			return nil, e
		}
		nodeCtx, stop := context.WithTimeout(ctx, min(time.Second, t.options.DialTimeout))
		slots, e := c.ClusterSlots(nodeCtx).Result()
		stop()
		if e != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		if len(slots) == 0 || len(slots) > 16384 {
			continue
		}
		ranges := make([]clusterRange, 0, len(slots))
		valid := true
		for _, slot := range slots {
			if len(slot.Nodes) == 0 || !validNode(slot.Nodes[0].Addr) || slot.Start < 0 || slot.End > 16383 || slot.Start > slot.End {
				valid = false
				break
			}
			ranges = append(ranges, clusterRange{slot.Start, slot.End, slot.Nodes[0].ID, slot.Nodes[0].Addr})
		}
		if !valid {
			continue
		}
		sort.Slice(ranges, func(i, j int) bool { return ranges[i].Start < ranges[j].Start })
		next := 0
		for _, r := range ranges {
			if r.Start != next {
				valid = false
				break
			}
			next = r.End + 1
		}
		if !valid || next != 16384 {
			continue
		}
		t.mu.Lock()
		if t.closed {
			t.mu.Unlock()
			return nil, contract.ErrRevoked
		}
		t.ranges = ranges
		t.mu.Unlock()
		return append([]clusterRange(nil), ranges...), nil
	}
	return nil, errors.New("Redis Cluster slot map is unavailable or incomplete")
}
func (t *topology) slot(ctx context.Context, slot int) (string, error) {
	t.mu.Lock()
	ranges := append([]clusterRange(nil), t.ranges...)
	t.mu.Unlock()
	if len(ranges) == 0 {
		var e error
		ranges, e = t.refresh(ctx)
		if e != nil {
			return "", e
		}
	}
	for _, r := range ranges {
		if slot >= r.Start && slot <= r.End {
			return r.Addr, nil
		}
	}
	return "", errors.New("Redis Cluster slot is unavailable")
}
func (t *topology) masters(ctx context.Context) ([]scanNode, error) {
	ranges, e := t.refresh(ctx)
	if e != nil {
		return nil, e
	}
	seen := map[string]bool{}
	nodes := []scanNode{}
	for _, r := range ranges {
		if !seen[r.ID+"/"+r.Addr] {
			seen[r.ID+"/"+r.Addr] = true
			nodes = append(nodes, scanNode{ID: r.ID, Addr: r.Addr})
		}
	}
	if len(nodes) > 256 {
		return nil, errors.New("Redis Cluster master limit reached")
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	return nodes, nil
}
func redirect(err error, slot int) (kind, addr string, ok bool) {
	var server redisdriver.Error
	var wire replyError
	if !errors.As(err, &server) && !errors.As(err, &wire) {
		return "", "", false
	}
	words := strings.Fields(err.Error())
	if len(words) != 3 || words[0] != "MOVED" && words[0] != "ASK" {
		return "", "", false
	}
	n, e := strconv.Atoi(words[1])
	if e != nil || n != slot || !validNode(words[2]) {
		return "", "", false
	}
	return words[0], words[2], true
}

// Only MOVED and ASK prove that the operation did not execute. Network errors,
// READONLY and script errors return to the draft/verification lifecycle unchanged.
func (s *Session) keyEval(ctx context.Context, c *redisdriver.Client, script string, keys []string, args ...any) (any, error) {
	if s.topology == nil || s.topology.mode != "cluster" {
		return c.Eval(ctx, script, keys, args...).Result()
	}
	if len(keys) == 0 {
		return nil, contract.ErrUnsupported
	}
	slot := rediscommand.Slot(keys[0])
	for _, k := range keys[1:] {
		if rediscommand.Slot(k) != slot {
			return nil, errors.New("Redis Cluster operation requires keys in the same hash slot")
		}
	}
	addr, e := s.topology.slot(ctx, slot)
	if e != nil {
		return nil, e
	}
	asking := false
	for attempt := 0; attempt < 4; attempt++ {
		var node *redisdriver.Client
		if asking {
			s.topology.mu.Lock()
			o := s.topology.options
			s.topology.mu.Unlock()
			o.Addr = addr
			o.DB = 0
			o.PoolSize = 1
			o.OnConnect = func(ctx context.Context, c *redisdriver.Conn) error { return c.Do(ctx, "ASKING").Err() }
			node = redisdriver.NewClient(&o)
		} else {
			node, e = s.topology.client(addr, 0)
			if e != nil {
				return nil, e
			}
		}
		value, err := node.Eval(ctx, script, keys, args...).Result()
		if asking {
			node.Close()
		}
		if err == nil {
			return value, nil
		}
		kind, target, ok := redirect(err, slot)
		if !ok {
			return nil, err
		}
		addr = target
		asking = kind == "ASK"
		if kind == "MOVED" {
			_, _ = s.topology.refresh(ctx)
		}
	}
	return nil, errors.New("Redis Cluster redirect limit reached")
}
func (t *topology) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	nodes, sentinels := t.nodes, t.sentinels
	t.nodes = nil
	t.sentinels = nil
	t.options.Password = ""
	t.sentinelPassword = ""
	t.mu.Unlock()
	var err error
	for _, c := range nodes {
		err = errors.Join(err, c.Close())
	}
	for _, c := range sentinels {
		err = errors.Join(err, c.Close())
	}
	return err
}
func (s *Session) scanPlan(ctx context.Context, c *redisdriver.Client) ([]scanNode, error) {
	if s.topology == nil {
		return []scanNode{{ID: "standalone", Addr: c.Options().Addr}}, nil
	}
	if s.topology.mode == "cluster" {
		return s.topology.masters(ctx)
	}
	addr, e := s.topology.master(ctx)
	if e != nil {
		return nil, e
	}
	if addr != c.Options().Addr {
		return nil, errors.New("Redis master changed; refresh the key browser")
	}
	return []scanNode{{ID: addr, Addr: addr}}, nil
}
func planIdentity(nodes []scanNode) string {
	var b strings.Builder
	for _, n := range nodes {
		fmt.Fprintf(&b, "%s/%s;", n.ID, n.Addr)
	}
	return b.String()
}

func (s *Session) scanEpoch(nodes []scanNode) string {
	epoch := planIdentity(nodes)
	if s.topology != nil && s.topology.mode == "cluster" {
		s.topology.mu.Lock()
		for _, r := range s.topology.ranges {
			epoch += fmt.Sprintf("%d-%d/%s;", r.Start, r.End, r.ID)
		}
		s.topology.mu.Unlock()
	}
	return digest(epoch)
}

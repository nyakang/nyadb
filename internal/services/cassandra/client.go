package cassandra

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/services/connections"
)

const identityQuery = "SELECT host_id, cluster_name, release_version, cql_version, data_center, rack, partitioner, schema_version FROM system.local WHERE key='local'"

type Client struct {
	operation       sync.Mutex
	mu              sync.Mutex
	conn            net.Conn
	closed, retired bool
	closeDone       chan struct{}
	identity        Identity
	key             [32]byte
}

func Open(ctx context.Context, cfg connections.CassandraConfig, secret string) (*Client, error) {
	cfg, e := connections.NormalizeCassandraConfig(cfg)
	if e != nil {
		return nil, e
	}
	if e = connections.ValidateCassandraCredential(cfg, secret, true); e != nil {
		return nil, e
	}
	if e = ctx.Err(); e != nil {
		return nil, e
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	var conn net.Conn
	if cfg.TLSMode == "disable" {
		conn, e = dialer.DialContext(ctx, "tcp", cfg.Endpoint)
	} else {
		host, _, _ := net.SplitHostPort(cfg.Endpoint)
		name := cfg.ServerName
		if name == "" {
			name = host
		}
		tc := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: name}
		if cfg.CAFile != "" {
			file, err := os.Open(cfg.CAFile)
			if err != nil {
				return nil, errors.New("Cassandra CA cannot be read")
			}
			raw, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
			file.Close()
			pool := x509.NewCertPool()
			if err != nil || len(raw) > 1<<20 || !pool.AppendCertsFromPEM(raw) {
				return nil, errors.New("invalid Cassandra CA")
			}
			tc.RootCAs = pool
		}
		td := &tls.Dialer{NetDialer: dialer, Config: tc}
		conn, e = td.DialContext(ctx, "tcp", cfg.Endpoint)
	}
	if e != nil {
		if ce := ctx.Err(); ce != nil {
			return nil, ce
		}
		return nil, errors.New("Cassandra native connection or TLS failed")
	}
	c := &Client{conn: conn, closeDone: make(chan struct{})}
	if _, e = rand.Read(c.key[:]); e != nil {
		c.Close()
		return nil, e
	}
	if e = c.startup(ctx, cfg, secret); e == nil {
		var result *Result
		result, e = c.execute(ctx, Request{Statement: identityQuery, PageSize: 2}, true)
		if e == nil {
			c.identity, e = readIdentity(result)
		}
	}
	if e != nil {
		if ce := ctx.Err(); ce != nil {
			e = ce
		}
		c.Close()
		return nil, e
	}
	return c, nil
}
func (c *Client) Identity() Identity { return c.identity }
func (c *Client) Alive() bool        { c.mu.Lock(); defer c.mu.Unlock(); return !c.closed && !c.retired }
func (c *Client) retire() {
	c.mu.Lock()
	c.retired = true
	_ = c.conn.Close()
	c.mu.Unlock()
}
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		done := c.closeDone
		c.mu.Unlock()
		<-done
		return nil
	}
	c.closed = true
	_ = c.conn.Close()
	c.mu.Unlock()
	c.operation.Lock()
	clear(c.key[:])
	c.operation.Unlock()
	close(c.closeDone)
	return nil
}
func (c *Client) exchange(ctx context.Context, opcode byte, body []byte) (out frame, err error) {
	if e := ctx.Err(); e != nil {
		return frame{}, e
	}
	if !c.Alive() {
		return frame{}, ErrRetired
	}
	deadline := time.Now().Add(45 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if e := c.conn.SetDeadline(deadline); e != nil {
		return frame{}, e
	}
	// Cancellation revokes the original physical socket; protocol v4 has no
	// query-cancel message. Wait for this local worker, never infer remote rollback.
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { c.retire(); close(done) })
	defer func() {
		if !stop() {
			<-done
		}
		if e := ctx.Err(); e != nil {
			err = e
		}
	}()
	if e := writeFrame(c.conn, opcode, body); e != nil {
		return frame{}, e
	}
	f, e := readFrame(c.conn)
	if e == nil {
		e = ctx.Err()
	}
	return f, e
}
func (c *Client) startup(ctx context.Context, cfg connections.CassandraConfig, secret string) error {
	c.operation.Lock()
	defer c.operation.Unlock()
	f, e := c.exchange(ctx, 5, nil)
	if e != nil {
		return ErrProtocol
	}
	r, _, e := frameReader(f)
	if e != nil {
		return e
	}
	if f.opcode != 6 {
		return ErrProtocol
	}
	n := int(r.u16())
	if n > 64 {
		return ErrBounds
	}
	version := ""
	for range n {
		key := r.str()
		list := r.list()
		if key == "CQL_VERSION" {
			for _, v := range list {
				if v == "3.4.7" {
					version = v
				}
			}
		}
	}
	if r.done() != nil || version == "" {
		return ErrProtocol
	}
	w := &writer{}
	w.u16(1)
	w.str("CQL_VERSION")
	w.str(version)
	f, e = c.exchange(ctx, 1, w.Bytes())
	if e != nil {
		return ErrProtocol
	}
	r, _, e = frameReader(f)
	if e != nil {
		return e
	}
	if f.opcode == 2 {
		if r.done() != nil || cfg.Auth != "none" {
			return ErrAuthentication
		}
		return nil
	}
	if f.opcode != 3 || r.str() != "org.apache.cassandra.auth.PasswordAuthenticator" || r.done() != nil || cfg.Auth != "password" {
		return ErrAuthentication
	}
	token := make([]byte, 0, len(cfg.Username)+len(secret)+2)
	token = append(token, 0)
	token = append(token, cfg.Username...)
	token = append(token, 0)
	token = append(token, secret...)
	w = &writer{}
	w.blob(token)
	clear(token)
	f, e = c.exchange(ctx, 15, w.Bytes())
	clear(w.Bytes())
	if e != nil {
		return ErrAuthentication
	}
	r, _, e = frameReader(f)
	if e != nil || f.opcode != 16 {
		return ErrAuthentication
	}
	_, _ = r.blob()
	return r.done()
}
func readIdentity(r *Result) (Identity, error) {
	if r == nil || r.Kind != 2 || r.Next != nil || len(r.Rows) != 1 || len(r.Columns) != 8 {
		return Identity{}, ErrProtocol
	}
	names := []string{"host_id", "cluster_name", "release_version", "cql_version", "data_center", "rack", "partitioner", "schema_version"}
	vals := make([]string, 8)
	for i, name := range names {
		col, value := r.Columns[i], r.Rows[0][i]
		if col.Keyspace != "system" || col.Table != "local" || col.Name != name || col.Type == nil || value.Null || value.Missing {
			return Identity{}, ErrProtocol
		}
		if (i == 0 || i == 7) && col.Type.Code != 0xc || i > 0 && i < 7 && col.Type.Code != 0xa && col.Type.Code != 0xd {
			return Identity{}, ErrProtocol
		}
		vals[i] = r.Rows[0][i].Text
	}
	out := Identity{vals[0], vals[1], vals[2], vals[3], vals[4], vals[5], vals[6], vals[7]}
	if len(out.HostID) != 36 || len(out.SchemaVersion) != 36 || out.ClusterName == "" || out.ReleaseVersion != "5.0.6" || out.CQLVersion != "3.4.7" || out.DataCenter == "" || out.Rack == "" || out.Partitioner != "org.apache.cassandra.dht.Murmur3Partitioner" {
		return Identity{}, ErrProtocol
	}
	return out, nil
}
func (c *Client) cursorMAC(hash [32]byte, state []byte) [32]byte {
	h := hmac.New(sha256.New, c.key[:])
	h.Write(hash[:])
	h.Write(state)
	var mac [32]byte
	copy(mac[:], h.Sum(nil))
	return mac
}
func requestBody(r Request) ([]byte, [32]byte, error) {
	var zero [32]byte
	if len(r.Statement) == 0 || len(r.Statement) > MaxStatement || !utf8.ValidString(r.Statement) || strings.ContainsRune(r.Statement, 0) || len(r.Parameters) > MaxColumns {
		return nil, zero, ErrInvalid
	}
	if r.PageSize == 0 {
		r.PageSize = 500
	}
	if r.PageSize < 1 || r.PageSize > MaxRows {
		return nil, zero, ErrBounds
	}
	if r.Consistency == 0 {
		r.Consistency = 10
	}
	if r.Consistency > 10 || r.SerialConsistency != 0 && r.SerialConsistency != 8 && r.SerialConsistency != 9 {
		return nil, zero, ErrInvalid
	}
	size := len(r.Statement) + 16
	for _, p := range r.Parameters {
		size += len(p.Bytes) + 4
		if p.Null && p.Unset || (p.Null || p.Unset) && len(p.Bytes) != 0 || len(p.Bytes) > MaxFrame || size > MaxFrame-maxPageState {
			return nil, zero, ErrInvalid
		}
	}
	w := &writer{}
	w.long(r.Statement)
	w.u16(r.Consistency)
	flags := byte(4)
	if len(r.Parameters) > 0 {
		flags |= 1
	}
	if r.SerialConsistency != 0 {
		flags |= 16
	}
	// The base payload excludes the pagination bit/state and authenticates every
	// execution option, including byte-exact parameters and page size.
	w.WriteByte(flags)
	if len(r.Parameters) > 0 {
		w.u16(uint16(len(r.Parameters)))
		for _, p := range r.Parameters {
			switch {
			case p.Null:
				w.i32(-1)
			case p.Unset:
				w.i32(-2)
			default:
				w.blob(p.Bytes)
			}
		}
	}
	w.i32(int32(r.PageSize))
	if r.SerialConsistency != 0 {
		w.u16(r.SerialConsistency)
	}
	body := append([]byte(nil), w.Bytes()...)
	return body, sha256.Sum256(body), nil
}
func (c *Client) Execute(ctx context.Context, r Request) (*Result, error) {
	return c.execute(ctx, r, false)
}
func (c *Client) execute(ctx context.Context, request Request, metadata bool) (*Result, error) {
	// Construct the owned wire request on the caller's thread, before a worker or
	// cursor can be parked. Neither caller parameters nor cursor bytes are retained.
	base, hash, e := requestBody(request)
	if e != nil {
		return nil, e
	}
	if e = ctx.Err(); e != nil {
		return nil, e
	}
	if !c.operation.TryLock() {
		return nil, ErrBusy
	}
	defer c.operation.Unlock()
	if !c.Alive() {
		return nil, ErrRetired
	}
	if e = ctx.Err(); e != nil {
		return nil, e
	}
	body := base
	if token := request.Cursor; token != nil {
		expected := c.cursorMAC(hash, token.state)
		if token.owner != c || token.request != hash || !hmac.Equal(token.mac[:], expected[:]) || len(token.state) == 0 || len(token.state) > maxPageState {
			return nil, ErrInvalid
		}
		state := append([]byte(nil), token.state...)
		// Insert paging state immediately after page size and before serial consistency.
		pos := len(base)
		if request.SerialConsistency != 0 {
			pos -= 2
		}
		w := &writer{}
		w.Write(base[:pos])
		w.blob(state)
		w.Write(base[pos:])
		body = w.Bytes()
		body[6+len(request.Statement)] |= 8
	}
	f, e := c.exchange(ctx, 7, body)
	if e != nil {
		c.retire()
		return nil, &Interrupted{!metadata, e}
	}
	result, state, e := decodeResult(f)
	if e != nil {
		var native *ServerError
		if errors.As(e, &native) {
			native.WriteOutcomeUnknown = !metadata && native.Code != 0x2000
			if native.WriteOutcomeUnknown {
				c.retire()
			}
			return nil, native
		}
		c.retire()
		return nil, &Interrupted{!metadata, e}
	}
	if len(state) > 0 {
		result.Next = &Cursor{owner: c, request: hash, state: state, mac: c.cursorMAC(hash, state)}
	}
	return result, nil
}

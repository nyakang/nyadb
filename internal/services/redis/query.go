package redis

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"crypto/tls"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/rediscommand"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type queryLease struct {
	mu                           sync.Mutex
	op                           sync.Mutex
	session                      *Session
	ctx                          context.Context
	cancel                       context.CancelFunc
	conn                         net.Conn
	reader                       *bufio.Reader
	closed, lost, multi, watched bool
	catalog                      string
}
type replyError string

func (e replyError) Error() string { return string(e) }
func (s *Session) ReserveQuery(_ uint64) (contract.QueryLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, contract.ErrRevoked
	}
	if len(s.leases) >= s.profile.RedisOptions.QueryMaxConnections {
		return nil, contract.ErrSQLSessionLimit
	}
	ctx, cancel := context.WithCancel(s.ctx)
	l := &queryLease{session: s, ctx: ctx, cancel: cancel, catalog: s.profile.Database}
	s.leases[l] = true
	return l, nil
}
func (l *queryLease) Release() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	l.cancel()
	if l.conn != nil {
		l.conn.Close()
		l.conn = nil
	}
	l.mu.Unlock()
	l.session.mu.Lock()
	delete(l.session.leases, l)
	l.session.mu.Unlock()
}
func (l *queryLease) Replace() (contract.QueryLease, error) {
	s := l.session
	l.Release()
	return s.ReserveQuery(0)
}
func (l *queryLease) dial(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return contract.ErrRevoked
	}
	if l.lost {
		return contract.ErrSQLSessionLost
	}
	if l.conn != nil {
		return nil
	}
	l.session.mu.Lock()
	o := l.session.options
	l.session.mu.Unlock()
	d := net.Dialer{Timeout: o.DialTimeout}
	var c net.Conn
	var err error
	if o.TLSConfig != nil {
		td := tls.Dialer{NetDialer: &d, Config: o.TLSConfig}
		c, err = td.DialContext(ctx, "tcp", o.Addr)
	} else {
		c, err = d.DialContext(ctx, "tcp", o.Addr)
	}
	if err != nil {
		return safeError(err)
	}
	l.conn = c
	l.reader = bufio.NewReader(c)
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { c.Close(); close(done) })
	defer func() {
		if !stop() {
			<-done
		}
	}()
	deadline := time.Now().Add(o.DialTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	c.SetDeadline(deadline)
	if o.Username != "" || o.Password != "" {
		args := []string{"AUTH", o.Password}
		if o.Username != "" {
			args = []string{"AUTH", o.Username, o.Password}
		}
		_, err = l.exchange(args)
	}
	if err == nil {
		_, err = l.exchange([]string{"SELECT", l.catalog})
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if d, ok := ctx.Deadline(); ok && !time.Now().Before(d) {
		err = context.DeadlineExceeded
	}
	if err != nil {
		c.Close()
		l.conn = nil
		return safeError(err)
	}
	c.SetDeadline(time.Time{})
	return nil
}
func (l *queryLease) Preconnect(ctx context.Context) error {
	l.op.Lock()
	defer l.op.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(l.ctx, cancel)
	defer stop()
	return l.dial(ctx)
}
func writeCommand(w io.Writer, args []string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n", len(a))
		b.WriteString(a)
		b.WriteString("\r\n")
	}
	data := []byte(b.String())
	for len(data) > 0 {
		n, e := w.Write(data)
		if e != nil {
			return e
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
func readReply(r *bufio.Reader, budget *int, depth int) (any, error) {
	if depth > 32 || *budget <= 0 {
		return nil, errors.New("Redis result exceeds display limits")
	}
	line, e := r.ReadSlice('\n')
	if e != nil || len(line) < 3 || len(line) > 4096 || line[len(line)-2] != '\r' {
		return nil, errors.New("invalid or oversized Redis reply")
	}
	*budget -= len(line)
	text := string(line[1 : len(line)-2])
	switch line[0] {
	case '+':
		return text, nil
	case '-':
		return nil, replyError(text)
	case ':':
		n, e := strconv.ParseInt(text, 10, 64)
		return n, e
	case '$':
		n, e := strconv.Atoi(text)
		if e != nil || n < -1 || n > *budget {
			return nil, errors.New("Redis result exceeds display limits")
		}
		if n == -1 {
			return nil, nil
		}
		raw := make([]byte, n+2)
		if _, e = io.ReadFull(r, raw); e != nil {
			return nil, e
		}
		if raw[n] != '\r' || raw[n+1] != '\n' {
			return nil, errors.New("invalid Redis reply")
		}
		*budget -= len(raw)
		return string(raw[:n]), nil
	case '*':
		n, e := strconv.Atoi(text)
		if e != nil || n < -1 || n > 1000 {
			return nil, errors.New("Redis result exceeds display limits")
		}
		if n == -1 {
			return nil, nil
		}
		values := make([]any, n)
		for i := range values {
			v, e := readReply(r, budget, depth+1)
			var server replyError
			if errors.As(e, &server) {
				v = map[string]string{"error": "Redis subcommand failed"}
				e = nil
			}
			if e != nil {
				return nil, e
			}
			values[i] = v
		}
		return values, nil
	}
	return nil, errors.New("unsupported Redis reply protocol")
}
func (l *queryLease) exchange(args []string) (any, error) {
	if e := writeCommand(l.conn, args); e != nil {
		return nil, e
	}
	budget := 1 << 20
	return readReply(l.reader, &budget, 0)
}
func responseJSON(v any) any {
	switch x := v.(type) {
	case string:
		if !utf8.ValidString(x) {
			return map[string]string{"base64": base64.StdEncoding.EncodeToString([]byte(x))}
		}
		return x
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			out[i] = responseJSON(v)
		}
		return out
	}
	return v
}
func (l *queryLease) Execute(ctx context.Context, db, text string) (contract.SQLResult, error) {
	return l.ExecuteStream(ctx, db, text, nil)
}
func (l *queryLease) ExecuteStream(ctx context.Context, db, text string, sink func(int, []contract.Column, []contract.Cell) error) (result contract.SQLResult, err error) {
	args, err := rediscommand.Parse(text)
	if err != nil {
		return result, err
	}
	command := strings.ToUpper(args[0])
	// Streaming, protocol switches, and socket ownership changes need a separate UI.
	switch command {
	case "SUBSCRIBE", "PSUBSCRIBE", "SSUBSCRIBE", "MONITOR", "HELLO", "AUTH", "QUIT", "RESET", "SYNC", "PSYNC", "SHUTDOWN":
		return result, contract.ErrUnsupported
	case "SCRIPT":
		if len(args) > 1 && strings.EqualFold(args[1], "DEBUG") {
			return result, contract.ErrUnsupported
		}
	case "CLIENT":
		if len(args) < 2 || strings.ToUpper(args[1]) != "INFO" && strings.ToUpper(args[1]) != "ID" && strings.ToUpper(args[1]) != "LIST" {
			return result, contract.ErrUnsupported
		}
	}
	if _, err = connectionsDatabase(db); err != nil {
		return result, err
	}
	if !l.op.TryLock() {
		return result, errors.New("Redis command is already running")
	}
	defer l.op.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopLease := context.AfterFunc(l.ctx, cancel)
	defer stopLease()
	if err = l.dial(ctx); err != nil {
		return result, err
	}
	l.mu.Lock()
	if l.closed || l.lost {
		l.mu.Unlock()
		return result, contract.ErrSQLSessionLost
	}
	c := l.conn
	if db != l.catalog && (l.multi || l.watched) {
		l.mu.Unlock()
		return result, errors.New("finish Redis MULTI/WATCH before changing databases")
	}
	l.mu.Unlock()
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { c.Close(); close(done) })
	defer func() {
		if !stop() {
			<-done
		}
		c.SetDeadline(time.Time{})
		deadline, hasDeadline := ctx.Deadline()
		if ctx.Err() != nil || hasDeadline && !time.Now().Before(deadline) {
			err = ctx.Err()
			if err == nil {
				err = context.DeadlineExceeded
			}
			l.mu.Lock()
			l.lost = true
			c.Close()
			l.mu.Unlock()
		}
	}()
	if deadline, ok := ctx.Deadline(); ok {
		c.SetDeadline(deadline)
	}
	start := time.Now()
	defer func() { result.Duration = time.Since(start) }()
	if db != l.catalog {
		if _, err = l.exchange([]string{"SELECT", db}); err != nil {
			return result, l.fail(err)
		}
		l.catalog = db
	}
	value, err := l.exchange(args)
	if err != nil {
		var server replyError
		if command == "EXEC" && errors.As(err, &server) && strings.HasPrefix(string(server), "EXECABORT") {
			l.multi = false
			l.watched = false
		}
		return result, l.fail(err)
	}
	switch command {
	case "MULTI":
		l.multi = true
	case "WATCH":
		l.watched = true
	case "UNWATCH":
		l.watched = false
	case "EXEC", "DISCARD":
		l.multi = false
		l.watched = false
	}
	raw, e := json.Marshal(responseJSON(value))
	if e != nil {
		return result, e
	}
	cell := preview(string(raw))
	set := contract.ResultSet{Columns: []contract.Column{{Name: "reply", DatabaseType: "Redis RESP"}}, Rows: [][]contract.Cell{{cell}}}
	if cell.Truncated {
		set.Truncation = []string{"Redis reply preview limited to 1024 characters"}
	}
	result.Sets = []contract.ResultSet{set}
	result.SetLegacyView()
	if sink != nil {
		err = sink(0, set.Columns, set.Rows[0])
	}
	return result, err
}
func connectionsDatabase(db string) (int, error) { return connections.RedisDatabase(db) }
func (l *queryLease) fail(err error) error {
	var server replyError
	if errors.As(err, &server) {
		return errors.New("Redis command failed; check command arguments and permissions")
	}
	l.mu.Lock()
	l.lost = true
	if l.conn != nil {
		l.conn.Close()
	}
	l.mu.Unlock()
	return errors.Join(contract.ErrSQLSessionLost, safeError(err))
}

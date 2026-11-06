package redis

import (
	"bufio"
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/rediscommand"
)

func wireExchange(c net.Conn, r *bufio.Reader, args []string) (any, error) {
	if e := writeCommand(c, args); e != nil {
		return nil, e
	}
	budget := 1 << 20
	return readReply(r, &budget, 0)
}
func (l *queryLease) roundTrip(ctx context.Context, args []string) (value any, err error) {
	if err = l.dial(ctx); err != nil {
		return nil, err
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil, contract.ErrRevoked
	}
	if l.lost {
		l.mu.Unlock()
		return nil, contract.ErrSQLSessionLost
	}
	c, r := l.conn, l.reader
	l.mu.Unlock()
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { c.Close(); close(done) })
	defer func() {
		if !stop() {
			<-done
		}
		c.SetDeadline(time.Time{})
		deadline, ok := ctx.Deadline()
		if ctx.Err() != nil || ok && !time.Now().Before(deadline) {
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
	return wireExchange(c, r, args)
}
func (l *queryLease) switchNode(ctx context.Context, addr string) error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return contract.ErrRevoked
	}
	if l.lost {
		l.mu.Unlock()
		return contract.ErrSQLSessionLost
	}
	if l.conn != nil && l.boundAddress == addr {
		l.mu.Unlock()
		return nil
	}
	if l.watched || l.multi && !l.pendingMulti {
		l.mu.Unlock()
		return errors.New("Redis transaction is bound to its original node")
	}
	if l.conn != nil {
		l.conn.Close()
		l.conn = nil
	}
	l.targetAddress = addr
	l.mu.Unlock()
	return l.dial(ctx)
}
func (s *Session) commandKeys(ctx context.Context, args []string) ([]string, error) {
	ctx, cancel, c, e := s.request(ctx, "0")
	if e != nil {
		return nil, e
	}
	defer cancel()
	values := make([]any, len(args))
	for i, v := range args {
		values[i] = v
	}
	keys, e := c.CommandGetKeys(ctx, values...).Result()
	if e != nil && strings.Contains(e.Error(), "no key arguments") {
		return nil, nil
	}
	if e != nil {
		return nil, errors.New("Redis command arguments or key metadata are unavailable")
	}
	return keys, nil
}
func (l *queryLease) flushMulti(ctx context.Context) error {
	if !l.pendingMulti {
		return nil
	}
	if _, e := l.roundTrip(ctx, []string{"MULTI"}); e != nil {
		return e
	}
	l.pendingMulti = false
	for _, args := range l.pendingCommands {
		if _, e := l.roundTrip(ctx, args); e != nil {
			return e
		}
	}
	l.pendingCommands = nil
	l.pendingBytes = 0
	return nil
}
func (l *queryLease) clearTransaction() {
	l.multi = false
	l.multiFailed = false
	l.watched = false
	l.pendingMulti = false
	l.pendingCommands = nil
	l.pendingBytes = 0
	l.boundSlot = -1
}
func (l *queryLease) runNative(ctx context.Context, db string, args []string) (_ any, err error) {
	defer func() {
		if err != nil && l.multi {
			l.multiFailed = true
		}
	}()
	mode := "standalone"
	if l.session.topology != nil {
		mode = l.session.topology.mode
	}
	if mode == "sentinel" {
		addr, e := l.session.topology.master(ctx)
		if e != nil {
			return nil, e
		}
		l.mu.Lock()
		changed := l.conn != nil && l.boundAddress != addr
		l.mu.Unlock()
		if changed {
			return nil, l.fail(errors.New("Redis Sentinel selected a new master; recover the command session"))
		}
	}
	if e := l.dial(ctx); e != nil {
		return nil, e
	}
	if db != l.catalog {
		if l.multi || l.watched {
			return nil, errors.New("finish Redis MULTI/WATCH before changing databases")
		}
		if _, e := l.roundTrip(ctx, []string{"SELECT", db}); e != nil {
			return nil, e
		}
		l.catalog = db
	}
	command := strings.ToUpper(args[0])
	if mode == "cluster" {
		if (l.multi || l.watched) && l.boundSlot >= 0 {
			if _, e := l.session.topology.refresh(ctx); e != nil {
				return nil, e
			}
			addr, e := l.session.topology.slot(ctx, l.boundSlot)
			if e != nil {
				return nil, e
			}
			l.mu.Lock()
			changed := addr != l.boundAddress
			l.mu.Unlock()
			if changed {
				return nil, l.fail(errors.New("Redis transaction's slot owner changed; recover the command session"))
			}
		}
		if command == "SELECT" && (len(args) != 2 || args[1] != "0") {
			return nil, errors.New("Redis Cluster supports only database 0")
		}
		switch command {
		case "MULTI":
			if l.multi {
				return nil, errors.New("Redis MULTI is already active")
			}
			l.multi = true
			if !l.watched {
				l.pendingMulti = true
				return "OK", nil
			}
		case "DISCARD":
			if l.pendingMulti {
				l.clearTransaction()
				return "OK", nil
			}
		case "EXEC":
			if l.multiFailed {
				if !l.pendingMulti {
					if _, e := l.roundTrip(ctx, []string{"DISCARD"}); e != nil {
						return nil, l.fail(e)
					}
				}
				l.clearTransaction()
				return nil, errors.New("Redis transaction was aborted after a rejected command; no queued commands were applied")
			}
			if e := l.flushMulti(ctx); e != nil {
				return nil, l.fail(e)
			}
		default:
			keys, e := l.session.commandKeys(ctx, args)
			if e != nil {
				return nil, e
			}
			if len(keys) > 0 {
				slot := rediscommand.Slot(keys[0])
				for _, key := range keys[1:] {
					if rediscommand.Slot(key) != slot {
						return nil, errors.New("Redis Cluster command requires keys in one hash slot")
					}
				}
				if (l.multi || l.watched) && l.boundSlot >= 0 && slot != l.boundSlot {
					return nil, errors.New("Redis transaction keys must share one hash slot")
				}
				addr, e := l.session.topology.slot(ctx, slot)
				if e != nil {
					return nil, e
				}
				if l.multi && !l.pendingMulti || l.watched {
					l.mu.Lock()
					same := addr == l.boundAddress
					l.mu.Unlock()
					if !same {
						return nil, l.fail(errors.New("Redis transaction's slot owner changed"))
					}
				} else if e = l.switchNode(ctx, addr); e != nil {
					return nil, e
				}
				if l.multi || command == "WATCH" {
					l.boundSlot = slot
				}
				if e = l.flushMulti(ctx); e != nil {
					return nil, l.fail(e)
				}
			} else if l.pendingMulti {
				if len(l.pendingCommands) >= 256 {
					return nil, errors.New("Redis queued command limit reached")
				}
				size := 0
				for _, arg := range args {
					size += len(arg)
				}
				if l.pendingBytes+size > 1<<20 {
					return nil, errors.New("Redis queued commands exceed 1 MiB")
				}
				l.pendingCommands = append(l.pendingCommands, append([]string(nil), args...))
				l.pendingBytes += size
				return "QUEUED", nil
			}
		}
	}
	for attempt := 0; attempt < 4; attempt++ {
		value, e := l.roundTrip(ctx, args)
		if e == nil {
			switch command {
			case "MULTI":
				l.multi = true
			case "WATCH":
				l.watched = true
			case "UNWATCH":
				l.watched = false
				if !l.multi {
					l.boundSlot = -1
				}
			case "EXEC", "DISCARD":
				l.clearTransaction()
			}
			return value, nil
		}
		var server replyError
		if command == "EXEC" && errors.As(e, &server) && strings.HasPrefix(string(server), "EXECABORT") {
			l.clearTransaction()
		}
		if mode != "cluster" || l.multi || l.watched {
			return nil, l.fail(e)
		}
		keys, keyErr := l.session.commandKeys(ctx, args)
		if keyErr != nil || len(keys) == 0 {
			return nil, l.fail(e)
		}
		kind, addr, ok := redirect(e, rediscommand.Slot(keys[0]))
		if !ok {
			return nil, l.fail(e)
		}
		if kind == "MOVED" {
			_, _ = l.session.topology.refresh(ctx)
		}
		if e = l.switchNode(ctx, addr); e != nil {
			return nil, e
		}
		if kind == "ASK" {
			if _, e = l.roundTrip(ctx, []string{"ASKING"}); e != nil {
				return nil, l.fail(e)
			}
		}
	}
	return nil, errors.New("Redis Cluster command redirect limit reached")
}

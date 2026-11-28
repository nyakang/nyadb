package manticore

import (
	"context"
	"database/sql/driver"
	"net"

	"github.com/nyakang/nyadb/internal/services/connections"
)

type transportConnection struct {
	driver.Conn
	driver.ExecerContext
	driver.QueryerContext
	driver.Validator
	physical net.Conn
}

func (c *transportConnection) Close() error {
	// Retire this physical owner before driver.Close sends COM_QUIT. Otherwise
	// the native server can close first and an idle EOF watcher can mistake
	// normal disposal for loss of the whole shared transport.
	err := c.physical.Close()
	_ = c.Conn.Close()
	return err
}

// SessionTransport owns all physical channels for one original session. Done
// must be stable and non-nil, and Close must be idempotent. Terminal loss cannot
// allocate a replacement transport or replay an operation.
type SessionTransport interface {
	DialContext(context.Context, string, string) (net.Conn, error)
	Close() error
	Done() <-chan struct{}
}

// OpenWithTransport consumes the original transport even if validation or the
// native handshake fails. It remains private to the Manticore adapter.
func OpenWithTransport(ctx context.Context, profileID int64, cfg connections.ManticoreConfig, password string, owned SessionTransport) (*Session, error) {
	if owned == nil {
		return nil, connections.ErrInvalid
	}
	return openSession(ctx, profileID, cfg, password, owned)
}

// Caller holds mu. Check terminal admission synchronously, before a watcher
// has necessarily been scheduled, so idle loss cannot publish a fresh owner.
func (s *Session) aliveLocked() bool {
	if s.closed || s.ctx.Err() != nil {
		return false
	}
	select {
	case <-s.transportDone:
		s.cancel()
		return false
	default:
		return true
	}
}

func (s *Session) watchTransport() {
	select {
	case <-s.transportDone:
		// Keep document receipts until the UI consumes them and explicitly closes
		// their owner. Canceling requests must not destructively close documents.
		s.cancel()
	case <-s.ctx.Done():
	}
}

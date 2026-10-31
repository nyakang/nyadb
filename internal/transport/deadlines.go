package transport

import (
	"net"
	"os"
	"sync"
	"time"
)

// SSH channels lack native deadlines. This adapter waits for the channel
// executor to acknowledge interruption before returning the caller's buffer.
// A deadline closes the original channel; it never opens or replays a channel.
type deadlineConn struct {
	net.Conn
	mu              sync.Mutex
	readMu, writeMu sync.Mutex
	read, write     time.Time
	changed         chan struct{}
}

func withDeadlines(c net.Conn) net.Conn {
	return &deadlineConn{Conn: bufferSocket(c), changed: make(chan struct{})}
}
func (c *deadlineConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	c.read = t
	c.write = t
	close(c.changed)
	c.changed = make(chan struct{})
	c.mu.Unlock()
	return nil
}
func (c *deadlineConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.read = t
	close(c.changed)
	c.changed = make(chan struct{})
	c.mu.Unlock()
	return nil
}
func (c *deadlineConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	c.write = t
	close(c.changed)
	c.changed = make(chan struct{})
	c.mu.Unlock()
	return nil
}
func (c *deadlineConn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	return c.run(p, true)
}
func (c *deadlineConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.run(p, false)
}
func (c *deadlineConn) run(p []byte, read bool) (int, error) {
	c.mu.Lock()
	initial := c.write
	if read {
		initial = c.read
	}
	c.mu.Unlock()
	if !initial.IsZero() && !time.Now().Before(initial) {
		return 0, os.ErrDeadlineExceeded
	}
	type outcome struct {
		n   int
		err error
	}
	result := make(chan outcome, 1)
	go func() {
		var n int
		var err error
		if read {
			n, err = c.Conn.Read(p)
		} else {
			n, err = c.Conn.Write(p)
		}
		result <- outcome{n, err}
	}()
	for {
		c.mu.Lock()
		deadline := c.write
		if read {
			deadline = c.read
		}
		changed := c.changed
		c.mu.Unlock()
		var timer *time.Timer
		var timeout <-chan time.Time
		if !deadline.IsZero() {
			timer = time.NewTimer(max(time.Until(deadline), 0))
			timeout = timer.C
		}
		select {
		case r := <-result:
			if timer != nil {
				timer.Stop()
			}
			return r.n, r.err
		case <-changed:
			if timer != nil {
				timer.Stop()
			}
		case <-timeout:
			_ = c.Conn.Close()
			r := <-result
			return r.n, os.ErrDeadlineExceeded
		}
	}
}

func (c *deadlineConn) Done() <-chan struct{} {
	return c.Conn.(interface{ Done() <-chan struct{} }).Done()
}

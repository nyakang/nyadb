package transport

import (
	"net"
	"sync"
)

// The bounded reader detects remote channel closure even while a database
// connection is idle, retaining at most two 64 KiB packets plus its current read.
type bufferedConn struct {
	net.Conn
	mu, readMu sync.Mutex
	pending    []byte
	packets    chan []byte
	done       chan struct{}
	once       sync.Once
	err        error
}

func bufferSocket(c net.Conn) net.Conn {
	b := &bufferedConn{Conn: c, packets: make(chan []byte, 2), done: make(chan struct{})}
	go b.readLoop()
	return b
}
func (b *bufferedConn) readLoop() {
	defer close(b.packets)
	for {
		packet := make([]byte, MaxFrameBytes)
		n, err := b.Conn.Read(packet)
		if n > 0 {
			select {
			case b.packets <- packet[:n]:
			case <-b.done:
				return
			}
		}
		if err != nil {
			b.mu.Lock()
			b.err = err
			b.mu.Unlock()
			b.once.Do(func() { close(b.done) })
			return
		}
	}
}
func (b *bufferedConn) Read(p []byte) (int, error) {
	b.readMu.Lock()
	defer b.readMu.Unlock()
	if len(p) == 0 {
		return 0, nil
	}
	for len(b.pending) == 0 {
		packet, ok := <-b.packets
		if !ok {
			b.mu.Lock()
			err := b.err
			b.mu.Unlock()
			if err == nil {
				err = net.ErrClosed
			}
			return 0, err
		}
		b.pending = packet
	}
	n := copy(p, b.pending)
	b.pending = b.pending[n:]
	return n, nil
}
func (b *bufferedConn) Done() <-chan struct{} { return b.done }
func (b *bufferedConn) Close() error          { b.once.Do(func() { close(b.done) }); return b.Conn.Close() }

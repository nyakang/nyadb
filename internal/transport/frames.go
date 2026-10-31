package transport

import (
	"errors"
	"golang.org/x/net/websocket"
	"io"
	"net"
	"sync"
	"time"
)

const Protocol = "nyadb-tcp-v1"
const MaxFrameBytes = 64 << 10

var binaryCodec = websocket.Codec{Marshal: func(v any) ([]byte, byte, error) {
	p, ok := v.([]byte)
	if !ok {
		return nil, 0, errors.New("binary data required")
	}
	return p, websocket.BinaryFrame, nil
}, Unmarshal: func(p []byte, t byte, v any) error {
	if t != websocket.BinaryFrame {
		return errors.New("nyadb-tcp-v1 requires binary frames")
	}
	dst, ok := v.(*[]byte)
	if !ok {
		return errors.New("binary target required")
	}
	*dst = append((*dst)[:0], p...)
	return nil
}}

// frameConn preserves TCP stream semantics across bounded binary messages.

type frameConn struct {
	ws              *websocket.Conn
	readMu, writeMu sync.Mutex
	pending         []byte
	frames          chan []byte
	done            chan struct{}
	once            sync.Once
	errMu           sync.Mutex
	readErr         error
}

func frameSocket(ws *websocket.Conn) net.Conn {
	ws.MaxPayloadBytes = MaxFrameBytes
	c := &frameConn{ws: ws, frames: make(chan []byte, 2), done: make(chan struct{})}
	go c.readLoop()
	return c
}
func (c *frameConn) readLoop() {
	defer close(c.frames)
	for {
		var packet []byte
		if err := binaryCodec.Receive(c.ws, &packet); err != nil {
			c.errMu.Lock()
			c.readErr = err
			c.errMu.Unlock()
			c.once.Do(func() { close(c.done) })
			return
		}
		if len(packet) == 0 {
			continue
		}
		select {
		case c.frames <- packet:
		case <-c.done:
			return
		}
	}
}
func (c *frameConn) Done() <-chan struct{} { return c.done }
func (c *frameConn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	if len(p) == 0 {
		return 0, nil
	}
	for len(c.pending) == 0 {
		packet, ok := <-c.frames
		if !ok {
			c.errMu.Lock()
			err := c.readErr
			c.errMu.Unlock()
			if err == nil {
				err = net.ErrClosed
			}
			return 0, err
		}
		c.pending = packet
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}
func (c *frameConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	n := 0
	for len(p) > 0 {
		chunk := p[:min(len(p), MaxFrameBytes)]
		if err := binaryCodec.Send(c.ws, chunk); err != nil {
			return n, err
		}
		n += len(chunk)
		p = p[len(chunk):]
	}
	return n, nil
}
func (c *frameConn) Close() error {
	c.once.Do(func() { close(c.done) })
	// WebSocket.Close writes a close frame before closing TCP. Interrupt a
	// blocked writer first so cancellation cannot wait for a stalled peer.
	_ = c.ws.SetDeadline(time.Now())
	_ = c.ws.Close()
	return nil
}

func (c *frameConn) LocalAddr() net.Addr                { return c.ws.LocalAddr() }
func (c *frameConn) RemoteAddr() net.Addr               { return c.ws.RemoteAddr() }
func (c *frameConn) SetDeadline(t time.Time) error      { return c.ws.SetDeadline(t) }
func (c *frameConn) SetReadDeadline(t time.Time) error  { return c.ws.SetReadDeadline(t) }
func (c *frameConn) SetWriteDeadline(t time.Time) error { return c.ws.SetWriteDeadline(t) }

var _ io.ReadWriteCloser = (*frameConn)(nil)

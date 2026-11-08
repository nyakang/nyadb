package cassandra

import (
	"bytes"
	"encoding/binary"
	"io"
	"unicode/utf8"
)

type reader struct {
	data           []byte
	pos            int
	err            error
	cells, decoded int
	types          int
}

func (r *reader) take(n int) []byte {
	if r.err != nil {
		return nil
	}
	if n < 0 || n > len(r.data)-r.pos {
		r.err = ErrProtocol
		return nil
	}
	b := r.data[r.pos : r.pos+n]
	r.pos += n
	return b
}
func (r *reader) u8() byte {
	b := r.take(1)
	if b == nil {
		return 0
	}
	return b[0]
}
func (r *reader) u16() uint16 {
	b := r.take(2)
	if b == nil {
		return 0
	}
	return binary.BigEndian.Uint16(b)
}
func (r *reader) i32() int32 {
	b := r.take(4)
	if b == nil {
		return 0
	}
	return int32(binary.BigEndian.Uint32(b))
}
func (r *reader) str() string {
	b := r.take(int(r.u16()))
	if r.err == nil && !utf8.Valid(b) {
		r.err = ErrProtocol
	}
	return string(b)
}
func (r *reader) list() []string {
	n := int(r.u16())
	if n > maxItems {
		r.err = ErrBounds
		return nil
	}
	v := make([]string, n)
	for i := range v {
		v[i] = r.str()
	}
	return v
}
func (r *reader) blob() ([]byte, bool) {
	n := r.i32()
	if n == -1 {
		return nil, true
	}
	if n < 0 {
		r.err = ErrProtocol
		return nil, false
	}
	return r.take(int(n)), false
}
func (r *reader) done() error {
	if r.err != nil {
		return r.err
	}
	if r.pos != len(r.data) {
		return ErrProtocol
	}
	return nil
}

type writer struct{ bytes.Buffer }

func (w *writer) u16(n uint16) { var b [2]byte; binary.BigEndian.PutUint16(b[:], n); w.Write(b[:]) }
func (w *writer) i32(n int32) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(n))
	w.Write(b[:])
}
func (w *writer) str(s string)  { w.u16(uint16(len(s))); w.WriteString(s) }
func (w *writer) long(s string) { w.i32(int32(len(s))); w.WriteString(s) }
func (w *writer) blob(b []byte) { w.i32(int32(len(b))); w.Write(b) }

type frame struct {
	opcode byte
	flags  byte
	body   []byte
}

func readFrame(conn io.Reader) (frame, error) {
	var h [9]byte
	if _, err := io.ReadFull(conn, h[:]); err != nil {
		return frame{}, err
	}
	if h[0] != 0x84 || int16(binary.BigEndian.Uint16(h[2:4])) != 0 || h[1]&^byte(8) != 0 {
		return frame{}, ErrProtocol
	}
	n := int32(binary.BigEndian.Uint32(h[5:]))
	if n < 0 || n > MaxFrame {
		return frame{}, ErrBounds
	}
	b := make([]byte, int(n))
	if _, err := io.ReadFull(conn, b); err != nil {
		return frame{}, err
	}
	return frame{h[4], h[1], b}, nil
}
func writeFrame(conn io.Writer, opcode byte, b []byte) error {
	if len(b) > MaxFrame {
		return ErrBounds
	}
	var h [9]byte
	h[0] = 4
	h[4] = opcode
	binary.BigEndian.PutUint32(h[5:], uint32(len(b)))
	payload := make([]byte, 9+len(b))
	copy(payload, h[:])
	copy(payload[9:], b)
	defer clear(payload)
	// A short write cannot be retried: the original server may have accepted a
	// complete mutation. net.Conn promises an error for short writes.
	n, err := conn.Write(payload)
	if err != nil {
		return err
	}
	if n != len(payload) {
		return io.ErrShortWrite
	}
	return nil
}
func frameReader(f frame) (*reader, []string, error) {
	r := &reader{data: f.body}
	var warnings []string
	if f.flags&8 != 0 {
		warnings = r.list()
	}
	return r, warnings, r.err
}

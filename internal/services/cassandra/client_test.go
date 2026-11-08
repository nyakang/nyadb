package cassandra

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestNativeDecoderPreservesNullMissingEmptyAndTypedMapKeys(t *testing.T) {
	typ := &Type{Code: 0x30, Fields: []Field{{"empty", &Type{Code: 0xa}}, {"null", &Type{Code: 0xa}}, {"missing", &Type{Code: 9}}}}
	w := &writer{}
	w.blob([]byte{})
	w.i32(-1)
	r := &reader{}
	v := r.value(typ, w.Bytes(), false, 0)
	if r.err != nil || len(v.Fields) != 3 || v.Fields[0].Value.Null || v.Fields[0].Value.Missing || !v.Fields[1].Value.Null || v.Fields[1].Value.Missing || !v.Fields[2].Value.Missing || v.Fields[2].Value.Null {
		t.Fatalf("UDT: %+v %v", v, r.err)
	}
	tuple := &Type{Code: 0x31, Arguments: []*Type{{Code: 0xa}, {Code: 0xa}, {Code: 9}}}
	v = r.value(tuple, w.Bytes(), false, 0)
	if r.err != nil || v.Items[0].Null || !v.Items[1].Null || !v.Items[2].Missing {
		t.Fatal("tuple null/empty/missing flattened")
	}
	mapType := &Type{Code: 0x21, Arguments: []*Type{{Code: 9}, {Code: 0xa}}}
	w = &writer{}
	w.i32(1)
	key := &writer{}
	key.i32(-129)
	w.blob(key.Bytes())
	w.blob([]byte("native"))
	v = r.value(mapType, w.Bytes(), false, 0)
	if r.err != nil || v.Entries[0].Key.Text != "-129" || v.Entries[0].Key.Type.Code != 9 || v.Entries[0].Value.Text != "native" {
		t.Fatal("typed map key lost")
	}
	clone := v.Clone()
	clone.Entries[0].Key.Raw[0] ^= 1
	clone.Type.Arguments[0].Code = 0xa
	if bytes.Equal(clone.Entries[0].Key.Raw, v.Entries[0].Key.Raw) || v.Type.Arguments[0].Code != 9 {
		t.Fatal("clone retained mutable bytes or types")
	}
}

func TestNativeDecoderExactNumbersDurationAndMalformedBounds(t *testing.T) {
	rawDuration := &reader{}
	custom := rawDuration.value(&Type{Code: 0, Custom: "org.apache.cassandra.db.marshal.DurationType"}, []byte{2, 4, 6}, false, 0)
	if rawDuration.err != nil || custom.Text != "1mo2d3ns" || custom.Type.Code != 0 {
		t.Fatal("protocol v4 custom duration lost")
	}
	for _, spec := range []struct {
		code uint16
		raw  []byte
		text string
	}{
		{0xe, []byte{0xff, 0x7f}, "-129"}, {0xe, []byte{0, 0x80}, "128"},
		{6, []byte{0, 0, 0, 3, 0xff, 0x7f}, "-0.129"},
		{6, []byte{0xff, 0xff, 0xff, 0xfe, 123}, "12300"},
		{6, []byte{0, 0, 0x20, 0, 123}, "123e-8192"},
		{0, []byte{2, 4, 6}, "1mo2d3ns"}, {0, []byte{1, 3, 5}, "-1mo2d3ns"},
	} {
		r := &reader{}
		v := r.value(&Type{Code: spec.code, Custom: "org.apache.cassandra.db.marshal.DurationType"}, spec.raw, false, 0)
		if r.err != nil || v.Text != spec.text {
			t.Fatalf("code %x got %q: %v", spec.code, v.Text, r.err)
		}
	}
	for _, spec := range []struct {
		typ *Type
		raw []byte
	}{
		{&Type{Code: 9}, []byte{1}}, {&Type{Code: 4}, []byte{2}}, {&Type{Code: 0xa}, []byte{0xff}},
		{&Type{Code: 0, Custom: "org.apache.cassandra.db.marshal.DurationType"}, []byte{1, 2, 0}}, {&Type{Code: 0, Custom: "org.apache.cassandra.db.marshal.DurationType"}, []byte{0xff}},
		{&Type{Code: 0xe}, make([]byte, 4097)}, {&Type{Code: 6}, make([]byte, 4097)},
		{&Type{Code: 0x20, Arguments: []*Type{{Code: 9}}}, []byte{0x7f, 0xff, 0xff, 0xff}},
		{&Type{Code: 0x20, Arguments: []*Type{{Code: 9}}}, []byte{0, 0, 0, 1, 0xff, 0xff, 0xff, 0xff}},
	} {
		r := &reader{}
		r.value(spec.typ, spec.raw, false, 0)
		if r.err == nil {
			t.Fatalf("accepted malformed type %x", spec.typ.Code)
		}
	}
	w := &writer{}
	for range maxDepth + 2 {
		w.u16(0x20)
	}
	w.u16(9)
	r := &reader{data: w.Bytes()}
	r.typ(0)
	if !errors.Is(r.err, ErrBounds) {
		t.Fatal("unbounded metadata nesting")
	}
	r = &reader{cells: maxCells - 1}
	r.value(&Type{Code: 0x31, Arguments: []*Type{{Code: 9}}}, []byte{}, false, 0)
	if !errors.Is(r.err, ErrBounds) {
		t.Fatal("missing fields bypassed cell bound")
	}
}

func TestNativeFramesRejectUnsupportedFlagsBoundsAndShortWrites(t *testing.T) {
	for _, spec := range []struct {
		version, flags byte
		stream         uint16
		size           uint32
	}{
		{0x83, 0, 0, 0}, {0x84, 1, 0, 0}, {0x84, 2, 0, 0}, {0x84, 4, 0, 0}, {0x84, 0, 1, 0}, {0x84, 0, 0, MaxFrame + 1}, {0x84, 0, 0, 0xffffffff},
	} {
		h := make([]byte, 9)
		h[0] = spec.version
		h[1] = spec.flags
		binary.BigEndian.PutUint16(h[2:], spec.stream)
		binary.BigEndian.PutUint32(h[5:], spec.size)
		if _, err := readFrame(bytes.NewReader(h)); err == nil {
			t.Fatal("accepted unsupported frame")
		}
	}
	w := &writer{}
	w.i32(0x2000)
	w.str("secret query reason")
	w.WriteByte(0)
	if _, _, err := decodeResult(frame{opcode: 0, body: w.Bytes()}); !errors.Is(err, ErrProtocol) {
		t.Fatal("accepted trailing error bytes")
	}
	w = &writer{}
	w.i32(0x2000)
	w.str("secret query reason")
	_, _, err := decodeResult(frame{opcode: 0, body: w.Bytes()})
	var native *ServerError
	if !errors.As(err, &native) || strings.Contains(err.Error(), "secret") {
		t.Fatal("native reason leaked")
	}
	if err := writeFrame(shortWriter{}, 7, []byte("mutation")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("short mutation write not rejected")
	}
}

type shortWriter struct{}

func (shortWriter) Write(b []byte) (int, error) { return len(b) - 1, nil }

func TestNativeCancellationWaitsAndRetiresOriginalSocket(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	c := &Client{conn: client, closeDone: make(chan struct{})}
	defer c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		header := make([]byte, 9)
		_, err := io.ReadFull(server, header)
		if err == nil {
			body := make([]byte, binary.BigEndian.Uint32(header[5:]))
			_, _ = io.ReadFull(server, body)
		}
		close(started)
	}()
	go func() {
		_, err := c.Execute(ctx, Request{Statement: "UPDATE original_counter SET value=value+1 WHERE id=1"})
		done <- err
	}()
	<-started
	if _, err := c.Execute(context.Background(), Request{Statement: "SELECT now() FROM system.local"}); !errors.Is(err, ErrBusy) {
		t.Fatal("concurrent request accepted")
	}
	cancel()
	select {
	case err := <-done:
		var interrupted *Interrupted
		if !errors.As(err, &interrupted) || !interrupted.WriteOutcomeUnknown || !errors.Is(err, context.Canceled) || c.Alive() {
			t.Fatalf("cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not acknowledge local worker")
	}
	if _, err := c.Execute(context.Background(), Request{Statement: "SELECT now() FROM system.local"}); !errors.Is(err, ErrRetired) {
		t.Fatal("retired socket reused")
	}
	closed := make(chan struct{})
	go func() { c.Close(); c.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("close deadlocked")
	}
}

func TestNativePrecancelAndInvalidRequestNeverReachSocket(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	c := &Client{conn: client, closeDone: make(chan struct{})}
	defer c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Execute(ctx, Request{Statement: "UPDATE counter SET n=n+1"}); !errors.Is(err, context.Canceled) || !c.Alive() {
		t.Fatal("pre-cancel retired original socket")
	}
	for _, r := range []Request{{Statement: ""}, {Statement: "SELECT\x00"}, {Statement: "SELECT", PageSize: -1}, {Statement: "SELECT", Consistency: 11}, {Statement: "SELECT", SerialConsistency: 1}, {Statement: "SELECT", Parameters: []Parameter{{Null: true, Unset: true}}}} {
		if _, err := c.Execute(context.Background(), r); err == nil || !c.Alive() {
			t.Fatal("invalid request reached socket")
		}
	}
}

//go:build linux || darwin || freebsd || netbsd || windows

package datasources

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
	"unsafe"
)

func TestODBCReaderCancellationWaitsForDriverAndCloseReleasesHandles(t *testing.T) {
	life, stop := context.WithCancel(context.Background())
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	api := &odbcAPI{wideBytes: 2, fetch: func(uintptr) int16 { close(entered); <-release; return -1 }, cancel: func(uintptr) int16 { once.Do(func() { close(cancelled) }); return 0 }, free: func(int16, uintptr) int16 { return 0 }, disconnect: func(uintptr) int16 { return 0 }}
	s := &odbcSource{api: api, ctx: life, stop: stop, readers: map[*odbcRows]bool{}}
	r := &odbcRows{source: s, handle: 3, columns: []Column{{Name: "id"}}, types: []int16{4}}
	s.readers[r] = true
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, e := r.Next(ctx); done <- e }()
	<-entered
	cancel()
	<-cancelled
	select {
	case e := <-done:
		t.Fatal("cancellation returned before driver acknowledgement", e)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if e := <-done; !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if e := r.Close(); e != nil {
		t.Fatal(e)
	}
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	if _, e := r.Next(context.Background()); !errors.Is(e, ErrClosed) {
		t.Fatal(e)
	}
}
func TestODBCReadBinaryNullAndMultipleWideChunks(t *testing.T) {
	api := &odbcAPI{wideBytes: 2, cancel: func(uintptr) int16 { return 0 }}
	life, stop := context.WithCancel(context.Background())
	defer stop()
	s := &odbcSource{api: api, ctx: life}
	source := api.wide("中文" + string(make([]rune, 5000)))
	source = source[:len(source)-2]
	offset := 0
	api.getData = func(_ uintptr, _ uint16, typ int16, out unsafe.Pointer, size int, length *int) int16 {
		b := unsafe.Slice((*byte)(out), size)
		if typ == -2 {
			*length = 0
			return 0
		}
		remaining := len(source) - offset
		*length = remaining
		n := min(remaining, size-2)
		copy(b, source[offset:offset+n])
		offset += n
		if offset < len(source) {
			return 1
		}
		return 0
	}
	v, e := s.data(context.Background(), 1, 1, 12)
	expected, decodeErr := api.text(source)
	if e != nil || decodeErr != nil || v.Null || v.Text != expected {
		t.Fatal(v, e)
	}
	v, e = s.data(context.Background(), 1, 1, -3)
	if e != nil || !v.Binary || v.Bytes == nil || len(v.Bytes) != 0 {
		t.Fatal(v, e)
	}
	api.getData = func(uintptr, uint16, int16, unsafe.Pointer, int, *int) int16 { return sqlNoData }
	v, e = s.data(context.Background(), 1, 1, -3)
	if e != nil || !v.Binary {
		t.Fatal(v, e)
	}
	_ = io.EOF
}

func TestODBCRejectsMalformedWideValuesInsteadOfReplacingCharacters(t *testing.T) {
	for _, b := range [][]byte{{0x00, 0xd8}, {0x00, 0xdc}, {0x00, 0xd8, 0x61, 0x00}, {0x61}} {
		if _, e := (&odbcAPI{wideBytes: 2}).text(b); !errors.Is(e, ErrInvalidSource) {
			t.Fatal("invalid UTF-16 silently converted", b, e)
		}
	}
	for _, b := range [][]byte{{0x00, 0xd8, 0x00, 0x00}, {0x00, 0x00, 0x11, 0x00}} {
		if _, e := (&odbcAPI{wideBytes: 4}).text(b); !errors.Is(e, ErrInvalidSource) {
			t.Fatal("invalid UTF-32 silently converted", b, e)
		}
	}
	a := &odbcAPI{wideBytes: 2}
	if value, e := a.text(a.wide("中文😀")[:8]); e != nil || value != "中文😀" {
		t.Fatal(value, e)
	}
}

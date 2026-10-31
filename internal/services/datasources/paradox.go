//go:build linux || darwin || freebsd || netbsd || windows

package datasources

import (
	"context"
	"encoding/binary"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
	"unsafe"

	"github.com/ebitengine/purego"
)

func paradoxLibraryNames() []string {
	switch runtime.GOOS {
	case "windows":
		return []string{"pxlib.dll", "libpx.dll"}
	case "darwin":
		return []string{"libpx.0.dylib", "libpx.dylib", "/opt/homebrew/lib/libpx.dylib", "/usr/local/lib/libpx.dylib"}
	default:
		return []string{"libpx.so.0", "libpx.so"}
	}
}

type paradoxAPI struct {
	library, libc         uintptr
	new                   func(uintptr, uintptr, uintptr, uintptr, uintptr) uintptr
	open                  func(uintptr, string) int32
	close, delete         func(uintptr)
	fields, records, size func(uintptr) int32
	field                 func(uintptr, int32) unsafe.Pointer
	record                func(uintptr, int32, unsafe.Pointer, *int32, uintptr) unsafe.Pointer
	encoding              func(uintptr, string) int32
	blobFile              func(uintptr, string) int32
	alpha, bcd            func(uintptr, unsafe.Pointer, int32, *unsafe.Pointer) int32
	bytes                 func(uintptr, unsafe.Pointer, int32, *unsafe.Pointer) int32
	double                func(uintptr, unsafe.Pointer, int32, *float64) int32
	long                  func(uintptr, unsafe.Pointer, int32, *int64) int32
	short                 func(uintptr, unsafe.Pointer, int32, *int16) int32
	byte                  func(uintptr, unsafe.Pointer, int32, *int8) int32
	blob                  func(uintptr, unsafe.Pointer, int32, *int32, *int32, *unsafe.Pointer) int32
	free                  func(unsafe.Pointer)
}

var paradoxErrorHandler = sync.OnceValue(func() uintptr {
	// libpx uses a C callback returning void. Windows requires a uintptr-sized
	// result in the Go adapter; libpx ignores it. Register only on first use,
	// and reuse the callback because native callback slots are never released.
	return purego.NewCallback(func(purego.CDecl, uintptr, int32, uintptr, uintptr) uintptr { return 0 })
})

func loadParadox(c Config) (*paradoxAPI, error) {
	names := paradoxLibraryNames()
	if c.LibraryPath != "" {
		names = []string{c.LibraryPath}
	}
	for _, name := range names {
		h, e := openLibrary(name)
		if e != nil {
			continue
		}
		a := &paradoxAPI{library: h}
		bindings := []struct {
			ptr  any
			name string
		}{{&a.new, "PX_new3"}, {&a.open, "PX_open_file"}, {&a.close, "PX_close"}, {&a.delete, "PX_delete"}, {&a.fields, "PX_get_num_fields"}, {&a.records, "PX_get_num_records"}, {&a.size, "PX_get_recordsize"}, {&a.field, "PX_get_field"}, {&a.record, "PX_get_record2"}, {&a.encoding, "PX_set_targetencoding"}, {&a.blobFile, "PX_set_blob_file"}, {&a.alpha, "PX_get_data_alpha"}, {&a.bytes, "PX_get_data_bytes"}, {&a.bcd, "PX_get_data_bcd"}, {&a.double, "PX_get_data_double"}, {&a.long, "PX_get_data_long"}, {&a.short, "PX_get_data_short"}, {&a.byte, "PX_get_data_byte"}, {&a.blob, "PX_get_data_blob"}}
		ok := true
		for _, b := range bindings {
			addr, e := librarySymbol(h, b.name)
			if e != nil {
				ok = false
				break
			}
			purego.RegisterFunc(b.ptr, addr)
		}
		if ok {
			libcs := []string{"libc.so.6"}
			switch runtime.GOOS {
			case "darwin":
				libcs = []string{"/usr/lib/libSystem.B.dylib"}
			case "windows":
				libcs = []string{"msvcrt.dll"}
			case "freebsd", "netbsd":
				libcs = []string{"libc.so"}
			}
			for _, name := range libcs {
				ch, e := openLibrary(name)
				if e != nil {
					continue
				}
				address, e := librarySymbol(ch, "free")
				if e != nil {
					closeLibrary(ch)
					continue
				}
				a.libc = ch
				purego.RegisterFunc(&a.free, address)
				return a, nil
			}
		}
		closeLibrary(h)
	}
	return nil, unavailable("libpx")
}

type paradoxField struct {
	name    unsafe.Pointer
	kind    byte
	pad     [3]byte
	length  int32
	decimal int32
}
type paradoxSource struct {
	api         *paradoxAPI
	doc         uintptr
	dir         string
	table       Table
	columns     []Column
	fields      []paradoxField
	size, count int
	mu          sync.Mutex
	closed      bool
}

func copyParadoxFile(ctx context.Context, source, dest string) error {
	in, e := os.Open(source)
	if e != nil {
		return safeError("libpx", "cannot read source", ErrInvalidSource)
	}
	defer in.Close()
	out, e := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return safeError("libpx", "cannot prepare private source copy", e)
	}
	buf := make([]byte, 64<<10)
	for {
		if e = ctx.Err(); e != nil {
			out.Close()
			return e
		}
		n, re := in.Read(buf)
		if n > 0 {
			if _, e = out.Write(buf[:n]); e != nil {
				out.Close()
				return safeError("libpx", "cannot prepare complete private source copy", e)
			}
		}
		if re != nil {
			if re != io.EOF {
				out.Close()
				return safeError("libpx", "cannot read complete source", re)
			}
			break
		}
	}
	return out.Close()
}

// libpx can open files with write-capable flags. Feed it only private copies,
// including any memo sidecar, so closing the native document cannot modify the
// selected source. The original file is opened only by os.Open for copying.
func openParadox(ctx context.Context, c Config) (Source, error) {
	a, e := loadParadox(c)
	if e != nil {
		return nil, e
	}
	s := &paradoxSource{api: a, table: Table{Name: strings.TrimSuffix(filepath.Base(c.Path), filepath.Ext(c.Path))}}
	fail := func(e error) (Source, error) { s.Close(); return nil, e }
	s.dir, e = os.MkdirTemp("", "nyadb-paradox-*")
	if e != nil {
		return fail(safeError("libpx", "cannot prepare private source copy", e))
	}
	private := filepath.Join(s.dir, filepath.Base(c.Path))
	if e = copyParadoxFile(ctx, c.Path, private); e != nil {
		return fail(e)
	}
	s.doc = a.new(paradoxErrorHandler(), 0, 0, 0, 0)
	if s.doc == 0 {
		return fail(safeError("libpx", "cannot allocate reader", nil))
	}
	if a.open(s.doc, private) < 0 {
		return fail(safeError("libpx", "cannot open Paradox database", ErrInvalidSource))
	}
	a.encoding(s.doc, "UTF-8")
	for _, ext := range []string{".MB", ".mb"} {
		source := strings.TrimSuffix(c.Path, filepath.Ext(c.Path)) + ext
		if _, e = os.Stat(source); e != nil {
			continue
		}
		dest := filepath.Join(s.dir, filepath.Base(source))
		if e = copyParadoxFile(ctx, source, dest); e != nil {
			return fail(e)
		}
		if a.blobFile(s.doc, dest) < 0 {
			return fail(safeError("libpx", "cannot open complete memo sidecar", ErrInvalidSource))
		}
		break
	}
	n := int(a.fields(s.doc))
	s.count = int(a.records(s.doc))
	s.size = int(a.size(s.doc))
	if n < 1 || n > 128 || s.count < 0 || s.size < 1 || s.size > MaxRowBytes {
		return fail(ErrInvalidSource)
	}
	offset := 0
	for i := 0; i < n; i++ {
		p := a.field(s.doc, int32(i))
		if p == nil {
			return fail(ErrInvalidSource)
		}
		f := *(*paradoxField)(p)
		if f.length < 1 || int(f.length) > MaxFieldBytes || offset+int(f.length) > s.size {
			return fail(ErrInvalidSource)
		}
		name, e := nativeText(f.name, 32767)
		if e != nil {
			return fail(e)
		}
		s.fields = append(s.fields, f)
		s.columns = append(s.columns, Column{Name: name, Type: paradoxType(f.kind), Nullable: true})
		offset += int(f.length)
	}
	if e = checkedColumns(s.columns); e != nil {
		return fail(e)
	}
	return s, nil
}
func nativeText(p unsafe.Pointer, max int) (string, error) {
	if p == nil {
		return "", ErrInvalidSource
	}
	b := unsafe.Slice((*byte)(p), max+1)
	n := 0
	for n < len(b) && b[n] != 0 {
		n++
	}
	if n > max {
		return "", ErrValueLimit
	}
	text := string(b[:n])
	if !utf8.ValidString(text) {
		return "", ErrInvalidSource
	}
	return text, nil
}
func paradoxType(k byte) string {
	switch k {
	case 1, 12, 14:
		return "text"
	case 2:
		return "date"
	case 3, 4, 22:
		return "integer"
	case 5, 23:
		return "decimal"
	case 6:
		return "float"
	case 9:
		return "boolean"
	case 20:
		return "time"
	case 21:
		return "datetime"
	default:
		return "binary"
	}
}
func (s *paradoxSource) Tables(ctx context.Context) ([]Table, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if s.closed {
		return nil, ErrClosed
	}
	return []Table{s.table}, nil
}
func (s *paradoxSource) Columns(ctx context.Context, t Table) ([]Column, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if s.closed {
		return nil, ErrClosed
	}
	if t != s.table {
		return nil, ErrInvalidSource
	}
	return append([]Column(nil), s.columns...), nil
}
func (s *paradoxSource) Rows(ctx context.Context, t Table) (RowReader, error) {
	if _, e := s.Columns(ctx, t); e != nil {
		return nil, e
	}
	return &paradoxRows{source: s}, nil
}
func (s *paradoxSource) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.doc != 0 {
		s.api.close(s.doc)
		s.api.delete(s.doc)
		s.doc = 0
	}
	if s.api != nil {
		if s.api.library != 0 {
			closeLibrary(s.api.library)
			s.api.library = 0
		}
		if s.api.libc != 0 {
			closeLibrary(s.api.libc)
			s.api.libc = 0
		}
	}
	if s.dir != "" {
		_ = os.RemoveAll(s.dir)
		s.dir = ""
	}
	return nil
}

type paradoxRows struct {
	source *paradoxSource
	index  int
	closed bool
}

func (r *paradoxRows) Columns() []Column { return append([]Column(nil), r.source.columns...) }
func (r *paradoxRows) Next(ctx context.Context) ([]Value, error) {
	s := r.source
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || r.closed {
		return nil, ErrClosed
	}
	for r.index < s.count {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		buf := make([]byte, s.size)
		var deleted int32
		if s.api.record(s.doc, int32(r.index), ptr(buf), &deleted, 0) == nil {
			return nil, safeError("libpx", "cannot read complete source record", ErrInvalidSource)
		}
		r.index++
		if deleted != 0 {
			continue
		}
		offset := 0
		row := make([]Value, len(s.fields))
		for i, f := range s.fields {
			data := buf[offset : offset+int(f.length)]
			v, e := s.value(f, data)
			if e != nil {
				return nil, e
			}
			row[i] = v
			offset += int(f.length)
		}
		if e := checkRow(row); e != nil {
			return nil, e
		}
		return row, nil
	}
	return nil, io.EOF
}
func (r *paradoxRows) Close() error {
	r.source.mu.Lock()
	defer r.source.mu.Unlock()
	r.closed = true
	return nil
}
func (s *paradoxSource) value(f paradoxField, b []byte) (Value, error) {
	a := s.api
	var rc int32
	var v Value
	var p unsafe.Pointer
	var number float64
	var integer int64
	var short int16
	var flag int8
	switch f.kind {
	case 1:
		rc = a.alpha(s.doc, ptr(b), f.length, &p)
		if rc > 0 {
			defer a.free(p)
			text, e := nativeText(p, MaxFieldBytes)
			if e != nil {
				return v, e
			}
			v.Text = text
		}
	case 3:
		rc = a.short(s.doc, ptr(b), f.length, &short)
		v.Text = strconv.FormatInt(int64(short), 10)
	case 2, 4, 20, 22:
		rc = a.long(s.doc, ptr(b), f.length, &integer)
		if rc == 0 {
			return Value{Null: true}, nil
		}
		if rc < 0 {
			return Value{}, ErrInvalidSource
		}
		if runtime.GOOS == "windows" {
			integer = int64(int32(integer))
		}
		if integer == -2147483648 {
			return Value{Null: true}, nil
		}
		switch f.kind {
		case 2:
			if integer < 1 || integer > 3652059 {
				return v, ErrInvalidSource
			}
			v.Text = time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, int(integer)-1).Format("2006-01-02")
		case 20:
			if integer < 0 || integer >= 86400000 {
				return v, ErrInvalidSource
			}
			v.Text = time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(integer) * time.Millisecond).Format("15:04:05.000")
		default:
			v.Text = strconv.FormatInt(integer, 10)
		}
	case 5, 6, 21:
		rc = a.double(s.doc, ptr(b), f.length, &number)
		if rc == 0 {
			return Value{Null: true}, nil
		}
		if rc < 0 {
			return Value{}, ErrInvalidSource
		}
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return v, ErrInvalidSource
		}
		if f.kind == 21 {
			days := math.Floor(number / 86400000)
			millis := number - days*86400000
			if days < 0 || days > 3652058 {
				return v, ErrInvalidSource
			}
			v.Text = time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, int(days)).Add(time.Duration(math.Round(millis)) * time.Millisecond).Format("2006-01-02 15:04:05.000")
		} else {
			v.Text = strconv.FormatFloat(number, 'g', -1, 64)
		}
	case 9:
		rc = a.byte(s.doc, ptr(b), f.length, &flag)
		v.Text = strconv.FormatInt(int64(flag), 10)
	case 23:
		rc = a.bcd(s.doc, ptr(b), f.length, &p)
		if rc > 0 {
			defer a.free(p)
			text, e := nativeText(p, 256)
			if e != nil {
				return v, e
			}
			v.Text = text
		}
	case 24:
		rc = a.bytes(s.doc, ptr(b), f.length, &p)
		if rc > 0 {
			defer a.free(p)
			v.Binary = true
			v.Bytes = append([]byte{}, unsafe.Slice((*byte)(p), int(f.length))...)
		}
	case 12, 13, 14, 15, 16:
		if len(b) < 10 {
			return v, ErrInvalidSource
		}
		length := binary.LittleEndian.Uint32(b[len(b)-6 : len(b)-2])
		if length > MaxFieldBytes {
			return v, ErrValueLimit
		}
		var mod, size int32
		rc = a.blob(s.doc, ptr(b), f.length, &mod, &size, &p)
		if rc > 0 {
			defer a.free(p)
			if size < 0 || size > MaxFieldBytes || p == nil && size > 0 {
				return v, ErrValueLimit
			}
			data := append([]byte{}, unsafe.Slice((*byte)(p), int(size))...)
			if f.kind == 12 || f.kind == 14 {
				if !utf8.Valid(data) {
					return v, ErrInvalidSource
				}
				v.Text = string(data)
			} else {
				v.Binary = true
				v.Bytes = data
			}
		}
	default:
		return v, safeError("libpx", "unsupported Paradox field type", ErrInvalidSource)
	}
	if rc < 0 {
		return Value{}, safeError("libpx", "cannot decode complete source field", ErrInvalidSource)
	}
	if rc == 0 {
		return Value{Null: true}, nil
	}
	return v, nil
}

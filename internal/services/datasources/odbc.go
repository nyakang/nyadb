//go:build linux || darwin || freebsd || netbsd || windows

package datasources

import (
	"context"
	"github.com/ebitengine/purego"
	"io"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"
)

const (
	sqlSuccess     int16 = 0
	sqlSuccessInfo int16 = 1
	sqlNoData      int16 = 100
	sqlNullData          = -1
	sqlNoTotal           = -4
	sqlHandleEnv   int16 = 1
	sqlHandleDBC   int16 = 2
	sqlHandleStmt  int16 = 3
)

func odbcLibraryNames() []string {
	switch runtime.GOOS {
	case "windows":
		return []string{"odbc32.dll"}
	case "darwin":
		return []string{"libodbc.2.dylib", "/opt/homebrew/lib/libodbc.2.dylib", "/usr/local/lib/libodbc.2.dylib", "/usr/lib/libiodbc.dylib"}
	default:
		return []string{"libodbc.so.2", "libodbc.so"}
	}
}

type odbcAPI struct {
	library     uintptr
	wideBytes   int
	alloc       func(int16, uintptr, *uintptr) int16
	free        func(int16, uintptr) int16
	setEnv      func(uintptr, int32, uintptr, int32) int16
	setConnect  func(uintptr, int32, uintptr, int32) int16
	connect     func(uintptr, uintptr, unsafe.Pointer, int16, unsafe.Pointer, int16, *int16, uint16) int16
	connectUTF8 func(uintptr, uintptr, string, int16, unsafe.Pointer, int16, *int16, uint16) int16
	disconnect  func(uintptr) int16
	exec        func(uintptr, unsafe.Pointer, int32) int16
	execUTF8    func(uintptr, string, int32) int16
	tables      func(uintptr, unsafe.Pointer, int16, unsafe.Pointer, int16, unsafe.Pointer, int16, unsafe.Pointer, int16) int16
	describe    func(uintptr, uint16, unsafe.Pointer, int16, *int16, *int16, *uintptr, *int16, *int16) int16
	numCols     func(uintptr, *int16) int16
	fetch       func(uintptr) int16
	getData     func(uintptr, uint16, int16, unsafe.Pointer, int, *int) int16
	cancel      func(uintptr) int16
	diag        func(int16, uintptr, int16, unsafe.Pointer, *int32, unsafe.Pointer, int16, *int16) int16
	getInfo     func(uintptr, uint16, unsafe.Pointer, int16, *int16) int16
	drivers     func(uintptr, uint16, unsafe.Pointer, int16, *int16, unsafe.Pointer, int16, *int16) int16
}

func loadODBC(c Config) (*odbcAPI, error) {
	names := odbcLibraryNames()
	if c.LibraryPath != "" {
		names = []string{c.LibraryPath}
	}
	for _, name := range names {
		h, e := openLibrary(name)
		if e != nil {
			continue
		}
		a := &odbcAPI{library: h, wideBytes: 2}
		if strings.Contains(strings.ToLower(name), "iodbc") {
			a.wideBytes = 4
		}
		bindings := []struct {
			ptr  any
			name string
		}{{&a.alloc, "SQLAllocHandle"}, {&a.free, "SQLFreeHandle"}, {&a.setEnv, "SQLSetEnvAttr"}, {&a.setConnect, "SQLSetConnectAttr"}, {&a.connect, "SQLDriverConnectW"}, {&a.connectUTF8, "SQLDriverConnect"}, {&a.disconnect, "SQLDisconnect"}, {&a.exec, "SQLExecDirectW"}, {&a.execUTF8, "SQLExecDirect"}, {&a.tables, "SQLTablesW"}, {&a.describe, "SQLDescribeColW"}, {&a.numCols, "SQLNumResultCols"}, {&a.fetch, "SQLFetch"}, {&a.getData, "SQLGetData"}, {&a.cancel, "SQLCancel"}, {&a.diag, "SQLGetDiagRecW"}, {&a.getInfo, "SQLGetInfoW"}, {&a.drivers, "SQLDriversW"}}
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
			return a, nil
		}
		_ = closeLibrary(h)
	}
	return nil, unavailable("ODBC driver manager")
}
func (a *odbcAPI) wide(s string) []byte {
	runes := []rune(s)
	if a.wideBytes == 2 {
		u := utf16.Encode(runes)
		b := make([]byte, (len(u)+1)*2)
		for i, x := range u {
			b[i*2] = byte(x)
			b[i*2+1] = byte(x >> 8)
		}
		return b
	}
	b := make([]byte, (len(runes)+1)*4)
	for i, x := range runes {
		for j := 0; j < 4; j++ {
			b[i*4+j] = byte(uint32(x) >> (8 * j))
		}
	}
	return b
}
func (a *odbcAPI) text(b []byte) (string, error) {
	if len(b)%a.wideBytes != 0 {
		return "", ErrInvalidSource
	}
	if a.wideBytes == 2 {
		u := make([]uint16, len(b)/2)
		for i := range u {
			u[i] = uint16(b[i*2]) | uint16(b[i*2+1])<<8
		}
		for i := 0; i < len(u); i++ {
			if u[i] >= 0xd800 && u[i] <= 0xdbff {
				if i+1 == len(u) || u[i+1] < 0xdc00 || u[i+1] > 0xdfff {
					return "", ErrInvalidSource
				}
				i++
			} else if u[i] >= 0xdc00 && u[i] <= 0xdfff {
				return "", ErrInvalidSource
			}
		}
		return string(utf16.Decode(u)), nil
	}
	r := make([]rune, len(b)/4)
	for i := range r {
		for j := 0; j < 4; j++ {
			r[i] |= rune(b[i*4+j]) << (8 * j)
		}
		if r[i] < 0 || r[i] > 0x10ffff || r[i] >= 0xd800 && r[i] <= 0xdfff {
			return "", ErrInvalidSource
		}
	}
	return string(r), nil
}
func ptr(b []byte) unsafe.Pointer {
	if len(b) == 0 {
		return nil
	}
	return unsafe.Pointer(&b[0])
}
func success(rc int16) bool { return rc == sqlSuccess || rc == sqlSuccessInfo }
func (a *odbcAPI) err(handleType int16, h uintptr, action string) error {
	state := make([]byte, 6*a.wideBytes)
	var number int32
	var n int16
	rc := a.diag(handleType, h, 1, ptr(state), &number, nil, 0, &n)
	s := ""
	if success(rc) {
		s, _ = a.text(state[:5*a.wideBytes])
		for _, x := range s {
			if !(x >= 'A' && x <= 'Z' || x >= '0' && x <= '9') {
				s = ""
				break
			}
		}
	}
	return &Error{Component: "ODBC", Action: action, SQLState: s}
}

type odbcSource struct {
	api      *odbcAPI
	env, dbc uintptr
	ctx      context.Context
	stop     context.CancelFunc
	mu       sync.Mutex
	closed   bool
	readers  map[*odbcRows]bool
	quote    string
}

func openODBC(ctx context.Context, c Config) (Source, error) {
	if c.ConnectionString == "" || strings.ContainsRune(c.ConnectionString, 0) || !utf8.ValidString(c.ConnectionString) {
		return nil, ErrInvalidSource
	}
	a, err := loadODBC(c)
	if err != nil {
		return nil, err
	}
	life, stop := context.WithCancel(context.Background())
	s := &odbcSource{api: a, ctx: life, stop: stop, readers: map[*odbcRows]bool{}, quote: "\""}
	fail := func(e error) (Source, error) { _ = s.Close(); return nil, e }
	if !success(a.alloc(sqlHandleEnv, 0, &s.env)) {
		return fail(safeError("ODBC", "cannot allocate environment", nil))
	}
	if !success(a.setEnv(s.env, 200, 3, 0)) {
		return fail(a.err(sqlHandleEnv, s.env, "cannot configure environment"))
	}
	if !success(a.alloc(sqlHandleDBC, s.env, &s.dbc)) {
		return fail(a.err(sqlHandleEnv, s.env, "cannot allocate connection"))
	}
	if !success(a.setConnect(s.dbc, 101, 1, 0)) {
		return fail(a.err(sqlHandleDBC, s.dbc, "driver does not accept read-only access"))
	}
	seconds := uintptr(15)
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline).Seconds()
		if remaining < 1 {
			remaining = 1
		}
		seconds = uintptr(remaining)
	}
	_ = a.setConnect(s.dbc, 103, seconds, 0)
	input := a.wide(c.ConnectionString)
	if len(input)/a.wideBytes > 32767 || len(c.ConnectionString) > 32766 {
		return fail(ErrInvalidSource)
	}
	var outLength int16
	var rc int16
	if runtime.GOOS == "windows" {
		rc = a.connect(s.dbc, 0, ptr(input), int16(len(input)/a.wideBytes-1), nil, 0, &outLength, 0)
	} else {
		// Unix drivers consume connection strings in the process UTF-8 locale.
		// Some otherwise Unicode-capable drivers corrupt paths through the W API.
		rc = a.connectUTF8(s.dbc, 0, c.ConnectionString, int16(len(c.ConnectionString)), nil, 0, &outLength, 0)
	}
	runtime.KeepAlive(input)
	if !success(rc) {
		return fail(a.err(sqlHandleDBC, s.dbc, "cannot open read-only connection"))
	}
	if err = ctx.Err(); err != nil {
		return fail(err)
	}
	quote := make([]byte, 16*a.wideBytes)
	var n int16
	if success(a.getInfo(s.dbc, 29, ptr(quote), int16(len(quote)), &n)) && n > 0 && int(n) <= len(quote) {
		decoded, e := a.text(quote[:n])
		if e != nil {
			return fail(e)
		}
		s.quote = strings.TrimSpace(decoded)
		if s.quote == "" {
			return fail(safeError("ODBC", "driver cannot quote identifiers", nil))
		}
	}
	return s, nil
}
func (s *odbcSource) statement() (uintptr, error) {
	if s.closed {
		return 0, ErrClosed
	}
	var h uintptr
	if !success(s.api.alloc(sqlHandleStmt, s.dbc, &h)) {
		return 0, s.api.err(sqlHandleDBC, s.dbc, "cannot allocate reader")
	}
	return h, nil
}
func (s *odbcSource) call(ctx context.Context, h uintptr, action string, fn func() int16) (int16, error) {
	if e := ctx.Err(); e != nil {
		return 0, e
	}
	if s.ctx.Err() != nil {
		return 0, ErrClosed
	}
	done := make(chan struct{})
	ack := make(chan struct{})
	go func() {
		defer close(ack)
		select {
		case <-done:
			return
		case <-ctx.Done():
		case <-s.ctx.Done():
		}
		s.api.cancel(h)
	}()
	rc := fn()
	close(done)
	<-ack
	if e := ctx.Err(); e != nil {
		return rc, e
	}
	if s.ctx.Err() != nil {
		return rc, ErrClosed
	}
	if !success(rc) && rc != sqlNoData {
		return rc, s.api.err(sqlHandleStmt, h, action)
	}
	return rc, nil
}
func (s *odbcSource) qualified(t Table) (string, error) {
	if e := validateTable(t); e != nil {
		return "", e
	}
	q := s.quote
	end := q
	if q == "[" {
		end = "]"
	}
	quote := func(v string) string { return q + strings.ReplaceAll(v, end, end+end) + end }
	parts := []string{}
	for _, v := range []string{t.Catalog, t.Schema, t.Name} {
		if v != "" {
			parts = append(parts, quote(v))
		}
	}
	return strings.Join(parts, "."), nil
}
func (s *odbcSource) columns(h uintptr) ([]Column, []int16, error) {
	var count int16
	if !success(s.api.numCols(h, &count)) {
		return nil, nil, s.api.err(sqlHandleStmt, h, "cannot read source fields")
	}
	if count < 1 || count > 128 {
		return nil, nil, ErrInvalidSource
	}
	cols := make([]Column, count)
	types := make([]int16, count)
	for i := range cols {
		buf := make([]byte, 32768*s.api.wideBytes)
		var n, typ, scale, nullable int16
		var size uintptr
		if !success(s.api.describe(h, uint16(i+1), ptr(buf), 32767, &n, &typ, &size, &scale, &nullable)) {
			return nil, nil, s.api.err(sqlHandleStmt, h, "cannot describe source field")
		}
		if n < 0 || int(n)*s.api.wideBytes >= len(buf) {
			return nil, nil, ErrInvalidSource
		}
		name, e := s.api.text(buf[:int(n)*s.api.wideBytes])
		if e != nil {
			return nil, nil, e
		}
		cols[i] = Column{Name: name, Type: odbcType(typ), Nullable: nullable != 0}
		types[i] = typ
	}
	if e := checkedColumns(cols); e != nil {
		return nil, nil, e
	}
	return cols, types, nil
}
func odbcType(t int16) string {
	switch t {
	case -2, -3, -4:
		return "binary"
	case -7:
		return "boolean"
	case -6, 4, 5, -5:
		return "integer"
	case 2, 3:
		return "decimal"
	case 6, 7, 8:
		return "float"
	case 9, 91:
		return "date"
	case 10, 92:
		return "time"
	case 93, 11:
		return "datetime"
	default:
		return "text"
	}
}
func (s *odbcSource) data(ctx context.Context, h uintptr, index uint16, typ int16) (Value, error) {
	v := Value{Binary: typ == -2 || typ == -3 || typ == -4}
	bufferSize := 8192
	ct := int16(-8)
	terminator := s.api.wideBytes
	if v.Binary {
		ct = -2
		terminator = 0
	}
	var data []byte
	for {
		buf := make([]byte, bufferSize)
		var length int
		rc, callErr := s.call(ctx, h, "cannot read complete source value", func() int16 { return s.api.getData(h, index, ct, ptr(buf), len(buf), &length) })
		if callErr != nil {
			return Value{}, callErr
		}
		if length == sqlNullData {
			return Value{Null: true}, nil
		}
		if rc == sqlNoData {
			break
		}
		if !success(rc) {
			return Value{}, s.api.err(sqlHandleStmt, h, "cannot read complete source value")
		}
		n := len(buf) - terminator
		if length >= 0 && length < n {
			n = length
		}
		if length == sqlNoTotal && rc == sqlSuccess { // Drivers must supply the actual last-part length for binary data.
			if v.Binary {
				return Value{}, safeError("ODBC", "driver returned an indeterminate binary value length", nil)
			}
			n = 0
			for i := 0; i < len(buf)-terminator; i += terminator {
				zero := true
				for j := 0; j < terminator; j++ {
					zero = zero && buf[i+j] == 0
				}
				if zero {
					break
				}
				n += terminator
			}
		}
		if n < 0 || n > len(buf) {
			return Value{}, ErrInvalidSource
		}
		data = append(data, buf[:n]...)
		if len(data) > MaxFieldBytes*2+s.api.wideBytes {
			return Value{}, ErrValueLimit
		}
		if rc == sqlSuccess {
			break
		}
	}
	if v.Binary {
		v.Bytes = append([]byte{}, data...)
	} else {
		if len(data)%s.api.wideBytes != 0 {
			return Value{}, ErrInvalidSource
		}
		var e error
		v.Text, e = s.api.text(data)
		if e != nil {
			return Value{}, e
		}
	}
	if len(v.Text)+len(v.Bytes) > MaxFieldBytes {
		return Value{}, ErrValueLimit
	}
	return v, nil
}
func (s *odbcSource) Tables(ctx context.Context) ([]Table, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, e := s.statement()
	if e != nil {
		return nil, e
	}
	defer s.api.free(sqlHandleStmt, h)
	pattern := s.api.wide("%")
	types := s.api.wide("TABLE,VIEW")
	if _, e = s.call(ctx, h, "cannot enumerate source tables", func() int16 { return s.api.tables(h, nil, 0, nil, 0, ptr(pattern), 1, ptr(types), 10) }); e != nil {
		return nil, e
	}
	out := []Table{}
	for {
		rc, e := s.call(ctx, h, "cannot enumerate source tables", func() int16 { return s.api.fetch(h) })
		if e != nil {
			return nil, e
		}
		if rc == sqlNoData {
			break
		}
		v := make([]string, 3)
		for i := range v {
			value, e := s.data(ctx, h, uint16(i+1), 12)
			if e != nil {
				return nil, e
			}
			v[i] = value.Text
		}
		t := Table{v[0], v[1], v[2]}
		if validateTable(t) != nil {
			return nil, ErrInvalidSource
		}
		out = append(out, t)
		if len(out) > 10000 {
			return nil, ErrValueLimit
		}
	}
	return out, nil
}
func (s *odbcSource) Columns(ctx context.Context, t Table) ([]Column, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, e := s.statement()
	if e != nil {
		return nil, e
	}
	defer s.api.free(sqlHandleStmt, h)
	name, e := s.qualified(t)
	if e != nil {
		return nil, e
	}
	queryText := "SELECT * FROM " + name + " WHERE 1=0"
	query := s.api.wide(queryText)
	if _, e = s.call(ctx, h, "cannot inspect source fields", func() int16 {
		if runtime.GOOS != "windows" {
			return s.api.execUTF8(h, queryText, int32(len(queryText)))
		}
		return s.api.exec(h, ptr(query), int32(len(query)/s.api.wideBytes-1))
	}); e != nil {
		return nil, e
	}
	cols, _, e := s.columns(h)
	return cols, e
}
func (s *odbcSource) Rows(ctx context.Context, t Table) (RowReader, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, e := s.statement()
	if e != nil {
		return nil, e
	}
	name, e := s.qualified(t)
	if e != nil {
		s.api.free(sqlHandleStmt, h)
		return nil, e
	}
	queryText := "SELECT * FROM " + name
	query := s.api.wide(queryText)
	if _, e = s.call(ctx, h, "cannot read source table", func() int16 {
		if runtime.GOOS != "windows" {
			return s.api.execUTF8(h, queryText, int32(len(queryText)))
		}
		return s.api.exec(h, ptr(query), int32(len(query)/s.api.wideBytes-1))
	}); e != nil {
		s.api.free(sqlHandleStmt, h)
		return nil, e
	}
	cols, types, e := s.columns(h)
	if e != nil {
		s.api.free(sqlHandleStmt, h)
		return nil, e
	}
	r := &odbcRows{source: s, handle: h, columns: cols, types: types}
	s.readers[r] = true
	return r, nil
}
func (s *odbcSource) Close() error {
	s.stop()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	for r := range s.readers {
		if r.handle != 0 {
			s.api.free(sqlHandleStmt, r.handle)
			r.handle = 0
		}
	}
	s.readers = nil
	if s.dbc != 0 {
		s.api.disconnect(s.dbc)
		s.api.free(sqlHandleDBC, s.dbc)
		s.dbc = 0
	}
	if s.env != 0 {
		s.api.free(sqlHandleEnv, s.env)
		s.env = 0
	}
	if s.api.library != 0 {
		_ = closeLibrary(s.api.library)
		s.api.library = 0
	}
	return nil
}

type odbcRows struct {
	source  *odbcSource
	handle  uintptr
	columns []Column
	types   []int16
}

func (r *odbcRows) Columns() []Column { return append([]Column(nil), r.columns...) }
func (r *odbcRows) Next(ctx context.Context) ([]Value, error) {
	s := r.source
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.handle == 0 || s.closed {
		return nil, ErrClosed
	}
	rc, e := s.call(ctx, r.handle, "cannot read source row", func() int16 { return s.api.fetch(r.handle) })
	if e != nil {
		return nil, e
	}
	if rc == sqlNoData {
		return nil, io.EOF
	}
	row := make([]Value, len(r.columns))
	for i := range row {
		if e = ctx.Err(); e != nil {
			return nil, e
		}
		if row[i], e = s.data(ctx, r.handle, uint16(i+1), r.types[i]); e != nil {
			return nil, e
		}
	}
	if e = checkRow(row); e != nil {
		return nil, e
	}
	return row, nil
}
func (r *odbcRows) Close() error {
	s := r.source
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.handle != 0 {
		s.api.cancel(r.handle)
		s.api.free(sqlHandleStmt, r.handle)
		r.handle = 0
	}
	delete(s.readers, r)
	return nil
}

// Drivers lists native driver names without exposing driver attributes or DSNs.
func Drivers(ctx context.Context, c Config) ([]string, error) {
	a, e := loadODBC(c)
	if e != nil {
		return nil, e
	}
	defer closeLibrary(a.library)
	var env uintptr
	if !success(a.alloc(sqlHandleEnv, 0, &env)) {
		return nil, safeError("ODBC", "cannot allocate environment", nil)
	}
	defer a.free(sqlHandleEnv, env)
	if !success(a.setEnv(env, 200, 3, 0)) {
		return nil, a.err(sqlHandleEnv, env, "cannot configure environment")
	}
	out := []string{}
	direction := uint16(2)
	for {
		if e = ctx.Err(); e != nil {
			return nil, e
		}
		b := make([]byte, 32768*a.wideBytes)
		var n, attrs int16
		rc := a.drivers(env, direction, ptr(b), 32767, &n, nil, 0, &attrs)
		if rc == sqlNoData {
			break
		}
		if !success(rc) {
			return nil, a.err(sqlHandleEnv, env, "cannot enumerate source drivers")
		}
		if n < 1 || int(n)*a.wideBytes >= len(b) {
			return nil, ErrInvalidSource
		}
		name, e := a.text(b[:int(n)*a.wideBytes])
		if e != nil {
			return nil, e
		}
		out = append(out, name)
		direction = 1
		if len(out) > 10000 {
			return nil, ErrValueLimit
		}
	}
	return out, nil
}

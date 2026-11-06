// Package native binds the stable DuckDB C API without CGO. Database handles
// outlive all pooled connections and results; cancellation interrupts only the
// owning connection and waits for DuckDB to acknowledge it.
package native

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

const Version = "v1.4.4"

// The supported library is the exact official build tested by this adapter.
// Check it before Dlopen: other releases can run CPU-specific initialization
// before library_version is callable (notably 2.0 on older x86 machines).
var libraryDigests = map[string]string{
	"linux-amd64":   "1ef33048e12235115ac0d277a0aaccbb560e33248144f488b5ac005cd9ba81b5",
	"linux-arm64":   "c8e20af1e0064bdb7bf79af4d16f17ee8be16803bc98a4df58588bed1301c042",
	"darwin-amd64":  "a763d7f2690b69c329fac7eecd1b50444800a3210b10ab6020723c8ecde168d3",
	"darwin-arm64":  "a763d7f2690b69c329fac7eecd1b50444800a3210b10ab6020723c8ecde168d3",
	"windows-amd64": "bc68fcd80f618374fa6f2b4fb7215fdde2977b620a8d2324713c68e26b2f54f0",
	"windows-arm64": "cffa9c989793366cbeaec2062b0cd515b67de538ce85196953a300b370893f0e",
}

func verifiedLibrary(path string) bool {
	expected := libraryDigests[runtime.GOOS+"-"+runtime.GOARCH]
	if expected == "" {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 256<<20 {
		return false
	}
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return false
	}
	return fmt.Sprintf("%x", h.Sum(nil)) == expected
}

// These layouts are the published duckdb_result and duckdb_string/duckdb_blob
// C structs. All supported release platforms use 64-bit idx_t and pointers.
type result struct {
	Columns, Rows, Changed      uint64
	ColumnData, Error, Internal uintptr
}

type api struct {
	typeAlias         func(uintptr) unsafe.Pointer
	createTimeNS      func(timeValue) uintptr
	statementType     func(uintptr) uint32
	resultType        func(result) uint32
	pendingStreaming  func(uintptr, *uintptr) uint32
	executePending    func(uintptr, *result) uint32
	destroyPending    func(*uintptr)
	fetchChunk        func(result) uintptr
	chunkSize         func(uintptr) uint64
	chunkVector       func(uintptr, uint64) uintptr
	destroyChunk      func(*uintptr)
	vectorData        func(uintptr) unsafe.Pointer
	vectorValidity    func(uintptr) unsafe.Pointer
	vectorType        func(uintptr) uintptr
	typeID            func(uintptr) uint32
	listChild         func(uintptr) uintptr
	arrayChild        func(uintptr) uintptr
	arraySize         func(uintptr) uint64
	structChild       func(uintptr, uint64) uintptr
	structCount       func(uintptr) uint64
	structName        func(uintptr, uint64) unsafe.Pointer
	enumInternal      func(uintptr) uint32
	enumValue         func(uintptr, uint64) unsafe.Pointer
	createDate        func(dateValue) uintptr
	createTime        func(timeValue) uintptr
	createTimeTZ      func(unsignedValue) uintptr
	createTimestamp   func(timeValue) uintptr
	createTimestampTZ func(timeValue) uintptr
	createTimestampS  func(timeValue) uintptr
	createTimestampMS func(timeValue) uintptr
	createTimestampNS func(timeValue) uintptr
	createInterval    func(intervalValue) uintptr
	valueToString     func(uintptr) unsafe.Pointer
	destroyValue      func(*uintptr)
	version           func() string
	free              func(unsafe.Pointer)
	createConfig      func(*uintptr) uint32
	setConfig         func(uintptr, string, string) uint32
	destroyConfig     func(*uintptr)
	open              func(string, *uintptr, uintptr, *unsafe.Pointer) uint32
	close             func(*uintptr)
	connect           func(uintptr, *uintptr) uint32
	disconnect        func(*uintptr)
	interrupt         func(uintptr)
	prepare           func(uintptr, string, *uintptr) uint32
	prepareError      func(uintptr) string
	destroyPrepared   func(*uintptr)
	nparams           func(uintptr) uint64
	bindNull          func(uintptr, uint64) uint32
	bindBool          func(uintptr, uint64, bool) uint32
	bindInt           func(uintptr, uint64, int64) uint32
	bindDouble        func(uintptr, uint64, float64) uint32
	bindString        func(uintptr, uint64, unsafe.Pointer, uint64) uint32
	bindBlob          func(uintptr, uint64, unsafe.Pointer, uint64) uint32
	destroyResult     func(*result)
	resultError       func(*result) string
	columnCount       func(*result) uint64
	rowsChanged       func(*result) uint64
	columnName        func(*result, uint64) string
	columnType        func(*result, uint64) uint32
	columnLogicalType func(*result, uint64) uintptr
	destroyType       func(*uintptr)
	decimalWidth      func(uintptr) uint8
	decimalScale      func(uintptr) uint8
}

var libraries = struct {
	sync.Mutex
	values map[string]*api
}{values: map[string]*api{}}

func libraryNames(explicit string) []string {
	if explicit != "" {
		return []string{explicit}
	}
	if p := os.Getenv("NYADB_DUCKDB_LIBRARY"); p != "" {
		return []string{p}
	}
	name := "libduckdb.so"
	if runtime.GOOS == "darwin" {
		name = "libduckdb.dylib"
	}
	if runtime.GOOS == "windows" {
		name = "duckdb.dll"
	}
	var paths []string
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		paths = append(paths, filepath.Join(dir, name), filepath.Join(dir, "runtimes", "duckdb", name))
		if runtime.GOOS == "darwin" {
			paths = append(paths, filepath.Join(dir, "..", "Frameworks", name))
		}
	}
	if cache, err := os.UserCacheDir(); err == nil {
		paths = append(paths, filepath.Join(cache, "NyaDB", "duckdb", Version, runtime.GOOS+"-"+runtime.GOARCH, name))
	}
	if runtime.GOOS != "windows" {
		paths = append(paths, filepath.Join("/usr/local/lib", name), filepath.Join("/opt/homebrew/lib", name), filepath.Join("/usr/lib", name), name)
	}
	return paths
}

func load(path string) (*api, error) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return nil, errors.New("DuckDB requires a 64-bit amd64 or arm64 runtime")
	}
	libraries.Lock()
	defer libraries.Unlock()
	for _, name := range libraryNames(path) {
		if a := libraries.values[name]; a != nil {
			return a, nil
		}
		if !verifiedLibrary(name) {
			continue
		}
		h, err := openLibrary(name)
		if err != nil {
			continue
		}
		a := &api{}
		bindings := []struct {
			target any
			name   string
		}{
			{&a.typeAlias, "duckdb_logical_type_get_alias"}, {&a.createTimeNS, "duckdb_create_time_ns"},
			{&a.statementType, "duckdb_prepared_statement_type"}, {&a.resultType, "duckdb_result_return_type"},
			{&a.pendingStreaming, "duckdb_pending_prepared_streaming"}, {&a.executePending, "duckdb_execute_pending"}, {&a.destroyPending, "duckdb_destroy_pending"}, {&a.fetchChunk, "duckdb_fetch_chunk"},
			{&a.chunkSize, "duckdb_data_chunk_get_size"}, {&a.chunkVector, "duckdb_data_chunk_get_vector"}, {&a.destroyChunk, "duckdb_destroy_data_chunk"},
			{&a.vectorData, "duckdb_vector_get_data"}, {&a.vectorValidity, "duckdb_vector_get_validity"}, {&a.vectorType, "duckdb_vector_get_column_type"}, {&a.typeID, "duckdb_get_type_id"},
			{&a.listChild, "duckdb_list_vector_get_child"}, {&a.arrayChild, "duckdb_array_vector_get_child"}, {&a.arraySize, "duckdb_array_type_array_size"}, {&a.structChild, "duckdb_struct_vector_get_child"}, {&a.structCount, "duckdb_struct_type_child_count"}, {&a.structName, "duckdb_struct_type_child_name"},
			{&a.enumInternal, "duckdb_enum_internal_type"}, {&a.enumValue, "duckdb_enum_dictionary_value"},
			{&a.createDate, "duckdb_create_date"}, {&a.createTime, "duckdb_create_time"}, {&a.createTimeTZ, "duckdb_create_time_tz_value"},
			{&a.createTimestamp, "duckdb_create_timestamp"}, {&a.createTimestampTZ, "duckdb_create_timestamp_tz"}, {&a.createTimestampS, "duckdb_create_timestamp_s"}, {&a.createTimestampMS, "duckdb_create_timestamp_ms"}, {&a.createTimestampNS, "duckdb_create_timestamp_ns"}, {&a.createInterval, "duckdb_create_interval"}, {&a.valueToString, "duckdb_value_to_string"}, {&a.destroyValue, "duckdb_destroy_value"},
			{&a.version, "duckdb_library_version"}, {&a.free, "duckdb_free"},
			{&a.createConfig, "duckdb_create_config"}, {&a.setConfig, "duckdb_set_config"}, {&a.destroyConfig, "duckdb_destroy_config"},
			{&a.open, "duckdb_open_ext"}, {&a.close, "duckdb_close"}, {&a.connect, "duckdb_connect"}, {&a.disconnect, "duckdb_disconnect"}, {&a.interrupt, "duckdb_interrupt"},
			{&a.prepare, "duckdb_prepare"}, {&a.prepareError, "duckdb_prepare_error"}, {&a.destroyPrepared, "duckdb_destroy_prepare"}, {&a.nparams, "duckdb_nparams"},
			{&a.bindNull, "duckdb_bind_null"}, {&a.bindBool, "duckdb_bind_boolean"}, {&a.bindInt, "duckdb_bind_int64"}, {&a.bindDouble, "duckdb_bind_double"},
			{&a.bindString, "duckdb_bind_varchar_length"}, {&a.bindBlob, "duckdb_bind_blob"}, {&a.destroyResult, "duckdb_destroy_result"}, {&a.resultError, "duckdb_result_error"}, {&a.columnCount, "duckdb_column_count"}, {&a.rowsChanged, "duckdb_rows_changed"},
			{&a.columnName, "duckdb_column_name"}, {&a.columnType, "duckdb_column_type"}, {&a.columnLogicalType, "duckdb_column_logical_type"}, {&a.destroyType, "duckdb_destroy_logical_type"}, {&a.decimalWidth, "duckdb_decimal_width"}, {&a.decimalScale, "duckdb_decimal_scale"},
		}
		ok := true
		for _, b := range bindings {
			addr, err := symbol(h, b.name)
			if err != nil {
				ok = false
				break
			}
			purego.RegisterFunc(b.target, addr)
		}
		if !ok || a.version() != Version {
			closeLibrary(h)
			continue
		}
		// Keep the library loaded for process lifetime: registered function
		// pointers must never reference an unloaded module.
		libraries.values[name] = a
		return a, nil
	}
	return nil, fmt.Errorf("DuckDB %s runtime is unavailable; install the NyaDB DuckDB runtime or select its library file", Version)
}

func typeName(a *api, r *result, i uint64) string {
	logical := a.columnLogicalType(r, i)
	alias := a.typeAlias(logical)
	name, _ := cstring(alias)
	a.free(alias)
	a.destroyType(&logical)
	if name == "JSON" {
		return "JSON"
	}
	// Logical complex types are deliberately shown as native text. A table's
	// exact declaration comes from duckdb_columns(), never this presentation.
	names := map[uint32]string{1: "BOOLEAN", 2: "TINYINT", 3: "SMALLINT", 4: "INTEGER", 5: "BIGINT", 6: "UTINYINT", 7: "USMALLINT", 8: "UINTEGER", 9: "UBIGINT", 10: "FLOAT", 11: "DOUBLE", 12: "TIMESTAMP", 13: "DATE", 14: "TIME", 15: "INTERVAL", 16: "HUGEINT", 17: "VARCHAR", 18: "BLOB", 20: "TIMESTAMP_S", 21: "TIMESTAMP_MS", 22: "TIMESTAMP_NS", 23: "ENUM", 24: "LIST", 25: "STRUCT", 26: "MAP", 27: "UUID", 28: "UNION", 29: "BIT", 30: "TIME WITH TIME ZONE", 31: "TIMESTAMP WITH TIME ZONE", 32: "UHUGEINT", 33: "ARRAY", 34: "ANY", 35: "BIGNUM", 36: "NULL", 39: "TIME_NS"}
	id := a.columnType(r, i)
	if id == 19 {
		t := a.columnLogicalType(r, i)
		defer a.destroyType(&t)
		return fmt.Sprintf("DECIMAL(%d,%d)", a.decimalWidth(t), a.decimalScale(t))
	}
	if n := names[id]; n != "" {
		return n
	}
	return "DUCKDB_TYPE_" + strings.TrimSpace(fmt.Sprint(id))
}

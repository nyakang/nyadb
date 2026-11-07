// Package dpi calls the published DM8 DPI interface in an explicitly supplied
// native client. Vendor headers, source and binaries are not bundled with NyaDB.
// This transport does not register workbench capabilities.
package dpi

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// These signatures and scalar values are API facts from the DM8 8.1.3.12
// DPI.h/DPIext.h/DPItypes.h distribution. DPIRETURN is signed 16-bit; slength
// and ulength are 64-bit in DM64. DSQL types are not ODBC type numbers.
type api struct {
	allocEnv    func(*uintptr) int16
	allocCon    func(uintptr, *uintptr) int16
	allocStmt   func(uintptr, *uintptr) int16
	freeEnv     func(uintptr) int16
	freeCon     func(uintptr) int16
	freeStmt    func(uintptr) int16
	setEnv      func(uintptr, int32, uintptr, int32) int16
	setCon      func(uintptr, int32, uintptr, int32) int16
	setStmt     func(uintptr, int32, uintptr, int32) int16
	getCon      func(uintptr, int32, unsafe.Pointer, int32, *int32) int16
	login       func(uintptr, string, string, string) int16
	logout      func(uintptr) int16
	prepare     func(uintptr, string) int16
	nParams     func(uintptr, *int16) int16
	bind        func(uintptr, uint16, int16, int16, int16, uint64, int16, unsafe.Pointer, int64, *int64) int16
	unBind      func(uintptr) int16
	execute     func(uintptr) int16
	execDirect  func(uintptr, string) int16
	cancel      func(uintptr) int16
	closeCursor func(uintptr) int16
	nColumns    func(uintptr, *int16) int16
	column      func(uintptr, int16, unsafe.Pointer, int16, *int16, *int16, *uint64, *int16, *int16) int16
	fetch       func(uintptr, *uint64) int16
	getData     func(uintptr, uint16, int16, unsafe.Pointer, int64, *int64) int16
	rowCount    func(uintptr, *int64) int16
	diagnostic  func(int16, uintptr, int16, *int32, unsafe.Pointer, int16, *int16) int16
}

var loaded = struct {
	sync.Mutex
	values map[string]*api
}{values: make(map[string]*api)}

// Only the actual native client used in this transport's fixture is admitted.
// Other ABIs/platforms must be independently verified before adding a digest.
var clientDigests = map[string][]string{
	"linux-amd64": {
		"6f806e005bb260594eb31a86726a178e6dfe27b10c0fd95fcc894ba118736f90",
		"84935dae2b77598ba32ae28eb12c2bdb89f09a7fe6555af3d48e59e098e5ccb3",
		"71f21a833797914960ac78b054bdf2e52d519d8d9a3119e5c7f3d02bb087c1d8",
	},
}

// VerifyLibrary checks the admitted client bytes without loading native code.
func VerifyLibrary(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("DM8 requires an explicit absolute native client path")
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 128<<20 {
		return "", errors.New("DM8 native client is unavailable")
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	if !slices.Contains(clientDigests[runtime.GOOS+"-"+runtime.GOARCH], hash) {
		return "", errors.New("DM8 native client ABI has not been verified")
	}
	return hash, nil
}

func load(path string) (*api, error) {
	hash, err := VerifyLibrary(path)
	if err != nil {
		return nil, err
	}
	loaded.Lock()
	defer loaded.Unlock()
	key := path + ":" + hash
	if a := loaded.values[key]; a != nil {
		return a, nil
	}
	h, err := openLibrary(path)
	if err != nil {
		return nil, errors.New("DM8 native client dependencies are unavailable")
	}
	a := &api{}
	bindings := []struct {
		target any
		name   string
	}{
		{&a.allocEnv, "dpi_alloc_env"}, {&a.allocCon, "dpi_alloc_con"}, {&a.allocStmt, "dpi_alloc_stmt"},
		{&a.freeEnv, "dpi_free_env"}, {&a.freeCon, "dpi_free_con"}, {&a.freeStmt, "dpi_free_stmt"},
		{&a.setEnv, "dpi_set_env_attr"}, {&a.setCon, "dpi_set_con_attr"}, {&a.setStmt, "dpi_set_stmt_attr"}, {&a.getCon, "dpi_get_con_attr"},
		{&a.login, "dpi_login"}, {&a.logout, "dpi_logout"}, {&a.prepare, "dpi_prepare"}, {&a.nParams, "dpi_number_params"},
		{&a.bind, "dpi_bind_param"}, {&a.unBind, "dpi_unbind_params"}, {&a.execute, "dpi_exec"}, {&a.execDirect, "dpi_exec_direct"},
		{&a.cancel, "dpi_cancel"}, {&a.closeCursor, "dpi_close_cursor"}, {&a.nColumns, "dpi_number_columns"},
		{&a.column, "dpi_desc_column"}, {&a.fetch, "dpi_fetch"}, {&a.getData, "dpi_get_data"},
		{&a.rowCount, "dpi_row_count"}, {&a.diagnostic, "dpi_get_diag_rec"},
	}
	for _, binding := range bindings {
		address, err := symbol(h, binding.name)
		if err != nil {
			closeLibrary(h)
			return nil, errors.New("DM8 native client API is incomplete")
		}
		purego.RegisterFunc(binding.target, address)
	}
	// Keep the library loaded while any captured physical handle can call it.
	loaded.values[key] = a
	return a, nil
}

func succeeded(rc int16) bool { return rc == 0 || rc == 1 }

// Error retains the native error code without logging DSNs, SQL or credentials
// that can be present in vendor diagnostic messages.
type Error struct {
	Operation string
	Code      int32
}

func (e *Error) Error() string { return fmt.Sprintf("DM8 %s failed (code %d)", e.Operation, e.Code) }

func (a *api) error(kind int16, handle uintptr, op string) error {
	var code int32
	var length int16
	buffer := make([]byte, 1024)
	if !succeeded(a.diagnostic(kind, handle, 1, &code, unsafe.Pointer(&buffer[0]), int16(len(buffer)), &length)) {
		code = 0
	}
	return &Error{Operation: op, Code: code}
}

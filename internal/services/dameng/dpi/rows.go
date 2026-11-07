package dpi

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"math"
	"reflect"
	"strconv"
	"unsafe"
)

type column struct {
	name            string
	kind            int16
	size            uint64
	scale, nullable int16
}
type rows struct {
	s                     *statement
	ctx                   context.Context
	columns               []column
	ownsStatement, closed bool
}

func (r *rows) Columns() []string {
	n := make([]string, len(r.columns))
	for i, c := range r.columns {
		n[i] = c.name
	}
	return n
}
func (r *rows) Close() (err error) {
	r.s.c.mu.Lock()
	defer r.s.c.mu.Unlock()
	if r.closed {
		return nil
	}
	_, finish := r.s.c.operationLocked(context.Background(), false)
	defer finish(&err)
	r.closed = true
	r.s.busy = false
	if r.s.h != 0 {
		if r.ownsStatement {
			r.s.freeLocked()
		} else if !succeeded(r.s.c.a.closeCursor(r.s.h)) {
			r.s.freeLocked()
		}
	}
	return nil
}

func (r *rows) Next(values []driver.Value) (err error) {
	c := r.s.c
	c.mu.Lock()
	defer c.mu.Unlock()
	if r.closed {
		return io.EOF
	}
	if c.h == 0 || r.s.h == 0 {
		return ErrSessionClosed
	}
	if len(values) != len(r.columns) {
		return errors.New("DM8 result value count mismatch")
	}
	ctx, finish := c.operationLocked(r.ctx, false)
	defer finish(&err)
	var count uint64
	var end bool
	err = c.runLocked(ctx, r.s, false, "fetch", func() int16 {
		rc := c.a.fetch(r.s.h, &count)
		if rc == 100 {
			end = true
			return 0
		}
		return rc
	})
	if err != nil {
		return err
	}
	if end {
		return io.EOF
	}
	for i, col := range r.columns {
		var value driver.Value
		var fieldErr error
		err = c.runLocked(ctx, r.s, false, "field read", func() int16 {
			var nativeErr int16
			value, nativeErr, fieldErr = r.readLocked(i, col)
			return nativeErr
		})
		if err != nil {
			return err
		}
		if fieldErr != nil {
			return fieldErr
		}
		values[i] = value
	}
	return nil
}

func (r *rows) readLocked(index int, col column) (driver.Value, int16, error) {
	a := r.s.c.a
	var length int64
	switch col.kind {
	case 3, 5, 6, 7, 8:
		var n int64
		rc := a.getData(r.s.h, uint16(index+1), 10, unsafe.Pointer(&n), 8, &length)
		if !succeeded(rc) {
			return nil, rc, nil
		}
		if length == -1 {
			return nil, rc, nil
		}
		return n, rc, nil
	case 10, 11:
		var n float64
		rc := a.getData(r.s.h, uint16(index+1), 6, unsafe.Pointer(&n), 8, &length)
		if !succeeded(rc) {
			return nil, rc, nil
		}
		if length == -1 {
			return nil, rc, nil
		}
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, 0, errors.New("unsupported DM8 nonfinite result")
		}
		return n, rc, nil
	case 9:
		var n numeric
		rc := a.getData(r.s.h, uint16(index+1), 16, unsafe.Pointer(&n), int64(unsafe.Sizeof(n)), &length)
		if !succeeded(rc) {
			return nil, rc, nil
		}
		if length == -1 {
			return nil, rc, nil
		}
		text, err := decodeDecimal(n)
		return text, rc, err
	case 1, 2, 12, 14, 15, 16, 17, 18, 19, 22, 23, 28:
		ctype := int16(0)
		binary := col.kind == 12 || col.kind == 17 || col.kind == 18
		if binary {
			ctype = 12
		}
		// Bounded complete reads only; no silent truncation or partial values.
		buffer := make([]byte, (1<<20)+1)
		rc := a.getData(r.s.h, uint16(index+1), ctype, unsafe.Pointer(&buffer[0]), int64(len(buffer)), &length)
		if !succeeded(rc) {
			return nil, rc, nil
		}
		if length == -1 {
			return nil, rc, nil
		}
		if rc == 1 || length < 0 || length > 1<<20 {
			return nil, 0, errors.New("DM8 field requires a bounded explicit large-value read")
		}
		if binary {
			return append([]byte{}, buffer[:length]...), rc, nil
		}
		return string(buffer[:length]), rc, nil
	default:
		return nil, 0, errors.New("unsupported DM8 native result type " + strconv.Itoa(int(col.kind)))
	}
}

func (r *rows) ColumnTypeDatabaseTypeName(i int) string {
	names := map[int16]string{1: "CHAR", 2: "VARCHAR", 3: "BIT", 5: "TINYINT", 6: "SMALLINT", 7: "INT", 8: "BIGINT", 9: "DECIMAL", 10: "REAL", 11: "DOUBLE", 12: "BLOB", 14: "DATE", 15: "TIME", 16: "TIMESTAMP", 17: "BINARY", 18: "VARBINARY", 19: "CLOB", 22: "TIME WITH TIME ZONE", 23: "TIMESTAMP WITH TIME ZONE", 28: "ROWID"}
	if n := names[r.columns[i].kind]; n != "" {
		return n
	}
	return "UNSUPPORTED"
}
func (r *rows) ColumnTypeNullable(i int) (bool, bool) {
	n := r.columns[i].nullable
	return n == 1, n == 0 || n == 1
}
func (r *rows) ColumnTypeLength(i int) (int64, bool) {
	switch r.columns[i].kind {
	case 1, 2, 12, 17, 18, 19:
		return int64(r.columns[i].size), r.columns[i].size <= math.MaxInt64
	}
	return 0, false
}
func (r *rows) ColumnTypePrecisionScale(i int) (int64, int64, bool) {
	c := r.columns[i]
	return int64(c.size), int64(c.scale), c.kind == 9
}
func (r *rows) ColumnTypeScanType(i int) reflect.Type {
	switch r.columns[i].kind {
	case 3, 5, 6, 7, 8:
		return reflect.TypeFor[int64]()
	case 10, 11:
		return reflect.TypeFor[float64]()
	case 12, 17, 18:
		return reflect.TypeFor[[]byte]()
	}
	return reflect.TypeFor[string]()
}

var _ driver.Rows = (*rows)(nil)

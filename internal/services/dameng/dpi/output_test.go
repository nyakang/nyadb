package dpi

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"unsafe"
)

func TestOutputBindingRejectsIncompleteAndLossyValuesBeforePublication(t *testing.T) {
	for _, text := range []string{"9007199254740993", "-9223372036854775808", "9223372036854775807"} {
		key := int64(42)
		b, err := newBinding(sql.Out{Dest: &key})
		if err != nil {
			t.Fatal(err)
		}
		copy(b.bytes, text)
		b.length = int64(len(text))
		set, err := b.stageOutput()
		b.release()
		if err != nil || key != 42 {
			t.Fatal("integer changed before publication", err)
		}
		set()
		if key == 42 {
			t.Fatal("integer was not published")
		}
	}
	for _, text := range []string{"1.234", "9223372036854775808", "1e3", "", "invalid"} {
		key := int64(42)
		b, _ := newBinding(sql.Out{Dest: &key})
		copy(b.bytes, text)
		b.length = int64(len(text))
		_, err := b.stageOutput()
		b.release()
		if err == nil || key != 42 {
			t.Fatal("lossy integer output accepted", text)
		}
	}
	for _, indicator := range []int64{-3, -2, MaxOutputTextBytes + 1, MaxOutputTextBytes + 2} {
		text := "original"
		b, _ := newBinding(sql.Out{Dest: &text})
		b.length = indicator
		_, err := b.stageOutput()
		b.release()
		if err == nil || text != "original" {
			t.Fatal("incomplete output accepted", indicator)
		}
	}
	for _, text := range []string{string([]byte{255}), strings.Repeat("x", MaxOutputTextBytes+1)} {
		dest := "original"
		if _, err := StageOutput(sql.Out{Dest: &dest}, text); err == nil || dest != "original" {
			t.Fatal("invalid text output accepted")
		}
	}
	for _, out := range []sql.Out{{}, {Dest: (*int64)(nil)}, {Dest: new(int)}, {Dest: new([]byte)}, {Dest: new(int64), In: true}} {
		if _, err := OutputKind(out); err == nil {
			t.Fatal("unsupported output destination accepted")
		}
	}
}

func TestOutputPublicationWaitsForOuterNativeAcknowledgement(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		for _, fail := range []string{"", "cancel-after-execute", "invalid-second-output", "rowcount-failure", "native-output-warning", "native-bind-warning"} {
			t.Run(strings.Join([]string{map[bool]string{false: "direct", true: "prepared"}[prepared], fail}, "/"), func(t *testing.T) {
				transport, _, _ := transportFixture(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				type buffer struct {
					p unsafe.Pointer
					n *int64
				}
				var buffers []buffer
				a := &api{
					allocStmt: func(_ uintptr, h *uintptr) int16 { *h = 2; return 0 },
					prepare:   func(uintptr, string) int16 { return 0 },
					nParams:   func(_ uintptr, n *int16) int16 { *n = 2; return 0 },
					setStmt:   func(uintptr, int32, uintptr, int32) int16 { return 0 },
					bind: func(_ uintptr, _ uint16, direction int16, c, d int16, _ uint64, _ int16, p unsafe.Pointer, _ int64, n *int64) int16 {
						if direction != 4 || c != 1003 || d != 2 {
							t.Fatal("unsafe native output binding")
						}
						buffers = append(buffers, buffer{p, n})
						if fail == "native-bind-warning" {
							return 1
						}
						return 0
					},
					execute: func(uintptr) int16 {
						for i, b := range buffers {
							text := "9007199254740993"
							if i == 1 && fail == "invalid-second-output" {
								text = "1.234"
							}
							copy(unsafe.Slice((*byte)(b.p), len(text)), text)
							*b.n = int64(len(text))
						}
						if fail == "native-output-warning" {
							return 1
						}
						return 0
					},
					rowCount: func(_ uintptr, n *int64) int16 {
						*n = 1
						if fail == "cancel-after-execute" {
							cancel()
						}
						if fail == "rowcount-failure" {
							return -1
						}
						return 0
					},
					unBind: func(uintptr) int16 { return 0 }, freeStmt: func(uintptr) int16 { return 0 },
					logout: func(uintptr) int16 { return 0 }, freeCon: func(uintptr) int16 { return 0 },
					diagnostic: func(_ int16, _ uintptr, _ int16, code *int32, _ unsafe.Pointer, _ int16, n *int16) int16 {
						*code = -1
						*n = 0
						return 0
					},
				}
				c := &conn{a: a, h: 1, transport: transport, options: Options{OperationTimeoutSeconds: 1}, statements: make(map[uintptr]*statement)}
				defer c.Close()
				key, other := int64(42), int64(43)
				args := []driver.NamedValue{{Ordinal: 1, Value: sql.Out{Dest: &key}}, {Ordinal: 2, Value: sql.Out{Dest: &other}}}
				var err error
				if prepared {
					var s *statement
					s, err = c.prepareLocked(ctx, "BEGIN ? := 1; ? := 2; END;")
					if err != nil {
						t.Fatal(err)
					}
					_, err = s.ExecContext(ctx, args)
				} else {
					_, err = c.ExecContext(ctx, "BEGIN ? := 1; ? := 2; END;", args)
				}
				if fail == "" {
					if err != nil || key != 9007199254740993 || other != key {
						t.Fatal("successful outputs missing", err, key, other)
					}
				} else if fail == "native-bind-warning" {
					if err == nil || errors.Is(err, ErrOutcomeUnknown) || key != 42 || other != 43 || c.h == 0 {
						t.Fatal("binding warning executed or changed outputs", err, key, other)
					}
				} else {
					if !errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, driver.ErrBadConn) || key != 42 || other != 43 || c.h != 0 {
						t.Fatal("unacknowledged outputs published", err, key, other)
					}
				}
			})
		}
	}
}

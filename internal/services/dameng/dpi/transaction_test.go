package dpi

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"
)

func TestNativeCommitRestorationFailurePreservesUnknownOutcome(t *testing.T) {
	for _, mode := range []string{"success", "restore-failure", "cancel-during-restore", "precancel"} {
		t.Run(mode, func(t *testing.T) {
			transport, _, _ := transportFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			executions, restorations := 0, 0
			a := &api{
				allocStmt: func(_ uintptr, h *uintptr) int16 { *h = 2; return 0 },
				setStmt:   func(uintptr, int32, uintptr, int32) int16 { return 0 },
				execDirect: func(_ uintptr, q string) int16 {
					if q != "COMMIT" {
						t.Fatal("unexpected native command", q)
					}
					executions++
					return 0
				},
				rowCount: func(_ uintptr, n *int64) int16 { *n = 0; return 0 },
				setCon: func(_ uintptr, attr int32, value uintptr, _ int32) int16 {
					if attr != 102 || value != 1 {
						t.Fatal("unexpected restoration")
					}
					restorations++
					if mode == "restore-failure" {
						return -1
					}
					if mode == "cancel-during-restore" {
						cancel()
					}
					return 0
				},
				freeStmt: func(uintptr) int16 { return 0 },
				logout:   func(uintptr) int16 { return 0 },
				freeCon:  func(uintptr) int16 { return 0 },
			}
			c := &conn{a: a, h: 1, transport: transport, options: Options{OperationTimeoutSeconds: 1}, statements: make(map[uintptr]*statement), inTransaction: true}
			defer c.Close()
			tx := &transaction{c: c, ctx: ctx}
			if mode == "precancel" {
				cancel()
			}
			err := tx.Commit()
			switch mode {
			case "success":
				if err != nil || c.h == 0 || c.inTransaction || executions != 1 || restorations != 1 {
					t.Fatal("confirmed commit", err)
				}
			case "precancel":
				if !errors.Is(err, context.Canceled) || errors.Is(err, ErrOutcomeUnknown) || c.h == 0 || !c.inTransaction || executions != 0 || restorations != 0 {
					t.Fatal("unsubmitted commit lost rollback ownership", err)
				}
			default:
				if !errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, driver.ErrBadConn) || c.h != 0 || executions != 1 || restorations != 1 {
					t.Fatal("post-commit failure became retryable", err)
				}
			}
		})
	}
}

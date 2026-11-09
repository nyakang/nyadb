package snapshotfiles

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInputRechecksOriginalBytesAndBounds(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "original.snapshot")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	ops := Operations()
	source, err := ops.InspectInput(ctx, path, 8)
	if err != nil || source.Size != 8 || len(source.SHA256) != 64 {
		t.Fatal(source, err)
	}
	r, err := ops.OpenInput(ctx, path, source, 8)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(r)
	if err != nil || string(raw) != "original" {
		t.Fatal(string(raw), err)
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("modified"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = ops.OpenInput(ctx, path, source, 8); !errors.Is(err, ErrChanged) {
		t.Fatal("changed input accepted", err)
	}
	if _, err = ops.InspectInput(ctx, path, 7); !errors.Is(err, ErrBounds) {
		t.Fatal("oversized source accepted", err)
	}
	if _, err = ops.InspectInput(ctx, filepath.Dir(path), 1024); !errors.Is(err, ErrBounds) {
		t.Fatal("directory accepted", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = ops.InspectInput(canceled, path, 8); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled inspection accepted", err)
	}
}

func TestOutputPublishesOnlyCompleteUnchangedDestinationAndRetainsPartial(t *testing.T) {
	for _, scenario := range []string{"new", "replace", "changed_before_open", "changed_before_finish", "appeared", "incomplete", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "download.snapshot")
			ops := Operations()
			if strings.HasPrefix(scenario, "changed") || scenario == "replace" {
				if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			plan, err := ops.PrepareOutput(ctx, path, 1024)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "changed_before_open" {
				if err = os.WriteFile(path, []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
				if _, err = ops.OpenOutput(ctx, plan); !errors.Is(err, ErrChanged) {
					t.Fatal("changed target accepted", err)
				}
				return
			}
			sink, err := ops.OpenOutput(ctx, plan)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = sink.Write([]byte("archive")); err != nil {
				t.Fatal(err)
			}
			if err = sink.Close(); err != nil {
				t.Fatal(err)
			}
			if scenario == "changed_before_finish" || scenario == "appeared" {
				if err = os.WriteFile(path, []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			receipt, err := sink.Finish(ctx, scenario != "incomplete")
			if scenario == "new" || scenario == "replace" {
				if err != nil || !receipt.Published || receipt.Partial != "" || receipt.Bytes != 7 {
					t.Fatal(receipt, err)
				}
				raw, err := os.ReadFile(path)
				if err != nil || string(raw) != "archive" {
					t.Fatal(string(raw), err)
				}
			} else {
				if receipt.Published || receipt.Partial == "" || receipt.Bytes != 7 {
					t.Fatal("partial lost", receipt, err)
				}
				raw, readErr := os.ReadFile(receipt.Partial)
				if readErr != nil || string(raw) != "archive" {
					t.Fatal(string(raw), readErr)
				}
				if scenario == "changed_before_finish" || scenario == "appeared" {
					if !errors.Is(err, ErrChanged) {
						t.Fatal(err)
					}
					raw, _ = os.ReadFile(path)
					if string(raw) != "changed" {
						t.Fatal("concurrent destination overwritten")
					}
				}
			}
		})
	}
}

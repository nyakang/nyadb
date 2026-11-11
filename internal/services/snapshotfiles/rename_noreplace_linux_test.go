//go:build linux

package snapshotfiles

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSnapshotNoReplaceMovePreservesCollisionAndCapturedDirectories(t *testing.T) {
	base := t.TempDir()
	sourcePath := filepath.Join(base, "source")
	targetPath := filepath.Join(base, "target")
	for _, path := range []string{sourcePath, targetPath} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sourcePath, "original"), []byte("original full bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetPath, "chosen"), []byte("foreign existing bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	target, err := os.Open(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	if err := snapshotMoveNoReplace(source, "original", target, "chosen"); !errors.Is(err, unix.EEXIST) {
		t.Fatal("collision replaced foreign file", err)
	}
	b, err := os.ReadFile(filepath.Join(sourcePath, "original"))
	if err != nil || string(b) != "original full bytes" {
		t.Fatal("collision consumed original source", string(b), err)
	}
	b, err = os.ReadFile(filepath.Join(targetPath, "chosen"))
	if err != nil || string(b) != "foreign existing bytes" {
		t.Fatal("foreign target changed", string(b), err)
	}
	movedSource, movedTarget := sourcePath+"-moved", targetPath+"-moved"
	if err := os.Rename(sourcePath, movedSource); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(targetPath, movedTarget); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{sourcePath, targetPath} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sourcePath, "original"), []byte("foreign replaced parent bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := snapshotMoveNoReplace(source, "original", target, "new-name"); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(filepath.Join(movedTarget, "new-name"))
	if err != nil || string(b) != "original full bytes" {
		t.Fatal("move used a replacement directory", string(b), err)
	}
	b, err = os.ReadFile(filepath.Join(sourcePath, "original"))
	if err != nil || string(b) != "foreign replaced parent bytes" {
		t.Fatal("move touched a foreign source", string(b), err)
	}
	if _, err := os.Lstat(filepath.Join(targetPath, "new-name")); !os.IsNotExist(err) {
		t.Fatal("move published into the replacement directory", err)
	}
}

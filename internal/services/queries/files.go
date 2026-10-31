package queries

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"
)

func ReadFile(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 8<<20+1))
	if e != nil {
		return "", e
	}
	if len(b) > 8<<20 {
		return "", errors.New("SQL editor file exceeds 8 MiB; use script execution")
	}
	if !utf8.Valid(b) {
		return "", errors.New("SQL file is not UTF-8")
	}
	return string(b), nil
}
func WriteFile(path, text string) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".nyadb-sql-*")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if _, e = f.WriteString(text); e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, path)
}

func WriteStream(path string, work func(io.Writer) error) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".nyadb-export-*")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if e = work(f); e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, path)
}

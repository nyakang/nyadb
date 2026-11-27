// Package databendfixture records owned public-product acceptance evidence.
// Production packages do not import it.
package databendfixture

import (
	"encoding/json"
	"fmt"
	"image/png"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"
)

type Fixture struct {
	Endpoint, Username, PasswordFile, Archive, SHA256, Version, Directory, Database string
	Starts                                                                          atomic.Int32
}

func Read(t *testing.T) *Fixture {
	t.Helper()
	if os.Getenv("NYADB_DATABEND_PUBLIC_IT") != "1" {
		t.Skip("requires owned Databend and pinned original managed package")
	}
	f := &Fixture{}
	raw, err := os.ReadFile(os.Getenv("NYADB_DATABEND_MANAGED_FIXTURE"))
	if err != nil || json.Unmarshal(raw, f) != nil || !filepath.IsAbs(f.Directory) || !filepath.IsAbs(f.Archive) || f.Version != "jdbc-0.4.8-agent-0.1.82" || f.SHA256 != "7069b794a21d8995b67d6918c6c106967ce94a18ae9a470159401bce18032bfd" {
		t.Fatal("original public fixture unavailable", err)
	}
	u, err := url.Parse(f.Endpoint)
	if err != nil || u.Scheme != "http" || !net.ParseIP(u.Hostname()).IsLoopback() || u.Port() == "" {
		t.Fatal("public fixture must use owned loopback endpoint")
	}
	return f
}

func (f *Fixture) Secret(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(f.PasswordFile)
	if err != nil || len(raw) == 0 {
		t.Fatal("original fixture credential unavailable", err)
	}
	return string(raw)
}

func (f *Fixture) Observe(directory string) func(int) error {
	expected := filepath.Join(directory, "databend@"+f.Version, "jre/bin/java")
	return func(pid int) error {
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return err
		}
		end := strings.LastIndexByte(string(stat), ')')
		if end < 0 {
			return fmt.Errorf("original process identity unavailable")
		}
		fields := strings.Fields(string(stat[end+1:]))
		actual, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
		if err != nil || len(fields) < 20 || actual != expected {
			return fmt.Errorf("original installed Java executable differs")
		}
		journal, err := os.OpenFile(filepath.Join(f.Directory, "processes.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer journal.Close()
		if err := json.NewEncoder(journal).Encode(map[string]any{"pid": pid, "start_ticks": fields[19], "executable": actual}); err != nil {
			return err
		}
		f.Starts.Add(1)
		return nil
	}
}

func (f *Fixture) Journal(t *testing.T, name string, value any) {
	t.Helper()
	file, err := os.OpenFile(filepath.Join(f.Directory, name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := json.NewEncoder(file).Encode(value); err != nil {
		t.Fatal(err)
	}
}

func (f *Fixture) Screenshot(t *testing.T, tt *ui.Tester, name string) {
	t.Helper()
	file, err := os.Create(filepath.Join(f.Directory, name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	err = png.Encode(file, tt.Image())
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		t.Fatal("public screenshot unavailable", err, closeErr)
	}
}

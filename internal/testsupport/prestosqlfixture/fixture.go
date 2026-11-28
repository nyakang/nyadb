// Package prestosqlfixture records owned public-product acceptance evidence.
// Production packages do not import it.
package prestosqlfixture

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
	"github.com/nyakang/nyadb/internal/services/connections"
)

type Fixture struct {
	Endpoint, Username, PasswordFile, Archive, SHA256, Version, Directory, Database string
	TrustStore, StorePasswordFile                                                   string
	Starts                                                                          atomic.Int32
}

func Read(t *testing.T) *Fixture {
	t.Helper()
	if os.Getenv("NYADB_PRESTOSQL_PUBLIC_IT") != "1" {
		t.Skip("requires owned PrestoSQL and pinned original managed package")
	}
	f := &Fixture{}
	raw, err := os.ReadFile(os.Getenv("NYADB_PRESTOSQL_PUBLIC_FIXTURE"))
	if err != nil || json.Unmarshal(raw, f) != nil || !filepath.IsAbs(f.Directory) || !filepath.IsAbs(f.Archive) || f.Version != "jdbc-350-plugin-0.1.57" || f.SHA256 != "1b08734ceba673f1d504bf4cf1080f8dc710d4e736811cfea145ff1307d4fa89" {
		t.Fatal("original public fixture unavailable", err)
	}
	u, err := url.Parse(f.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || (u.Hostname() != "localhost" && !net.ParseIP(u.Hostname()).IsLoopback()) || u.Port() == "" {
		t.Fatal("public fixture must use owned loopback endpoint")
	}
	return f
}

func (f *Fixture) Credentials(t *testing.T) connections.PrestoSQLCredentials {
	t.Helper()
	read := func(path string) string {
		if path == "" {
			return ""
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal("fixture secret unavailable", err)
		}
		return string(raw)
	}
	return connections.PrestoSQLCredentials{Password: read(f.PasswordFile), TrustStorePassword: read(f.StorePasswordFile)}
}
func (f *Fixture) Secret(t *testing.T) string {
	t.Helper()
	encoded, err := connections.EncodePrestoSQLCredentials(f.Credentials(t))
	if err != nil {
		t.Fatal("fixture credential encoding", err)
	}
	return encoded
}
func (f *Fixture) URI(t *testing.T) string {
	t.Helper()
	endpoint, err := url.Parse(f.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	credentials := f.Credentials(t)
	params := url.Values{}
	if endpoint.Scheme == "https" {
		params.Set("sslmode", "verify-full")
		params.Set("SSLTrustStorePath", f.TrustStore)
		params.Set("SSLTrustStoreType", "PKCS12")
		params.Set("SSLTrustStorePassword", credentials.TrustStorePassword)
	}
	u := url.URL{Scheme: "prestosql", Host: endpoint.Host, Path: "/memory/default", User: url.UserPassword(f.Username, credentials.Password), RawQuery: params.Encode()}
	return u.String()
}

func (f *Fixture) Observe(directory string) func(int) error {
	expected := filepath.Join(directory, "prestosql@"+f.Version, "jre/bin/java")
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

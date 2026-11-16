// Package zookeeperfixture shares owned-product evidence across integration
// tests. Production packages do not import it.
package zookeeperfixture

import (
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/driverstore"
)

type Fixture struct {
	Archive, SHA256, Version, Directory, Endpoint string
	Starts                                        atomic.Int32
}

func Read(t *testing.T) *Fixture {
	t.Helper()
	if os.Getenv("NYADB_ZOOKEEPER_PUBLIC_IT") != "1" {
		t.Skip("requires public acceptance against an owned official ZooKeeper")
	}
	f := &Fixture{}
	raw, err := os.ReadFile(os.Getenv("NYADB_AGENT_ZOOKEEPER_FIXTURE_FILE"))
	if err != nil || json.Unmarshal(raw, f) != nil || f.Archive == "" || f.Directory == "" || f.Version == "" || len(f.SHA256) != 64 {
		t.Fatal("owned native fixture unavailable")
	}
	host, port, err := net.SplitHostPort(f.Endpoint)
	if err != nil || !net.ParseIP(host).IsLoopback() || port == "" {
		t.Fatal("requires original owned loopback endpoint")
	}
	return f
}

func (f *Fixture) Observe(directory, version string) func(int) error {
	executable := filepath.Join(directory, "zookeeper@"+version, "bin", "agent")
	return func(pid int) error {
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return err
		}
		end := strings.LastIndexByte(string(stat), ')')
		if end < 0 {
			return fmt.Errorf("original process stat unavailable")
		}
		fields := strings.Fields(string(stat[end+1:]))
		actual, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
		if err != nil || len(fields) < 20 || actual != executable {
			return fmt.Errorf("original executable identity changed")
		}
		record, err := json.Marshal(map[string]any{"pid": pid, "start_ticks": fields[19], "executable": actual, "version": version})
		if err != nil {
			return err
		}
		journal, err := os.OpenFile(filepath.Join(f.Directory, "processes.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = journal.Write(append(record, '\n'))
		closeErr := journal.Close()
		if err != nil {
			return err
		}
		if closeErr == nil {
			f.Starts.Add(1)
		}
		return closeErr
	}
}

func (f *Fixture) Install(t *testing.T) (*driverstore.Store, connections.ManagedDriver) {
	t.Helper()
	store, err := driverstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d, err := store.Import(context.Background(), driverstore.Product{Key: "zookeeper", Runtime: "native"}, f.Archive)
	if err != nil || d.SHA256 != f.SHA256 || d.Version != f.Version || !d.Supports("zookeeper-v1") {
		t.Fatal("native driver import changed original pin", err)
	}
	t.Cleanup(func() {
		if err := store.Remove(context.Background(), d.Key, d.Version); err != nil {
			t.Error("original package remained leased", err)
		}
	})
	return store, d
}

func (f *Fixture) Profile(t *testing.T, d connections.ManagedDriver, id int64) connections.Profile {
	t.Helper()
	p, err := connections.Defaults(connections.ZooKeeper)
	if err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(f.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	p.ID, p.Name, p.Host, p.Port = id, "original public ZooKeeper", host, uint16(n)
	p.ZooKeeperOptions.DriverVersion, p.ZooKeeperOptions.DriverSHA256, p.ZooKeeperOptions.DriverDirectory = d.Version, d.SHA256, d.Directory
	p.ZooKeeperOptions.SessionTimeoutMS, p.ZooKeeperOptions.ConnectionTimeoutMS = 2000, 5000
	return p
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
		t.Fatal("public image could not be recorded", err, closeErr)
	}
}

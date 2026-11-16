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
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/driverstore"
)

type Fixture struct {
	Archive, SHA256, Version, Directory, Endpoint string
	Starts                                        atomic.Int32
	Security                                      *Security
	Topology                                      *Topology
}

type Security struct {
	TLSEndpoint         string `json:"tls_endpoint"`
	MTLSEndpoint        string `json:"mtls_endpoint"`
	SASLEndpoint        string `json:"sasl_endpoint"`
	SASLTLSEndpoint     string `json:"sasl_tls_endpoint"`
	SASLMTLSEndpoint    string `json:"sasl_mtls_endpoint"`
	CAFile              string `json:"ca_file"`
	WrongCAFile         string `json:"wrong_ca_file"`
	ClientCertFile      string `json:"client_cert_file"`
	ClientKeyFile       string `json:"client_key_file"`
	WrongClientCertFile string `json:"wrong_client_cert_file"`
	WrongClientKeyFile  string `json:"wrong_client_key_file"`
	Username            string `json:"username"`
	DigestSecret        string `json:"digest_secret"`
	SASLSecret          string `json:"sasl_secret"`
}

type Topology struct {
	Endpoints  []string `json:"endpoints"`
	Containers []string `json:"containers"`
	Token      string   `json:"token"`
}

func ReadTopology(t *testing.T) *Fixture {
	t.Helper()
	if os.Getenv("NYADB_ZOOKEEPER_TOPOLOGY_IT") != "1" {
		t.Skip("requires owned three-member ensemble and authenticated secure servers")
	}
	f := ReadSecurity(t)
	if f.Topology == nil || len(f.Topology.Endpoints) != 3 || len(f.Topology.Containers) != 3 || filepath.Base(f.Directory) != "agent-"+f.Topology.Token || len(f.Topology.Token) != 16 {
		t.Fatal("owned ensemble fixture unavailable")
	}
	seen := map[string]bool{}
	for _, endpoint := range append(append([]string(nil), f.Topology.Endpoints...), f.Security.SASLTLSEndpoint, f.Security.SASLMTLSEndpoint) {
		host, port, err := net.SplitHostPort(endpoint)
		if err != nil || !net.ParseIP(host).IsLoopback() || port == "" || seen[endpoint] {
			t.Fatal("owned topology endpoint unavailable")
		}
		seen[endpoint] = true
	}
	return f
}

// MemberAction affects only a recorded, still-owned original fixture container.
// It never chooses arbitrary Docker resources or substitutes protocol replies.
func (f *Fixture) MemberAction(t *testing.T, index int, start bool) {
	t.Helper()
	if f.Topology == nil || index < 0 || index >= len(f.Topology.Containers) {
		t.Fatal("original ensemble member identity unavailable")
	}
	id := f.Topology.Containers[index]
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(ctx, "docker", "inspect", id).CombinedOutput()
	var inspected []struct {
		Config struct{ Labels map[string]string }
	}
	if err != nil || json.Unmarshal(raw, &inspected) != nil || len(inspected) != 1 || inspected[0].Config.Labels["nyadb.zookeeper.fixture"] != f.Topology.Token {
		t.Fatal("original ensemble container ownership changed", err)
	}
	args := []string{"stop", "--time", "2", id}
	if start {
		args = []string{"start", id}
	}
	if raw, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		t.Fatal("owned ensemble member action failed", err, string(raw))
	}
}

// ConnectedMember joins the captured original process's actual socket inodes
// to Linux TCP observations. Endpoint order or library shuffling is not proof.
func (f *Fixture) ConnectedMember(t *testing.T, pid int) int {
	t.Helper()
	fd := filepath.Join("/proc", strconv.Itoa(pid), "fd")
	entries, err := os.ReadDir(fd)
	if err != nil {
		t.Fatal("original process file descriptors unavailable", err)
	}
	inodes := map[string]bool{}
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join(fd, entry.Name()))
		if err == nil && strings.HasPrefix(target, "socket:[") {
			inodes[strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")] = true
		}
	}
	ports := map[uint64]int{}
	for i, endpoint := range f.Topology.Endpoints {
		_, port, _ := net.SplitHostPort(endpoint)
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil {
			t.Fatal(err)
		}
		ports[n] = i
	}
	found := -1
	for _, family := range []string{"tcp", "tcp6"} {
		raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "net", family))
		if err != nil {
			t.Fatal("original TCP observation unavailable", err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 10 || fields[3] != "01" || !inodes[fields[9]] {
				continue
			}
			_, hexPort, _ := strings.Cut(fields[2], ":")
			port, err := strconv.ParseUint(hexPort, 16, 16)
			index, ok := ports[port]
			if err == nil && ok {
				if found >= 0 && found != index {
					t.Fatal("original document has ambiguous ensemble sockets")
				}
				found = index
			}
		}
	}
	if found < 0 {
		t.Fatal("captured original document has no established ensemble connection")
	}
	return found
}

func ReadSecurity(t *testing.T) *Fixture {
	t.Helper()
	if os.Getenv("NYADB_ZOOKEEPER_SECURITY_IT") != "1" {
		t.Skip("requires owned ZooKeeper security acceptance")
	}
	f := Read(t)
	if f.Security == nil || f.Security.CAFile == "" || f.Security.Username == "" || f.Security.DigestSecret == "" || f.Security.SASLSecret == "" {
		t.Fatal("owned security fixture unavailable")
	}
	for _, endpoint := range []string{f.Security.TLSEndpoint, f.Security.MTLSEndpoint, f.Security.SASLEndpoint} {
		host, _, err := net.SplitHostPort(endpoint)
		if err != nil || !net.ParseIP(host).IsLoopback() {
			t.Fatal("owned security endpoint unavailable")
		}
	}
	return f
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
	return f.EndpointProfile(t, d, id, f.Endpoint)
}

func (f *Fixture) EndpointProfile(t *testing.T, d connections.ManagedDriver, id int64, endpoint string) connections.Profile {
	t.Helper()
	p, err := connections.Defaults(connections.ZooKeeper)
	if err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(endpoint)
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

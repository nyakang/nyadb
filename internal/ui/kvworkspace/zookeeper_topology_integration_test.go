package kvworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/driverstore"
	"github.com/nyakang/nyadb/internal/testsupport/zookeeperfixture"
)

func zooTopologyExecute(t *testing.T, s *State, d *Document, method string, params any, write bool) contract.KVResult {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	s.SetMethod(d, method)
	d.Editor.Buffer.Set(string(raw))
	s.Run(d)
	if write {
		if d.confirmation == nil || d.Busy() {
			t.Fatal("original topology write bypassed public confirmation")
		}
		s.Confirm(d, true)
	}
	nativeZooConsume(t, s, d)
	r, ok := d.LastReceipt()
	if !ok || !r.Result.Completed || r.Result.ZNode == nil || r.Result.ZNode.Reply == nil || r.Result.ProjectionError != "" {
		t.Fatal("original topology request did not complete", d.Error())
	}
	return r.Result.Clone()
}

func TestZooKeeperNativePublicEnsembleFailoverKeepsOriginalDocumentSession(t *testing.T) {
	f := zookeeperfixture.ReadTopology(t)
	_, driver := f.Install(t)
	m, _ := zooSecurityRuntime(t, f, driver)
	var lastPID atomic.Int32
	observe := f.Observe(driver.Directory, driver.Version)
	ctx := driverstore.WithRuntimeObserver(context.Background(), func(pid int) error {
		if err := observe(pid); err != nil {
			return err
		}
		lastPID.Store(int32(pid))
		return nil
	})
	p := f.EndpointProfile(t, driver, 41, f.Topology.Endpoints[0])
	p.ZooKeeperOptions.AdditionalServers = strings.Join(f.Topology.Endpoints[1:], ",")
	p.ZooKeeperOptions.Chroot, p.ZooKeeperOptions.Namespace = "/nyadb_ensemble", "/ns"
	p.ZooKeeperOptions.SessionTimeoutMS, p.ZooKeeperOptions.ConnectionTimeoutMS = 20000, 10000
	if _, err := m.ConnectSession(ctx, p, ""); err != nil {
		t.Fatal("real three-member public publication", err)
	}
	generation := m.Generation(p.ID)
	s := New(Dependencies{ProviderFor: m.KVRequests})
	t.Cleanup(s.CloseAll)
	d := s.Open(41, p.ID, contract.KVRequest{})
	s.Preconnect(d)
	nativeZooConsume(t, s, d)
	pid := int(lastPID.Load())
	if pid <= 0 || d.lease == nil || d.Scope().ZNode.Root != "/nyadb_ensemble/ns" {
		t.Fatal("original ensemble document/root was not captured")
	}
	originalScope := d.Scope()
	root := zooTopologyExecute(t, s, d, "kv_get", map[string]string{"key": "/"}, false)
	if string(root.ZNode.Reply.Node.Value) != "namespace" {
		t.Fatal("Java-authored qualified ensemble root changed")
	}
	created := zooTopologyExecute(t, s, d, "kv_put", nativeZooPut("/original-owner", []byte{0, 255, 128}, "ephemeral"), true)
	before := zooTopologyExecute(t, s, d, "kv_get", map[string]string{"key": "/original-owner"}, false)
	owner := before.ZNode.Reply.Node.Stat.EphemeralOwner
	if owner == 0 {
		t.Fatal("original document did not own a real ephemeral session")
	}
	member := f.ConnectedMember(t, pid)
	starts := f.Starts.Load()
	draft := d.Request()
	f.MemberAction(t, member, false)
	// The real connected server is stopped while its two peers retain quorum.
	// The original library may reconnect; the application must not replace the
	// native process, session identity, draft or receipt to accomplish failover.
	after := zooTopologyExecute(t, s, d, "kv_get", map[string]string{"key": "/original-owner"}, false)
	failoverStarts := f.Starts.Load() - starts
	if failoverStarts != 0 || int(lastPID.Load()) != pid || m.Generation(p.ID) != generation || d.Scope().Generation != originalScope.Generation || d.Scope().ZNode != originalScope.ZNode || d.Request() != draft || after.ZNode.Reply.Node.Stat.EphemeralOwner != owner || !bytes.Equal(after.ZNode.Reply.Node.Value, before.ZNode.Reply.Node.Value) {
		t.Fatal("ensemble failover replaced the original physical document/session")
	}
	replacement := f.ConnectedMember(t, pid)
	if replacement == member {
		t.Fatal("original process did not move to a surviving ensemble member")
	}
	write := zooTopologyExecute(t, s, d, "kv_put", nativeZooPut("/after-failover", []byte{0, 255, 128}, "persistent"), true)
	observer := zooSecurityLease(t, m, p.ID, 42)
	verified := nativeZooCall(t, observer, "kv_get", map[string]string{"key": "/after-failover"})
	if !bytes.Equal(verified.ZNode.Reply.Node.Value, []byte{0, 255, 128}) || verified.ZNode.Reply.Node.Stat.Version != 0 {
		t.Fatal("independent original process did not observe exactly one committed write")
	}
	zooSecurityView(t, f, s, d, "ensemble-failover")
	records := map[string]any{
		"root": zooSecurityOriginal(t, f, root), "created": zooSecurityOriginal(t, f, created), "before": zooSecurityOriginal(t, f, before),
		"after": zooSecurityOriginal(t, f, after), "write": zooSecurityOriginal(t, f, write), "verified": zooSecurityOriginal(t, f, verified),
		"original_pid": pid, "failed_member": member, "surviving_member": replacement, "generation": generation, "ephemeral_owner": owner,
		"original_process_starts_during_failover": failoverStarts, "root_identity": originalScope.ZNode.Root,
	}
	s.Close(d)
	// Closing the original owning document must remove its ephemeral value from
	// the surviving real quorum, independently of the workspace's cached receipt.
	deadline := time.Now().Add(10 * time.Second)
	for {
		gone := nativeZooCall(t, observer, "kv_get", map[string]string{"key": "/original-owner"})
		if gone.ZNode.Reply.Found != nil && !*gone.ZNode.Reply.Found {
			records["closed"] = zooSecurityOriginal(t, f, gone)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("original document close left its ephemeral owner in the ensemble")
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.MemberAction(t, member, true)
	zooSecurityRecord(t, f, "topology-ensemble-receipts", records)
	_, port, _ := net.SplitHostPort(f.Topology.Endpoints[member])
	t.Log("original document moved from stopped member port", port, "to surviving member", replacement)
}

func TestZooKeeperNativePublicAuthenticatedTLSAndMutualTLSOriginalReceipts(t *testing.T) {
	f := zookeeperfixture.ReadTopology(t)
	_, driver := f.Install(t)
	m, ctx := zooSecurityRuntime(t, f, driver)
	for index, mode := range []string{"digest-tls", "digest-mtls", "sasl-tls", "sasl-mtls"} {
		t.Run(mode, func(t *testing.T) {
			endpoint := []string{f.Security.TLSEndpoint, f.Security.MTLSEndpoint, f.Security.SASLTLSEndpoint, f.Security.SASLMTLSEndpoint}[index]
			p := f.EndpointProfile(t, driver, int64(51+index), endpoint)
			p.TLSMode, p.Username, p.ZooKeeperOptions.CAFile = "verify-full", f.Security.Username, f.Security.CAFile
			secret, key := f.Security.DigestSecret, "/nyadb_digest"
			p.ZooKeeperOptions.Auth = "digest"
			if index >= 2 {
				p.ZooKeeperOptions.Auth, secret, key = "sasl_digest", f.Security.SASLSecret, "/nyadb_secure"
			}
			if index%2 == 1 {
				p.ZooKeeperOptions.ClientCertFile, p.ZooKeeperOptions.ClientKeyFile = f.Security.ClientCertFile, f.Security.ClientKeyFile
			}
			if _, err := m.ConnectSession(ctx, p, secret); err != nil {
				t.Fatal("original combined authentication/secure publication", mode, err)
			}
			generation := m.Generation(p.ID)
			l := zooSecurityLease(t, m, p.ID, uint64(index+1))
			read := nativeZooCall(t, l, "kv_get", map[string]string{"key": key})
			writeKey := key + "/" + mode
			write := nativeZooCall(t, l, "kv_put", nativeZooPut(writeKey, []byte{0, 255, 128}, "persistent"))
			verified := nativeZooCall(t, l, "kv_get", map[string]string{"key": writeKey})
			if !bytes.Equal(verified.ZNode.Reply.Node.Value, []byte{0, 255, 128}) {
				t.Fatal("combined secure transport changed binary value")
			}
			records := map[string]any{"read": zooSecurityOriginal(t, f, read), "write": zooSecurityOriginal(t, f, write), "verified": zooSecurityOriginal(t, f, verified), "generation": generation}
			if index < 2 {
				unauthorized := p
				unauthorized.ID += 100
				if _, err := m.ConnectSession(ctx, unauthorized, "deliberately-wrong-secure-auth"); err != nil {
					t.Fatal("secure digest root should remain public", err)
				}
				deniedLease := zooSecurityLease(t, m, unauthorized.ID, 9)
				plan := nativeZooPlan(t, deniedLease, "kv_get", map[string]string{"key": key})
				denied, err := deniedLease.Execute(ctx, plan)
				if err == nil || denied.Completed || denied.ZNode == nil || !bytes.Contains(denied.ZNode.Native, []byte("not authenticated")) {
					t.Fatal("secure transport bypassed original digest ACL", err)
				}
				records["denied"] = zooSecurityOriginal(t, f, denied)
			} else {
				deadline, cancel := context.WithTimeout(ctx, 8*time.Second)
				before := f.Starts.Load()
				err := m.Test(deadline, p, "deliberately-wrong-secure-auth")
				expired := deadline.Err() != nil
				cancel()
				if err == nil || expired || f.Starts.Load() != before+1 {
					t.Fatal("secure SASL rejection must come from one original process before the test deadline", err)
				}
				records["rejected"] = map[string]any{"original_process_starts": 1, "test_deadline_expired": false, "error": err.Error()}
			}
			if index%2 == 1 {
				missing := p
				missing.ZooKeeperOptions.ClientCertFile, missing.ZooKeeperOptions.ClientKeyFile = "", ""
				if err := m.Test(ctx, missing, secret); err == nil {
					t.Fatal("combined authentication bypassed mandatory client identity")
				}
				records["missing_client_rejected"] = true
			}
			wrong := p
			wrong.ZooKeeperOptions.CAFile = f.Security.WrongCAFile
			if err := m.Test(ctx, wrong, secret); err == nil {
				t.Fatal("combined secure authentication bypassed server trust")
			}
			records["wrong_ca_rejected"] = true
			nativeZooCall(t, l, "kv_get", map[string]string{"key": writeKey})
			if m.Generation(p.ID) != generation {
				t.Fatal("isolated combined-security rejection replaced original publication")
			}
			s := New(Dependencies{ProviderFor: m.KVRequests})
			t.Cleanup(s.CloseAll)
			d := s.Open(uint64(51+index), p.ID, contract.KVRequest{Method: "kv_get", JSON: `{"key":"` + writeKey + `"}`})
			s.Run(d)
			nativeZooConsume(t, s, d)
			zooSecurityView(t, f, s, d, mode)
			zooSecurityRecord(t, f, "topology-"+mode+"-receipts", records)
		})
	}
}

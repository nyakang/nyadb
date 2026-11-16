package kvworkspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/services/driverstore"
	"github.com/nyakang/nyadb/internal/testsupport/zookeeperfixture"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// Generic result JSON deliberately omits native error bytes. Acceptance exports
// the exact original envelope separately, after checking fixture credentials.
type zooSecurityReceipt struct {
	Result       contract.KVResult
	NativeBytes  []byte
	NativeSHA256 string
}

func zooSecurityOriginal(t *testing.T, f *zookeeperfixture.Fixture, result contract.KVResult) zooSecurityReceipt {
	t.Helper()
	result = result.Clone()
	if result.ZNode == nil || !result.ZNode.Captured || len(result.ZNode.Native) == 0 {
		t.Fatal("security evidence requires the complete original native envelope")
	}
	raw := result.ZNode.Native
	if bytes.Contains(raw, []byte(f.Security.DigestSecret)) || bytes.Contains(raw, []byte(f.Security.SASLSecret)) {
		t.Fatal("original security receipt exposed a fixture credential")
	}
	hash := sha256.Sum256(raw)
	return zooSecurityReceipt{Result: result, NativeBytes: raw, NativeSHA256: hex.EncodeToString(hash[:])}
}

func zooSecurityRuntime(t *testing.T, f *zookeeperfixture.Fixture, d connections.ManagedDriver) (*dbruntime.Manager, context.Context) {
	t.Helper()
	m := dbruntime.New()
	dbruntime.RegisterBuiltins(m, nil)
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Error(err)
		}
	})
	return m, driverstore.WithRuntimeObserver(context.Background(), f.Observe(d.Directory, d.Version))
}

func zooSecurityLease(t *testing.T, m *dbruntime.Manager, id int64, document uint64) contract.KVRequestLease {
	t.Helper()
	p, err := m.KVRequests(id)
	if err != nil {
		t.Fatal(err)
	}
	l, err := p.ReserveKV(document)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := l.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := l.Preconnect(context.Background()); err != nil {
		t.Fatal("original secure document preconnect", err)
	}
	return l
}

func zooSecurityRecord(t *testing.T, f *zookeeperfixture.Fixture, name string, records any) {
	t.Helper()
	raw, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(f.Security.DigestSecret)) || bytes.Contains(raw, []byte(f.Security.SASLSecret)) {
		t.Fatal("security evidence exposed a credential")
	}
	if err := os.WriteFile(filepath.Join(f.Directory, name+".json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func zooSecurityView(t *testing.T, f *zookeeperfixture.Fixture, s *State, d *Document, name string) {
	t.Helper()
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		if unknown, ok := d.UnknownReceipt(); ok {
			// Trigger the same blocked public action in each locale. This must
			// retain the original receipt and never start another executor.
			before := f.Starts.Load()
			s.Run(d)
			got, present := d.UnknownReceipt()
			if !present || d.Busy() || d.confirmation != nil || f.Starts.Load() != before || !bytes.Equal(got.Result.ZNode.Native, unknown.Result.ZNode.Native) || d.Error() != locales.Text("原写入未确认；恢复后可读取，关闭页签可放弃本地草稿与回执") {
				t.Fatal("localized denied action changed original receipt or enabled replay")
			}
		}
		for _, size := range [][2]int{{720, 520}, {1280, 800}} {
			tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d); s.Modals(c) }, size[0], size[1])
			tt.Frame()
			f.Screenshot(t, tt, language+"-"+strconv.Itoa(size[0])+"x"+strconv.Itoa(size[1])+"-security-"+name)
		}
	}
}

func TestZooKeeperNativePublicDigestACLIsolationAndDeniedWriteReceipt(t *testing.T) {
	f := zookeeperfixture.ReadSecurity(t)
	_, driver := f.Install(t)
	m, ctx := zooSecurityRuntime(t, f, driver)
	p := f.Profile(t, driver, 11)
	p.Username, p.ZooKeeperOptions.Auth = f.Security.Username, "digest"
	if _, err := m.ConnectSession(ctx, p, f.Security.DigestSecret); err != nil {
		t.Fatal(err)
	}
	l := zooSecurityLease(t, m, p.ID, 1)
	read := nativeZooCall(t, l, "kv_get", map[string]string{"key": "/nyadb_digest"})
	if string(read.ZNode.Reply.Node.Value) != "fixture" {
		t.Fatal("protected fixture was not authored by the real Java client")
	}
	write := nativeZooCall(t, l, "kv_put", map[string]any{"key": "/nyadb_digest", "value": map[string]string{"encoding": "utf8", "data": "original-authenticated-value"}, "writeMode": "update"})
	if write.ZNode.Reply.Version == nil || *write.ZNode.Reply.Version != 1 {
		t.Fatal("protected native write version changed")
	}
	var denied []zooSecurityReceipt
	for index, secret := range []string{"", "deliberately-wrong-digest"} {
		other := p
		other.ID = int64(12 + index)
		if index == 0 {
			other.Username = ""
			other.ZooKeeperOptions.Auth = "none"
		}
		if _, err := m.ConnectSession(ctx, other, secret); err != nil {
			t.Fatal("root should remain public while the fixture node is ACL-protected", err)
		}
		provider, _ := m.KVRequests(other.ID)
		s := New(Dependencies{ProviderFor: func(int64) (contract.KVRequestsProvider, error) { return provider, nil }})
		t.Cleanup(s.CloseAll)
		doc := s.Open(uint64(10+index), other.ID, contract.KVRequest{})
		s.Preconnect(doc)
		nativeZooConsume(t, s, doc)
		s.SetMethod(doc, "kv_get")
		doc.Editor.Buffer.Set(`{"key":"/nyadb_digest"}`)
		s.Run(doc)
		nativeZooConsume(t, s, doc)
		r, ok := doc.LastReceipt()
		if !ok || r.Result.Completed || r.Result.Code != "NativeError" || r.Result.ZNode == nil || !r.Result.ZNode.Acknowledged || !bytes.Contains(r.Result.ZNode.Native, []byte("not authenticated")) {
			t.Fatal("ACL denial became a successful or missing native read")
		}
		denied = append(denied, zooSecurityOriginal(t, f, r.Result))
		s.SetMethod(doc, "kv_put")
		doc.Editor.Buffer.Set(`{"key":"/nyadb_digest","value":{"encoding":"utf8","data":"denied-write"},"writeMode":"update"}`)
		s.Run(doc)
		s.Confirm(doc, true)
		nativeZooConsume(t, s, doc)
		unknown, ok := doc.UnknownReceipt()
		if !ok || !unknown.Result.WriteOutcomeUnknown || unknown.Result.ZNode == nil || !unknown.Result.ZNode.Acknowledged || unknown.Result.ZNode.ErrorCode == nil || !bytes.Contains(unknown.Result.ZNode.Native, []byte("not authenticated")) {
			t.Fatal("original remote write error lost its independent complete receipt")
		}
		denied = append(denied, zooSecurityOriginal(t, f, unknown.Result))
		s.Run(doc)
		if doc.Busy() || doc.confirmation != nil {
			t.Fatal("ACL error enabled an automatic write replay")
		}
		if index == 1 {
			zooSecurityView(t, f, s, doc, "digest-denied-write")
		}
	}
	verified := nativeZooCall(t, l, "kv_get", map[string]string{"key": "/nyadb_digest"})
	if string(verified.ZNode.Reply.Node.Value) != "original-authenticated-value" || verified.ZNode.Reply.Node.Stat.Version != 1 {
		t.Fatal("denied write changed protected data")
	}
	zooSecurityRecord(t, f, "security-digest-receipts", map[string]any{"read": zooSecurityOriginal(t, f, read), "write": zooSecurityOriginal(t, f, write), "denied": denied, "verified": zooSecurityOriginal(t, f, verified)})
}

func TestZooKeeperNativePublicTLSMutualTLSCapturedTrustAndRecovery(t *testing.T) {
	f := zookeeperfixture.ReadSecurity(t)
	_, driver := f.Install(t)
	m, ctx := zooSecurityRuntime(t, f, driver)
	for index, endpoint := range []string{f.Security.TLSEndpoint, f.Security.MTLSEndpoint} {
		name := []string{"tls", "mtls"}[index]
		t.Run(name, func(t *testing.T) {
			p := f.EndpointProfile(t, driver, int64(20+index), endpoint)
			p.TLSMode = "verify-full"
			dir := t.TempDir()
			copyFile := func(source, name string) string {
				t.Helper()
				raw, err := os.ReadFile(source)
				if err != nil {
					t.Fatal(err)
				}
				dest := filepath.Join(dir, name)
				if err := os.WriteFile(dest, raw, 0600); err != nil {
					t.Fatal(err)
				}
				return dest
			}
			p.ZooKeeperOptions.CAFile = copyFile(f.Security.CAFile, "ca.pem")
			if index == 1 {
				p.ZooKeeperOptions.ClientCertFile = copyFile(f.Security.ClientCertFile, "client.pem")
				p.ZooKeeperOptions.ClientKeyFile = copyFile(f.Security.ClientKeyFile, "client.key")
			}
			if _, err := m.ConnectSession(ctx, p, ""); err != nil {
				t.Fatal("real secure native publication", err)
			}
			generation := m.Generation(p.ID)
			// An original session captures trust and identity bytes, not later path contents.
			wrongCA, _ := os.ReadFile(f.Security.WrongCAFile)
			if err := os.WriteFile(p.ZooKeeperOptions.CAFile, wrongCA, 0600); err != nil {
				t.Fatal(err)
			}
			if err := m.Test(ctx, p, ""); err == nil {
				t.Fatal("foreign server CA was accepted")
			}
			l := zooSecurityLease(t, m, p.ID, 1)
			read := nativeZooCall(t, l, "kv_get", map[string]string{"key": "/nyadb_secure"})
			if string(read.ZNode.Reply.Node.Value) != "fixture" || m.Generation(p.ID) != generation {
				t.Fatal("isolated wrong trust test invalidated original session")
			}
			copyFile(f.Security.CAFile, "ca.pem")
			wrongHost := p
			wrongHost.Host = "localhost"
			if err := m.Test(ctx, wrongHost, ""); err == nil {
				t.Fatal("certificate with IP-only SAN accepted a foreign hostname")
			}
			if index == 1 {
				missing := p
				missing.ZooKeeperOptions.ClientCertFile = ""
				missing.ZooKeeperOptions.ClientKeyFile = ""
				if err := m.Test(ctx, missing, ""); err == nil {
					t.Fatal("server did not enforce mandatory client certificates")
				}
				wrong := p
				wrong.ZooKeeperOptions.ClientCertFile = f.Security.WrongClientCertFile
				wrong.ZooKeeperOptions.ClientKeyFile = f.Security.WrongClientKeyFile
				if err := m.Test(ctx, wrong, ""); err == nil {
					t.Fatal("server accepted a client signed by a foreign CA")
				}
				copyFile(f.Security.WrongClientCertFile, "client.pem")
				copyFile(f.Security.WrongClientKeyFile, "client.key")
				second := zooSecurityLease(t, m, p.ID, 2)
				nativeZooCall(t, second, "kv_get", map[string]string{"key": "/nyadb_secure"})
				copyFile(f.Security.ClientCertFile, "client.pem")
				copyFile(f.Security.ClientKeyFile, "client.key")
			}
			key := "/nyadb_secure/" + name
			write := nativeZooCall(t, l, "kv_put", nativeZooPut(key, []byte{0, 255, 128}, "persistent"))
			verified := nativeZooCall(t, l, "kv_get", map[string]string{"key": key})
			if !bytes.Equal(verified.ZNode.Reply.Node.Value, []byte{0, 255, 128}) {
				t.Fatal("secure native binary value changed")
			}
			provider, _ := m.KVRequests(p.ID)
			s := New(Dependencies{ProviderFor: m.KVRequests})
			t.Cleanup(s.CloseAll)
			doc := s.Open(9, p.ID, contract.KVRequest{Method: "kv_get", JSON: `{"key":"/nyadb_secure"}`})
			s.Run(doc)
			nativeZooConsume(t, s, doc)
			last, _ := doc.LastReceipt()
			draft := doc.Request()
			scope := provider.KVScope()
			if err := m.Disconnect(p.ID); err != nil {
				t.Fatal(err)
			}
			s.DisconnectProfile(p.ID)
			if _, err := m.ConnectSession(ctx, p, ""); err != nil {
				t.Fatal(err)
			}
			before := f.Starts.Load()
			s.Recover(doc)
			nativeZooConsume(t, s, doc)
			got, ok := doc.LastReceipt()
			if !ok || doc.NeedsRecovery() || doc.Request() != draft || f.Starts.Load() != before || !bytes.Equal(got.Result.ZNode.Native, last.Result.ZNode.Native) || doc.Scope().Generation <= scope.Generation {
				t.Fatal("secure recovery replayed or lost original request/receipt")
			}
			zooSecurityView(t, f, s, doc, name+"-recovery")
			zooSecurityRecord(t, f, "security-"+name+"-receipts", map[string]any{"read": zooSecurityOriginal(t, f, read), "write": zooSecurityOriginal(t, f, write), "verified": zooSecurityOriginal(t, f, verified), "original": zooSecurityOriginal(t, f, last.Result), "recovered": zooSecurityOriginal(t, f, got.Result), "generation": doc.Scope().Generation})
		})
	}
}

func TestZooKeeperNativePublicSASLRequiredAuthenticationAndOriginalReceipt(t *testing.T) {
	f := zookeeperfixture.ReadSecurity(t)
	_, driver := f.Install(t)
	m, ctx := zooSecurityRuntime(t, f, driver)
	p := f.EndpointProfile(t, driver, 30, f.Security.SASLEndpoint)
	p.Username = f.Security.Username
	p.ZooKeeperOptions.Auth = "sasl_digest"
	if _, err := m.ConnectSession(ctx, p, f.Security.SASLSecret); err != nil {
		t.Fatal("real mandatory SASL publication", err)
	}
	l := zooSecurityLease(t, m, p.ID, 1)
	read := nativeZooCall(t, l, "kv_get", map[string]string{"key": "/nyadb_secure"})
	generation := m.Generation(p.ID)
	var rejected []map[string]any
	for _, mode := range []string{"wrong-secret", "none", "digest"} {
		other := p
		secret := "deliberately-wrong-sasl"
		if mode == "none" {
			other.ZooKeeperOptions.Auth = "none"
			other.Username = ""
			secret = ""
		}
		if mode == "digest" {
			other.ZooKeeperOptions.Auth = "digest"
			secret = f.Security.SASLSecret
		}
		deadline, cancel := context.WithTimeout(ctx, 8*time.Second)
		before := f.Starts.Load()
		err := m.Test(deadline, other, secret)
		contextErr := deadline.Err()
		cancel()
		starts := f.Starts.Load() - before
		if err == nil || contextErr != nil || starts != 1 {
			t.Fatal("SASL rejection must come from one original process before the test deadline", mode, err, contextErr, starts)
		}
		rejected = append(rejected, map[string]any{"mode": mode, "error": err.Error(), "original_process_starts": starts, "test_deadline_expired": false})
	}
	write := nativeZooCall(t, l, "kv_put", nativeZooPut("/nyadb_secure/native-sasl", []byte{0, 255, 128}, "persistent"))
	verified := nativeZooCall(t, l, "kv_get", map[string]string{"key": "/nyadb_secure/native-sasl"})
	if m.Generation(p.ID) != generation || !bytes.Equal(verified.ZNode.Reply.Node.Value, []byte{0, 255, 128}) {
		t.Fatal("failed SASL probes invalidated original publication or bytes")
	}
	s := New(Dependencies{ProviderFor: m.KVRequests})
	t.Cleanup(s.CloseAll)
	doc := s.Open(9, p.ID, contract.KVRequest{Method: "kv_get", JSON: `{"key":"/nyadb_secure/native-sasl"}`})
	s.Run(doc)
	nativeZooConsume(t, s, doc)
	zooSecurityView(t, f, s, doc, "sasl-receipt")
	zooSecurityRecord(t, f, "security-sasl-receipts", map[string]any{"read": zooSecurityOriginal(t, f, read), "write": zooSecurityOriginal(t, f, write), "verified": zooSecurityOriginal(t, f, verified), "generation": generation, "rejected": rejected})
}

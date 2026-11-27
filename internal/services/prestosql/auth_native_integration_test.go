package prestosql

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/driverstore"
	"github.com/nyakang/nyadb/internal/services/jdbcplugin"
)

func TestPrestoSQLPrivateAuthNativeTLSCredentialsAndRecovery(t *testing.T) {
	if os.Getenv("NYADB_PRESTOSQL_AUTH_IT") != "1" {
		t.Skip("explicit original TLS/password fixture required")
	}
	var f struct{ Endpoint, WrongHost, Directory, Archive, SHA256, TrustStore, StorePassword, Password string }
	raw, err := os.ReadFile(os.Getenv("NYADB_PRESTOSQL_AUTH_FIXTURE"))
	if err != nil || json.Unmarshal(raw, &f) != nil {
		t.Fatal("original private auth fixture unavailable")
	}
	u, err := url.Parse(f.Endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() != "localhost" {
		t.Fatal("owned TLS loopback fixture required")
	}
	badHost, err := url.Parse(f.WrongHost)
	if err != nil || badHost.Scheme != "https" || badHost.Hostname() != "127.0.0.1" || badHost.Port() != u.Port() {
		t.Fatal("owned negative hostname fixture required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
	defer cancel()
	store, err := driverstore.New(filepath.Join(f.Directory, "store"))
	if err != nil {
		t.Fatal(err)
	}
	archive, err := os.Open(f.Archive)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := store.Install(ctx, archive, f.SHA256)
	archive.Close()
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var processes []int
	var receipts []map[string]any
	p := Provider{Store: store, Started: func(pid int) error {
		mu.Lock()
		defer mu.Unlock()
		processes = append(processes, pid)
		file, err := os.OpenFile(filepath.Join(f.Directory, "go-processes.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer file.Close()
		return json.NewEncoder(file).Encode(map[string]any{"pid": pid})
	}, Observe: func(method string, r jdbcplugin.Receipt) {
		digest := sha256.Sum256(r.OriginalResponse())
		encoded, _ := json.Marshal(r)
		if strings.Contains(string(encoded), f.Password) || strings.Contains(string(encoded), f.StorePassword) {
			t.Error("credential exposed in ordinary native receipt")
		}
		mu.Lock()
		defer mu.Unlock()
		receipts = append(receipts, map[string]any{"method": method, "receipt": r.Clone(), "original_sha256": hex.EncodeToString(digest[:])})
	}}
	c := connections.PrestoSQLManagedConfig{Endpoint: f.Endpoint, Username: "nyadb_tls65", Catalog: "memory", Schema: "default", DriverVersion: manifest.Version, DriverSHA256: f.SHA256}
	trusted := url.Values{"SSLVerification": {"FULL"}, "SSLTrustStorePath": {f.TrustStore}, "SSLTrustStoreType": {"PKCS12"}, "source": {"nyadb_tls65"}}.Encode()
	secret := Credentials{Password: f.Password, Parameters: map[string]string{"SSLTrustStorePassword": f.StorePassword}}
	type refusal struct {
		name        string
		config      connections.PrestoSQLManagedConfig
		credentials Credentials
	}
	cases := []refusal{{"untrusted_certificate", c, Credentials{Password: f.Password}}}
	c.JDBCParameters = trusted
	wrong := c
	wrong.Endpoint = f.WrongHost
	cases = append(cases, refusal{"wrong_hostname", wrong, secret}, refusal{"wrong_password", c, Credentials{Password: "invalid-fixture-password", Parameters: secret.Parameters}}, refusal{"wrong_truststore_password", c, Credentials{Password: f.Password, Parameters: map[string]string{"SSLTrustStorePassword": "invalid-fixture-store-password"}}})
	var rejected []string
	for _, test := range cases {
		opened, err := p.OpenManaged(ctx, 65, test.config, test.credentials)
		if opened != nil {
			_ = opened.Close()
			t.Fatal("negative auth case accepted", test.name)
		}
		if err == nil {
			t.Fatal("negative auth case had no failure", test.name)
		}
		if strings.Contains(err.Error(), f.Password) || strings.Contains(err.Error(), f.StorePassword) {
			t.Fatal("credential exposed in public auth error", test.name)
		}
		rejected = append(rejected, test.name)
	}
	s, err := p.OpenManaged(ctx, 65, c, secret)
	if err != nil {
		t.Fatal("trusted native password session failed", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	// After successful opening the caller replaces all credential inputs. New
	// owners and explicit replacements must use the original session snapshot.
	secret.Password = "caller changed password"
	secret.Parameters["SSLTrustStorePassword"] = "caller changed trust password"
	q, err := s.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	first := queryNative(t, q, "", "SELECT current_user, BIGINT '65'")
	check := func(r contract.SQLResult) {
		t.Helper()
		if len(r.Sets) != 1 || len(r.Sets[0].Rows) != 1 || r.Sets[0].Rows[0][0].Text != "nyadb_tls65" || r.Sets[0].Rows[0][1].Text != "65" {
			t.Fatal("authenticated original identity changed")
		}
	}
	check(first)
	old := q
	q, err = q.Replace()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = old.Execute(ctx, "", "SELECT 99"); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("replaced auth owner executed SQL", err)
	}
	replaced := queryNative(t, q, "", "SELECT current_user, BIGINT '65'")
	check(replaced)
	_ = s.Close()
	if _, err = q.Execute(ctx, "", "SELECT 99"); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed auth owner implicitly recovered", err)
	}
	recovered, err := p.OpenManaged(ctx, 65, c, Credentials{Password: f.Password, Parameters: map[string]string{"SSLTrustStorePassword": f.StorePassword}})
	if err != nil {
		t.Fatal("explicit original-profile auth recovery", err)
	}
	t.Cleanup(func() { _ = recovered.Close() })
	q2, err := recovered.ReserveQuery(2)
	if err != nil {
		t.Fatal(err)
	}
	recovery := queryNative(t, q2, "", "SELECT current_user, BIGINT '65'")
	check(recovery)
	_ = recovered.Close()
	if err = store.Remove(ctx, "prestosql", manifest.Version); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(processes) != 9 {
		t.Fatal("unexpected original auth owners", len(processes))
	}
	raw, err = json.MarshalIndent(map[string]any{"rejected": rejected, "first": first, "replaced": replaced, "recovered": recovery, "processes": processes, "receipts": receipts, "original_credential_snapshot_preserved": true, "explicit_recovery_only": true, "package_removed": true}, "", "  ")
	if err != nil || strings.Contains(string(raw), f.Password) || strings.Contains(string(raw), f.StorePassword) {
		t.Fatal("unsafe auth evidence")
	}
	if err = os.WriteFile(filepath.Join(f.Directory, "go-auth.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

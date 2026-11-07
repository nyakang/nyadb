package dbruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestTrinoNativeRuntimePublicationFixture(t *testing.T) {
	if os.Getenv("NYADB_TRINO_IT") != "1" {
		t.Skip("requires disposable native Trino fixture")
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_TRINO_CONFIG"))
	if err != nil {
		t.Fatal("missing private fixture configuration")
	}
	var cfg struct {
		Endpoint, Username, Catalog, Schema string
		PasswordFile                        string `json:"password_file"`
		CAFile                              string `json:"ca_file"`
		ServerName                          string `json:"server_name"`
	}
	if err = json.Unmarshal(raw, &cfg); err != nil || !strings.HasPrefix(cfg.Endpoint, "https://127.0.0.1:") && !strings.HasPrefix(cfg.Endpoint, "http://127.0.0.1:") {
		t.Fatal("owned loopback fixture required")
	}
	endpoint, _ := url.Parse(cfg.Endpoint)
	port, err := strconv.ParseUint(endpoint.Port(), 10, 16)
	if err != nil {
		t.Fatal("invalid fixture port")
	}
	password, err := os.ReadFile(cfg.PasswordFile)
	if err != nil {
		t.Fatal("missing private fixture password")
	}
	p, _ := connections.Defaults(connections.Trino)
	p.ID = 77
	p.Name = "runtime fixture"
	p.Host = "127.0.0.1"
	p.Port = uint16(port)
	p.Database = cfg.Catalog
	p.TrinoOptions.DefaultSchema = cfg.Schema
	p.Username = cfg.Username
	p.TLSMode = "disable"
	if cfg.CAFile != "" {
		p.TLSMode = "verify-full"
		p.TrinoOptions.CAFile = cfg.CAFile
		p.TrinoOptions.ServerName = cfg.ServerName
	}
	ctx := context.Background()
	m := New()
	RegisterBuiltins(m, nil)
	defer m.Close()
	first, err := m.ConnectSession(ctx, p, string(password))
	if err != nil {
		t.Fatal(err)
	}
	meta, err := contract.Require[contract.MetadataProvider](first)
	if err != nil {
		t.Fatal(err)
	}
	ns, err := meta.Namespaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	label := `"` + p.Database + `"."` + cfg.Schema + `"`
	wanted := m.Namespace(p.ID, label)
	found := false
	for _, n := range ns {
		if n.Ref == wanted {
			found = true
		}
	}
	if !found || wanted.Catalog != p.Database || wanted.Schema != cfg.Schema {
		t.Fatal("runtime lost native catalog/database identity")
	}
	runtime := contract.ComposeData(first)
	if first.Capabilities().Explain {
		t.Fatal("runtime advertised unverified plan tools")
	}
	if runtime.AdvancedLoader == nil || runtime.TableMetadata == nil || runtime.Exporter == nil || runtime.Saver != nil || runtime.Importer != nil || runtime.Transactions.Begin != nil || runtime.Capabilities.EditableTables {
		t.Fatal("runtime composed unsupported OLTP tools")
	}
	if len(runtime.ExportFormats) != 6 {
		t.Fatal("runtime lost native export format limits")
	}
	if m.Administration(p.ID) != nil {
		t.Fatal("Trino received MySQL administration")
	}
	provider, err := contract.Require[contract.QueryProvider](first)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := provider.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if _, err = lease.Execute(ctx, label, "SELECT 1"); err != nil {
		t.Fatal(err)
	}
	second, err := m.ConnectSession(ctx, p, string(password))
	if err != nil {
		t.Fatal(err)
	}
	if second.Generation() <= first.Generation() {
		t.Fatal("public generation did not advance")
	}
	if _, err = meta.Namespaces(ctx); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old metadata binding not revoked")
	}
	if _, err = lease.Execute(ctx, label, "SELECT 2"); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old query lease used replacement session")
	}
	if _, err = runtime.TableMetadata(ctx, p.ID, label, "unused"); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old data binding used replacement session")
	}
	_ = first.Close()
	if !m.IsConnected(p.ID) {
		t.Fatal("late old close revoked replacement publication")
	}
	if err = m.Disconnect(p.ID); err != nil {
		t.Fatal(err)
	}
}

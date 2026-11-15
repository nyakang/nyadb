package localdb

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestH2ProfileRestartDuplicateAndStrictEnvelope(t *testing.T) {
	store, path, db := openStore(t)
	ctx := context.Background()
	p, _ := connections.Defaults(connections.H2)
	p.Name, p.Database = "H2 file", filepath.Join(t.TempDir(), "original")
	p.H2Options.DriverVersion, p.H2Options.DriverSHA256 = "owned", strings.Repeat("a", 64)
	p.H2Options.DriverDirectory = t.TempDir()
	p.H2Options.MaxConnections, p.H2Options.MetadataReserve, p.H2Options.SQLMaxConnections = 4, 1, 3
	p.H2Options.PoolWaitTimeoutMilliseconds = 1500
	saved, err := store.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	copy, err := connections.NewService(store).Duplicate(ctx, saved.ID, "copy")
	if err != nil || copy.H2Options != p.H2Options || copy.Kind != connections.H2 {
		t.Fatal("duplicate H2 identity changed", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	store = NewProfileStore(reopened)
	got, err := store.Get(ctx, saved.ID)
	if err != nil || got.H2Options != p.H2Options || got.Database != p.Database {
		t.Fatal("H2 settings did not survive restart", err)
	}
	valid, err := encodeProfileOptions(p)
	if err != nil {
		t.Fatal(err)
	}
	legacy := strings.Replace(valid, `,"auto_server":true`, "", 1)
	var legacyProfile connections.Profile
	legacyProfile.Kind = connections.H2
	if err := decodeEngineOptions(&legacyProfile, legacy); err != nil || legacyProfile.H2Options.AutoServer {
		t.Fatal("legacy saved file silently switched to shared mode", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(valid), &fields); err != nil {
		t.Fatal(err)
	}
	fields["sqlite"] = json.RawMessage(`null`)
	foreign, _ := json.Marshal(fields)
	for _, raw := range []string{"", `{"version":1,"kind":"h2","h2":null}`, strings.Replace(valid, `"h2":`, `"h2":null,"h2":`, 1), strings.Replace(valid, `"pool_wait_timeout_milliseconds":1500`, `"pool_wait_timeout_milliseconds":149`, 1), strings.Replace(valid, `"h2":{`, `"h2":{"password":"secret",`, 1), string(foreign)} {
		if _, err := reopened.Exec("UPDATE connection_profiles SET mysql_advanced_json=? WHERE id=?", raw, saved.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Get(ctx, saved.ID); err == nil {
			t.Fatal("invalid H2 envelope accepted")
		}
	}
	other, _ := connections.Defaults(connections.SQLite)
	base, _ := encodeProfileOptions(other)
	base = strings.TrimSuffix(base, "}") + `,"H2":null}`
	if err := decodeEngineOptions(&other, base); err == nil {
		t.Fatal("foreign null H2 envelope accepted by SQLite")
	}
}

func TestH2TLSRootsSurviveProfileStorageRestart(t *testing.T) {
	store, path, db := openStore(t)
	p, _ := connections.Defaults(connections.H2)
	p.Name, p.Host, p.Port, p.Database = "H2 TLS original", "127.0.0.1", 9092, "owned"
	p.H2Options.Mode, p.H2Options.AutoServer, p.H2Options.TLSCAFile = "ssl", false, filepath.Join(t.TempDir(), "original.pem")
	saved, err := store.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := NewProfileStore(reopened).Get(context.Background(), saved.ID)
	if err != nil || got.H2Options != p.H2Options || got.Host != p.Host {
		t.Fatal("TLS profile lost original roots or host", err)
	}
}

func TestH2AutomaticCustomAndOldFixedDriverPinsSurviveRestart(t *testing.T) {
	store, path, db := openStore(t)
	ctx := context.Background()
	var saved []connections.Profile
	for _, profile := range []string{"h2-auto", "h2-custom", "h2-v1", "h2-v2", "h2-v3", "h2-legacy"} {
		p, err := connections.Defaults(connections.H2)
		if err != nil || p.H2Options.DriverProfile != "h2-auto" {
			t.Fatal("new H2 defaults do not request automatic selection", err)
		}
		p.Name, p.Database = profile, filepath.Join(t.TempDir(), "original")
		p.H2Options.DriverProfile, p.H2Options.DriverVersion, p.H2Options.DriverSHA256 = profile, "original-"+profile, strings.Repeat("a", 64)
		p.H2Options.DriverDirectory = t.TempDir()
		original, err := store.Create(ctx, p)
		if err != nil {
			t.Fatal("original driver profile was not stored", profile, err)
		}
		saved = append(saved, original)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	store = NewProfileStore(reopened)
	for _, original := range saved {
		got, err := store.Get(ctx, original.ID)
		if err != nil || got.H2Options != original.H2Options || got.Database != original.Database {
			t.Fatal("saved automatic/custom/old fixed pin changed after restart", original.Name, err)
		}
		copy, err := connections.NewService(store).Duplicate(ctx, got.ID, got.Name+" copy")
		if err != nil || copy.H2Options != original.H2Options || copy.ID == original.ID {
			t.Fatal("duplicate lost original complete driver identity", original.Name, err)
		}
	}
}

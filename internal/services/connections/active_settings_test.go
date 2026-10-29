package connections_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestActiveSettingsResolveAndRoundTrip(t *testing.T) {
	store, _, _ := openStore(t)
	svc := connections.NewService(store)
	main := connections.DefaultMySQLAdvanced()
	other := connections.DefaultMySQLAdvanced()
	other.ConnectTimeoutSeconds = 48
	p, err := svc.Create(context.Background(), connections.Profile{
		Name: "Environment", Kind: connections.MySQL, Host: "primary.example.test", Port: 3306,
		Username: "root", MySQLAdvanced: main,
		Variants: []connections.ConnectionVariant{{Name: "Staging", Host: "staging.example.test",
			Port: 3307, Username: "reader", MySQLAdvanced: other}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.ActiveSettings(context.Background(), p.ID)
	if err != nil || got.Host != p.Host || got.ConfigurationName != "" ||
		got.Port != 3306 || got.ConnectionName != "Environment" {
		t.Fatalf("primary settings not selected: %+v %v", got, err)
	}
	p.ActiveVariant = "staging"
	p, err = svc.Update(context.Background(), p)
	if err != nil || p.ActiveVariant != "Staging" {
		t.Fatalf("active canonical name not persisted: %+v %v", p, err)
	}
	got, err = svc.ActiveSettings(context.Background(), p.ID)
	if err != nil || got.ProfileID != p.ID || got.ConfigurationName != "Staging" ||
		got.Host != "staging.example.test" || got.Port != 3307 ||
		got.Username != "reader" || got.MySQLAdvanced.ConnectTimeoutSeconds != 48 {
		t.Fatalf("active variant settings not resolved: %+v %v", got, err)
	}
	again, err := svc.Duplicate(context.Background(), p.ID, "Environment Copy")
	if err != nil || again.ActiveVariant != "Staging" {
		t.Fatalf("duplicated connection changed active selection: %+v %v", again, err)
	}
	p.ActiveVariant = ""
	p, err = svc.Update(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	got, err = svc.ActiveSettings(context.Background(), p.ID)
	if err != nil || got.Host != "primary.example.test" || got.ConfigurationName != "" {
		t.Fatalf("switch back to main failed: %+v %v", got, err)
	}
}

func TestActiveSettingsRejectDanglingOrUnsupported(t *testing.T) {
	advanced := connections.DefaultMySQLAdvanced()
	base := connections.Profile{Name: "Demo", Kind: connections.MySQL, Host: "localhost", Port: 3306,
		MySQLAdvanced: advanced, Variants: []connections.ConnectionVariant{
			{Name: "Replica", Host: "replica.example.test", Port: 3307, MySQLAdvanced: advanced},
		}}
	for _, active := range []string{"Missing", "Primary", "Replica-renamed"} {
		bad := base
		bad.ActiveVariant = active
		if _, err := connections.ResolveActiveSettings(bad); !errors.Is(err, connections.ErrInvalid) {
			t.Fatalf("missing Active %q resolved: %v", active, err)
		}
	}
	bad := connections.Profile{Name: "Pg", Kind: connections.PostgreSQL, Host: "localhost", Port: 5432}
	if _, err := connections.ResolveActiveSettings(bad); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("resolved a non-MySQL profile as MySQL")
	}
	bad.ActiveVariant = "Fake"
	if _, err := connections.NormalizeProfile(bad); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("non-MySQL record accepted alternative Active target")
	}
	// The resolver must not mutate a caller's configurations.
	base.Variants[0].Host = "  replica.example.test "
	if _, err := connections.ResolveActiveSettings(base); err != nil {
		t.Fatal(err)
	}
	if base.Variants[0].Host != "  replica.example.test " {
		t.Fatal("resolution mutated input profile")
	}
}

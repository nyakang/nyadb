package localdb

import (
	"context"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestMySQLConnectionVariantsRoundTripAndCopy(t *testing.T) {
	store, _, _ := openStore(t)
	ctx := context.Background()
	p, err := store.Create(ctx, connections.Profile{
		Name: "Lab", Kind: connections.MySQL, Host: "localhost", Port: 3306,
		Variants: []connections.ConnectionVariant{
			{Name: "Staging", Host: "staging.example.test", Port: 3307, Username: "reader",
				MySQLAdvanced: connections.DefaultMySQLAdvanced()},
			{Name: "Replica", Host: "replica.example.test", Port: 3308,
				MySQLAdvanced: connections.DefaultMySQLAdvanced()},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, p.ID)
	if err != nil || len(got.Variants) != 2 ||
		got.Variants[0].Host != "staging.example.test" ||
		got.Variants[1].Port != 3308 {
		t.Fatalf("variant persistence: %+v (%v)", got.Variants, err)
	}
	got.Variants[0].Host = "changed.example.test"
	got, err = store.Update(ctx, got)
	if err != nil || got.Variants[0].Host != "changed.example.test" {
		t.Fatalf("updating variant: %+v %v", got.Variants, err)
	}
	copy, err := connections.NewService(store).Duplicate(ctx, p.ID, "Lab copy")
	if err != nil || len(copy.Variants) != 2 || copy.Variants[0].Host != got.Variants[0].Host {
		t.Fatalf("duplicate lost variants: %+v %v", copy.Variants, err)
	}
	profiles, err := store.List(ctx)
	if err != nil || len(profiles) != 2 || len(profiles[0].Variants) != 2 {
		t.Fatalf("variants affected connection root count: %+v %v", profiles, err)
	}
}

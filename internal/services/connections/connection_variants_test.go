package connections

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestMySQLConnectionVariantsRoundTripAndCopy(t *testing.T) {
	store, _ := openStore(t)
	ctx := context.Background()
	p, err := store.Create(ctx, Profile{
		Name: "Lab", Kind: MySQL, Host: "localhost", Port: 3306,
		Variants: []ConnectionVariant{
			{Name: "Staging", Host: "staging.example.test", Port: 3307, Username: "reader",
				MySQLAdvanced: DefaultMySQLAdvanced()},
			{Name: "Replica", Host: "replica.example.test", Port: 3308,
				MySQLAdvanced: DefaultMySQLAdvanced()},
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
	copy, err := NewService(store).Duplicate(ctx, p.ID, "Lab copy")
	if err != nil || len(copy.Variants) != 2 || copy.Variants[0].Host != got.Variants[0].Host {
		t.Fatalf("duplicate lost variants: %+v %v", copy.Variants, err)
	}
	profiles, err := store.List(ctx)
	if err != nil || len(profiles) != 2 || len(profiles[0].Variants) != 2 {
		t.Fatalf("variants affected connection root count: %+v %v", profiles, err)
	}
}

func TestMySQLConnectionVariantsRejectInvalid(t *testing.T) {
	store, _ := openStore(t)
	for _, variants := range [][]ConnectionVariant{
		{{Name: "", Host: "localhost", Port: 3306}},
		{{Name: "New", Host: "localhost", Port: 0}},
		{{Name: "same", Host: "localhost", Port: 3306}, {Name: "SAME", Host: "localhost", Port: 3306}},
		{{Name: "No secrets", Host: "https://name:password@host", Port: 3306}},
		{{Name: strings.Repeat("x", 121), Host: "localhost", Port: 3306}},
	} {
		_, err := store.Create(context.Background(), Profile{
			Name: "Lab", Kind: MySQL, Host: "localhost", Port: 3306,
			Variants: variants,
		})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted invalid alternative: %+v, err=%v", variants, err)
		}
	}
	_, err := store.Create(context.Background(), Profile{
		Name: "SQLite", Kind: SQLite, Database: "/tmp/test.sqlite",
		Variants: []ConnectionVariant{{Name: "Extra", Host: "localhost", Port: 3306}},
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("accepted SQLite variant: %v", err)
	}
}

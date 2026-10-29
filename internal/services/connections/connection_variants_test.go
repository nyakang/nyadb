package connections_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestMySQLConnectionVariantsRejectInvalid(t *testing.T) {
	store, _, _ := openStore(t)
	for _, variants := range [][]connections.ConnectionVariant{
		{{Name: "", Host: "localhost", Port: 3306}},
		{{Name: "New", Host: "localhost", Port: 0}},
		{{Name: "same", Host: "localhost", Port: 3306}, {Name: "SAME", Host: "localhost", Port: 3306}},
		{{Name: "No secrets", Host: "https://name:password@host", Port: 3306}},
		{{Name: strings.Repeat("x", 121), Host: "localhost", Port: 3306}},
	} {
		_, err := store.Create(context.Background(), connections.Profile{
			Name: "Lab", Kind: connections.MySQL, Host: "localhost", Port: 3306,
			Variants: variants,
		})
		if !errors.Is(err, connections.ErrInvalid) {
			t.Fatalf("accepted invalid alternative: %+v, err=%v", variants, err)
		}
	}
	_, err := store.Create(context.Background(), connections.Profile{
		Name: "SQLite", Kind: connections.SQLite, Database: "/tmp/test.sqlite",
		Variants: []connections.ConnectionVariant{{Name: "Extra", Host: "localhost", Port: 3306}},
	})
	if !errors.Is(err, connections.ErrInvalid) {
		t.Fatalf("accepted SQLite variant: %v", err)
	}
}

package connections_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestConnectionNotesValidation(t *testing.T) {
	store, _, _ := openStore(t)
	for _, notes := range []string{
		strings.Repeat("a", 64*1024+1),
		"bad\x00note",
		string([]byte{0xff}),
	} {
		_, err := store.Create(context.Background(), connections.Profile{
			Name: "invalid notes", Kind: connections.MySQL, Host: "localhost", Port: 3306, Notes: notes,
		})
		if !errors.Is(err, connections.ErrInvalid) {
			t.Fatalf("expected ErrInvalid for invalid notes, got %v", err)
		}
	}
	_, err := store.Create(context.Background(), connections.Profile{
		Name: "max notes", Kind: connections.MySQL, Host: "localhost", Port: 3306,
		Notes: strings.Repeat("x", 64*1024),
	})
	if err != nil {
		t.Fatalf("valid max-length notes rejected: %v", err)
	}
}

package connections_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestCompatibilityRejectsInvalidChoices(t *testing.T) {
	store, _, _ := openStore(t)
	for _, change := range []func(*connections.MySQLCompatibility){
		func(c *connections.MySQLCompatibility) { c.LowerCaseTableNames = "3" },
		func(c *connections.MySQLCompatibility) { c.NDBSupport = "unknown" },
		func(c *connections.MySQLCompatibility) { c.DatabaseMethod = "random" },
		func(c *connections.MySQLCompatibility) { c.ViewMethod = "random" },
		func(c *connections.MySQLCompatibility) { c.SQLMode = "bad\x00mode" },
	} {
		a := connections.DefaultMySQLAdvanced()
		change(&a.Compatibility)
		_, err := store.Create(context.Background(), connections.Profile{
			Name: "bad", Kind: connections.MySQL, Host: "localhost", Port: 3306, MySQLAdvanced: a,
		})
		if !errors.Is(err, connections.ErrInvalid) {
			t.Fatalf("invalid compatibility accepted: %+v (%v)", a.Compatibility, err)
		}
	}
}

package dbruntime

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestMongoDBPublishedGenerationsRevokeCapturedFeatures(t *testing.T) {
	if os.Getenv("NYADB_MONGODB_IT") != "1" {
		t.Skip("requires disposable MongoDB")
	}
	port, err := strconv.Atoi(os.Getenv("NYADB_MONGODB_PORT"))
	if err != nil || port < 1024 || port > 65535 {
		t.Fatal("explicit loopback port required")
	}
	p, _ := connections.Defaults(connections.MongoDB)
	p.ID = 93
	p.Name = "MongoDB runtime"
	p.Host = "127.0.0.1"
	p.Port = uint16(port)
	p.Username = os.Getenv("NYADB_MONGODB_USERNAME")
	p.MongoDBOptions.DirectConnection = true
	m := New()
	RegisterBuiltins(m, nil)
	t.Cleanup(func() { m.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err = m.Connect(ctx, p, os.Getenv("NYADB_MONGODB_PASSWORD")); err != nil {
		t.Fatal(err)
	}
	first, _ := m.Session(p.ID)
	provider, err := contract.Require[contract.QueryProvider](first)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := provider.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	data := m.Data(p.ID)
	if !first.Capabilities().Query || !first.Capabilities().EditableTables || first.Capabilities().Transactions || data.JSONL == nil {
		t.Fatal("incorrect runtime feature intersection")
	}
	if _, err = lease.Execute(ctx, "admin", `{"ping":1}`); err != nil {
		t.Fatal(err)
	}
	if err = m.Connect(ctx, p, os.Getenv("NYADB_MONGODB_PASSWORD")); err != nil {
		t.Fatal(err)
	}
	second, _ := m.Session(p.ID)
	if second.Generation() <= first.Generation() {
		t.Fatal("public generation did not advance")
	}
	if _, err = lease.Execute(ctx, "admin", `{"ping":1}`); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old query lease executed", err)
	}
	if _, err = data.Count(ctx, p.ID, "admin", "records"); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("captured reads used new session", err)
	}
	if _, err = data.Saver(ctx, p.ID, "admin", "records", []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"document": {Text: `{"x":1}`, Kind: contract.JSON}}}}); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("captured write used new session", err)
	}
}

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

func TestRedisPublishedGenerationsRevokeCapturedFeatures(t *testing.T) {
	if os.Getenv("NYADB_REDIS_IT") != "1" {
		t.Skip("requires disposable Redis")
	}
	port, err := strconv.Atoi(os.Getenv("NYADB_REDIS_PORT"))
	if err != nil || port < 1024 || port > 65535 {
		t.Fatal("explicit loopback port required")
	}
	p, _ := connections.Defaults(connections.Redis)
	p.ID = 93
	p.Name = "Redis runtime"
	p.Host = "127.0.0.1"
	p.Port = uint16(port)
	p.Username = os.Getenv("NYADB_REDIS_USERNAME")
	if os.Getenv("NYADB_REDIS_TLS") == "1" {
		p.TLSMode = "verify-full"
		p.RedisOptions.CAFile = os.Getenv("NYADB_REDIS_CA")
	}
	m := New()
	RegisterBuiltins(m, nil)
	t.Cleanup(func() { m.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err = m.Connect(ctx, p, os.Getenv("NYADB_REDIS_PASSWORD")); err != nil {
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
	if _, err = lease.Execute(ctx, "0", `["PING"]`); err != nil {
		t.Fatal(err)
	}
	if err = m.Connect(ctx, p, os.Getenv("NYADB_REDIS_PASSWORD")); err != nil {
		t.Fatal(err)
	}
	second, _ := m.Session(p.ID)
	if second.Generation() <= first.Generation() {
		t.Fatal("public generation did not advance")
	}
	if _, err = lease.Execute(ctx, "0", `["PING"]`); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old query lease executed", err)
	}
	if _, err = data.Count(ctx, p.ID, "0", "keys"); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("captured reads used new session", err)
	}
	if _, err = data.Saver(ctx, p.ID, "0", "keys", []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"key": {Text: "eA=="}, "type": {Text: "string"}, "value": {Text: `{"string":"eA=="}`, Kind: contract.JSON}}}}); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("captured write used new session", err)
	}
}

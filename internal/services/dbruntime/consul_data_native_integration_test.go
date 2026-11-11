package dbruntime_test

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/consul"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

func consulDataReserve(t *testing.T, m *dbruntime.Manager, id int64) contract.DataDocumentLease {
	t.Helper()
	l, err := m.Data(id).ReserveDocument(m.Namespace(id, "kv").Object("keyspace", "keys"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func TestConsulNativePublicDataOriginalLiveReadOnlyRuntimeAndRestrictedScope(t *testing.T) {
	p, secret := consulPublicProfile(t)
	p.ConsulOptions.Prefix = "native/public-data/" + t.Name() + "/"
	m := dbruntime.New()
	defer m.Close()
	dbruntime.RegisterBuiltins(m, nil)
	h, err := m.ConnectSession(context.Background(), p, secret)
	if err != nil {
		t.Fatal(err)
	}
	provider, _ := m.KVRequests(p.ID)
	writer, err := provider.ReserveKV(99)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	value := bytes.Repeat([]byte{0, 255}, 1024)
	for _, name := range []string{"a", "b", "d"} {
		consulRuntimeDo(t, writer, consulRuntimePut(t, p.ConsulOptions.Prefix+name, value))
	}
	lease := consulDataReserve(t, m, p.ID)
	r := lease.Runtime()
	object := m.Namespace(p.ID, "kv").Object("keyspace", "keys")
	if r.Capabilities != h.Capabilities() || r.Saver != nil || r.Transactions.Begin != nil || r.Importer != nil || r.JSONL != nil || r.AdvancedLoader == nil || r.Count == nil || r.Chunk == nil || r.Exporter == nil {
		t.Fatal("runtime exposed unsupported data tools", r.Capabilities)
	}
	page, err := r.AdvancedLoader(context.Background(), p.ID, "kv", "keys", contract.DataOptions{Object: object, Limit: 2})
	if err != nil || !page.LiveRead || page.PagingMode != "cursor" || !page.HasMore || *page.Editable || page.Rows[0][2].Value.Text != "18446744073709551615" {
		t.Fatal("bound live typed page changed", err)
	}
	peer := consulDataReserve(t, m, p.ID)
	if _, err := peer.Runtime().AdvancedLoader(context.Background(), p.ID, "kv", "keys", contract.DataOptions{Limit: 2, Offset: 2, After: page.NextCursor}); err == nil {
		t.Fatal("cursor crossed original document wrapper")
	}
	remove, _ := consul.KeyDeleteRequest(p.ConsulOptions.Prefix+"b", nil, false)
	consulRuntimeDo(t, writer, remove)
	consulRuntimeDo(t, writer, consulRuntimePut(t, p.ConsulOptions.Prefix+"c", []byte{3}))
	next, err := r.AdvancedLoader(context.Background(), p.ID, "kv", "keys", contract.DataOptions{Limit: 2, Offset: 2, After: page.NextCursor})
	if err != nil || len(next.Rows) != 2 || next.Rows[0][0].Value.Text != p.ConsulOptions.Prefix+"c" || !next.LiveRead {
		t.Fatal("runtime invented a fixed cross-page snapshot", err)
	}
	row, err := r.RowReader(context.Background(), p.ID, "kv", "keys", *page.RowRefs[0])
	if err != nil || !bytes.Equal(row.Values["value"].Bytes, value) {
		t.Fatal("original full value changed", err)
	}
	chunk, err := r.Chunk(context.Background(), p.ID, "kv", "keys", "value", *page.RowRefs[0], 0, 1024)
	if err != nil || chunk.NextOffset != 1024 || chunk.TotalUnits != 2048 {
		t.Fatal("original bounded chunk changed", err)
	}
	if n, err := r.Count(context.Background(), p.ID, "kv", "keys"); err != nil || n != 3 {
		t.Fatal("independent live count failed", err)
	}
	emitted := 0
	if err := r.Exporter(context.Background(), p.ID, "kv", "keys", contract.DataOptions{Object: object}, []string{"key", "value", "flags"}, func(values []contract.DataValue) error {
		if values[2].Text != "18446744073709551615" {
			t.Error("unsigned export value changed")
		}
		emitted++
		return nil
	}); err != nil || emitted != 3 {
		t.Fatal("bound original export failed", err)
	}
	if _, err := m.Data(p.ID).ReserveDocument((contract.NamespaceRef{ProfileID: p.ID + 1, Catalog: "kv"}).Object("keyspace", "keys")); err == nil {
		t.Fatal("foreign profile reservation accepted")
	}
	for _, token := range []string{os.Getenv("NYADB_CONSUL_READ_TOKEN"), os.Getenv("NYADB_CONSUL_WRITE_TOKEN")} {
		if token == "" {
			continue
		}
		limited := p
		limited.ID++
		limited.ConsulOptions.Prefix = "allowed/public-data/" + t.Name() + "/"
		consulRuntimeDo(t, writer, consulRuntimePut(t, limited.ConsulOptions.Prefix+"key", []byte{0, 255}))
		if _, err := m.ConnectSession(context.Background(), limited, token); err != nil {
			t.Fatal(err)
		}
		l := consulDataReserve(t, m, limited.ID)
		p, err := l.Runtime().AdvancedLoader(context.Background(), limited.ID, "kv", "keys", contract.DataOptions{Limit: 10})
		if err != nil || len(p.Rows) != 1 || p.Rows[0][0].Value.Text != limited.ConsulOptions.Prefix+"key" {
			t.Fatal("restricted runtime expanded original visible scope", err)
		}
		l.Close()
	}
	fresh, err := m.ConnectSession(context.Background(), p, secret)
	if err != nil || fresh.Generation() <= h.Generation() {
		t.Fatal("original replacement did not advance generation", err)
	}
	if _, err := r.Count(context.Background(), p.ID, "kv", "keys"); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("captured runtime selected replacement generation", err)
	}
}

func TestConsulNativePublicDataProcessedReadFaultsKeepOriginalRuntimeAndRequireReopen(t *testing.T) {
	cfg, secret := consulRuntimeConfig(t)
	for _, mode := range []string{"lost", "partial", "cancel", "replace", "close"} {
		t.Run(mode, func(t *testing.T) {
			cfg.Prefix = "native/public-data-fault/" + t.Name() + "/"
			seed, err := consul.OpenNativeSession(context.Background(), 1, cfg, secret)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { seed.Close() })
			writer, _ := seed.ReserveKV(1)
			defer writer.Close()
			consulRuntimeDo(t, writer, consulRuntimePut(t, cfg.Prefix+"key", []byte{0, 255}))
			armed := &atomic.Bool{}
			proxy, calls, processed := consulRuntimeFault(t, cfg, "data-"+mode, armed)
			p, _ := connections.Defaults(connections.Consul)
			u, _ := url.Parse(proxy.Endpoint)
			port, _ := strconv.Atoi(u.Port())
			p.ID, p.Name, p.Host, p.Port = 904, "original Consul read fault", u.Hostname(), uint16(port)
			p.ConsulOptions.Auth, p.ConsulOptions.Prefix = cfg.Auth, cfg.Prefix
			m := dbruntime.New()
			dbruntime.RegisterBuiltins(m, nil)
			defer m.Close()
			original, err := m.ConnectSession(context.Background(), p, secret)
			if err != nil {
				t.Fatal(err)
			}
			l := consulDataReserve(t, m, p.ID)
			r := l.Runtime()
			armed.Store(true)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			finished := make(chan error, 1)
			go func() {
				_, err := r.AdvancedLoader(ctx, p.ID, "kv", "keys", contract.DataOptions{Limit: 1})
				finished <- err
			}()
			consulRuntimeAwait(t, processed)
			switch mode {
			case "cancel":
				cancel()
			case "close":
				l.Close()
			case "replace":
				fresh, err := m.ConnectSession(context.Background(), p, secret)
				if err != nil || fresh.Generation() <= original.Generation() {
					t.Fatal("replacement failed", err)
				}
			}
			select {
			case err := <-finished:
				if err == nil {
					t.Fatal("lost original read became a complete page")
				}
			case <-time.After(15 * time.Second):
				t.Fatal("original executor did not acknowledge termination")
			}
			beforeRetry := calls.Load()
			if _, err := r.AdvancedLoader(context.Background(), p.ID, "kv", "keys", contract.DataOptions{Limit: 1}); err == nil || calls.Load() != beforeRetry {
				t.Fatal("retired runtime replayed or selected a replacement")
			}
			if mode != "close" && mode != "replace" {
				if n, err := r.Count(context.Background(), p.ID, "kv", "keys"); err != nil || n != 1 {
					t.Fatal("page retirement affected independent count", err)
				}
			}
			fresh := consulDataReserve(t, m, p.ID)
			page, err := fresh.Runtime().AdvancedLoader(context.Background(), p.ID, "kv", "keys", contract.DataOptions{Limit: 1})
			if err != nil || len(page.Rows) != 1 || calls.Load() < 2 {
				t.Fatal("explicit original document reopening failed", err)
			}
			fresh.Close()
		})
	}
}

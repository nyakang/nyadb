package h2

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestH2NativeTCPAndTLSAuthenticationDataCancellationAndRecovery(t *testing.T) {
	if os.Getenv("NYADB_H2_NETWORK_IT") != "1" {
		t.Skip("requires owned native H2 TCP/TLS servers")
	}
	provider, options, password, _ := managedH2Fixture(t)
	var fixture struct {
		Servers map[string]struct {
			TCPPort, SSLPort uint16
			CAFile           string
		}
		WrongHostPort uint16
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_MANAGED_H2_FIXTURE_FILE"))
	if err != nil || json.Unmarshal(raw, &fixture) != nil {
		t.Fatal("network fixture unavailable")
	}
	for _, profile := range []string{"h2-v1", "h2-v2", "h2-v3", "h2-legacy"} {
		for _, mode := range []string{"tcp", "ssl"} {
			t.Run(profile+"/"+mode, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				defer cancel()
				server := fixture.Servers[profile]
				a := options
				a.Mode, a.AutoServer, a.DriverProfile = mode, false, profile
				a.RequireExisting = true
				port := server.TCPPort
				if mode == "ssl" {
					port = server.SSLPort
					a.TLSCAFile = server.CAFile
				}
				p := connections.Profile{ID: 81, Name: "native original network", Kind: connections.H2, Host: "127.0.0.1", Port: port, Database: "owned", Username: "sa", H2Options: a}
				if mode == "ssl" {
					untrusted := p
					untrusted.H2Options.TLSCAFile = ""
					if opened, err := provider.Open(ctx, untrusted, password); err == nil {
						opened.Close()
						t.Fatal("unknown native TLS chain accepted")
					}
					wrong := p
					wrong.Port = fixture.WrongHostPort
					if opened, err := provider.Open(ctx, wrong, password); !errors.Is(err, errTLSTrust) {
						if opened != nil {
							opened.Close()
						}
						t.Fatal("native TLS certificate without original host was admitted", err)
					}
				}
				original, err := provider.Open(ctx, p, password)
				if err != nil {
					t.Fatal("native network session unavailable", err)
				}
				t.Cleanup(func() { original.Close() })
				query, err := contract.Require[contract.QueryProvider](original)
				if err != nil {
					t.Fatal(err)
				}
				lease, err := query.ReserveQuery(1)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(lease.Release)
				sessionID := func() string {
					t.Helper()
					result, err := lease.Execute(ctx, "PUBLIC", `SELECT SESSION_ID()`)
					if err != nil || len(result.Sets) != 1 || len(result.Sets[0].Rows) != 1 {
						t.Fatal("original native physical session unavailable", err)
					}
					return result.Sets[0].Rows[0][0].Text
				}
				id := sessionID()
				data := contract.ComposeData(original)
				page, err := data.Loader(ctx, p.ID, "PUBLIC", "network_records", 0, 10)
				if err != nil || page == nil || len(page.Rows) != 1 || page.Rows[0][0].Value.Text != "9007199254740993" {
					t.Fatal("native typed page lost", err)
				}
				if _, err := lease.Execute(ctx, "PUBLIC", `CREATE TABLE "network_edit_`+mode+`"("id" BIGINT PRIMARY KEY,"note" VARCHAR);INSERT INTO "network_edit_`+mode+`" VALUES(9007199254740993,'original')`); err != nil {
					t.Fatal(err)
				}
				object := "network_edit_" + mode
				page, err = data.Loader(ctx, p.ID, "PUBLIC", object, 0, 10)
				if err != nil {
					t.Fatal(err)
				}
				receipt, err := data.Saver(ctx, p.ID, "PUBLIC", object, []contract.DataEdit{{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{"note": {Text: "native confirmed network"}}}})
				if err != nil || receipt == nil || !receipt.CommitConfirmed || receipt.NativeJSON == "" {
					t.Fatal("native network save lost original receipt", err)
				}
				second, err := query.ReserveQuery(2)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(second.Release)
				observer, err := query.ReserveQuery(3)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(observer.Release)
				page, err = data.Loader(ctx, p.ID, "PUBLIC", object, 0, 10)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = second.Execute(ctx, "PUBLIC", `BEGIN;UPDATE "`+object+`" SET "note"="note" WHERE "id"=9007199254740993`); err != nil {
					t.Fatal(err)
				}
				blocked, cancelRead := context.WithCancel(ctx)
				type completion struct {
					receipt *contract.DataEditResult
					err     error
				}
				done := make(chan completion, 1)
				go func() {
					receipt, err := data.Saver(blocked, p.ID, "PUBLIC", object, []contract.DataEdit{{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{"note": {Text: "cancelled original waiter"}}}})
					done <- completion{receipt, err}
				}()
				deadline := time.Now().Add(12 * time.Second)
				observedBlocked := false
				for time.Now().Before(deadline) {
					q, err := observer.Execute(ctx, "PUBLIC", `SELECT COUNT(*) FROM INFORMATION_SCHEMA.SESSIONS WHERE BLOCKER_ID IS NOT NULL`)
					if err != nil {
						cancelRead()
						second.Execute(ctx, "PUBLIC", "ROLLBACK")
						<-done
						t.Fatal(err)
					}
					if len(q.Sets) == 1 && len(q.Sets[0].Rows) == 1 && q.Sets[0].Rows[0][0].Text != "0" {
						observedBlocked = true
						break
					}
					time.Sleep(25 * time.Millisecond)
				}
				cancelRead()
				if _, err = second.Execute(ctx, "PUBLIC", `ROLLBACK`); err != nil {
					t.Fatal(err)
				}
				select {
				case complete := <-done:
					if !observedBlocked || !errors.Is(complete.err, context.Canceled) || complete.receipt == nil || complete.receipt.CommitConfirmed {
						t.Fatal("original network executor did not acknowledge cancellation", complete.err)
					}
					var native transactionReceipt
					if json.Unmarshal([]byte(complete.receipt.NativeJSON), &native) != nil || len(native.Statements) != 1 || !native.Statements[0].Dispatched || !native.Statements[0].ExecutorAcknowledged && !native.Statements[0].ProcessExitConfirmed {
						t.Fatal("native cancel lost original executor receipt")
					}
				case <-time.After(30 * time.Second):
					t.Fatal("original native executor acknowledgement missing")
				}
				failed := p
				failed.ID = 82
				if opened, err := provider.Open(ctx, failed, password+"-wrong"); err == nil {
					opened.Close()
					t.Fatal("native network wrong password accepted")
				}
				if sessionID() != id {
					t.Fatal("native cancellation/authentication replaced original physical session")
				}
				lease.Release()
				second.Release()
				observer.Release()
				if err := original.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err := lease.Execute(ctx, "PUBLIC", `SELECT 1`); err == nil {
					t.Fatal("disconnected native lease implicitly recovered")
				}
				if mode == "ssl" {
					s := original.(*Session)
					host, port := s.transport.endpoint()
					if host == "" || port == 0 {
						t.Fatal("original TLS transport identity lost")
					}
				}
				recovered, err := provider.Open(ctx, p, password)
				if err != nil {
					t.Fatal("explicit native network recovery unavailable", err)
				}
				defer recovered.Close()
				newData := contract.ComposeData(recovered)
				observed, err := newData.Loader(ctx, p.ID, "PUBLIC", object, 0, 10)
				if err != nil || observed == nil || len(observed.Rows) != 1 || observed.Rows[0][1].Value.Text != "native confirmed network" {
					t.Fatal("explicit recovery lost original native committed values", err)
				}
			})
		}
	}
}

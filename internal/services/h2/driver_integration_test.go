package h2

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/driverstore"
)

func TestH2NativeAutomaticAndCustomDriverOwnership(t *testing.T) {
	if os.Getenv("NYADB_H2_DRIVER_IT") != "1" {
		t.Skip("requires owned H2 automatic/custom driver fixture")
	}
	provider, options, password, directory := managedH2Fixture(t)
	var fixture struct{ DriverJARs map[string]string }
	raw, err := os.ReadFile(os.Getenv("NYADB_MANAGED_H2_FIXTURE_FILE"))
	if err != nil || json.Unmarshal(raw, &fixture) != nil {
		t.Fatal("original driver fixture unavailable")
	}
	for _, mode := range []string{"auto", "custom"} {
		for _, profile := range []string{"h2-v1", "h2-v2", "h2-v3"} {
			t.Run(mode+"/"+profile, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
				defer cancel()
				a := options
				a.DriverProfile, a.AutoServer, a.ReadOnly = "h2-"+mode, false, true
				if mode == "custom" {
					source := filepath.Join(directory, "custom-imports", profile)
					if err := os.MkdirAll(source, 0700); err != nil {
						t.Fatal(err)
					}
					original, err := os.ReadFile(fixture.DriverJARs[profile])
					if err != nil {
						t.Fatal(err)
					}
					jar := filepath.Join(source, "h2.jar")
					if err := os.WriteFile(jar, original, 0600); err != nil {
						t.Fatal(err)
					}
					paths := []string{jar}
					if profile == "h2-v2" {
						entry := filepath.Join(source, "entry.jar")
						helper := filepath.Join(source, "helper.jar")
						writeNativeDriverEntry(t, entry, "Manifest-Version: 1.0\r\nClass-Path: h2.jar\r\n\r\n")
						writeNativeDriverEntry(t, helper, "Manifest-Version: 1.0\r\n\r\n")
						paths = []string{helper, entry}
					}
					imported, err := provider.Store.ImportJDBC(ctx, driverstore.Product{Key: "h2", Runtime: "java", MainClass: "com.dbx.agent.h2.H2Agent"}, connections.JDBCImport{BaseVersion: options.DriverVersion, BaseSHA256: options.DriverSHA256, Profile: "h2-custom", Class: "org.h2.Driver", Paths: paths})
					if err != nil {
						t.Fatal("native custom graph import failed", err)
					}
					a.DriverVersion, a.DriverSHA256 = imported.Version, imported.SHA256
					// Later physical sessions/recovery must use retained bytes even
					// after every mutable caller-side graph file has disappeared.
					if err := os.RemoveAll(source); err != nil {
						t.Fatal(err)
					}
				}
				p := connections.Profile{ID: 901, Kind: connections.H2, Name: "original driver " + mode + " " + profile, Username: "sa", Database: filepath.Join(directory, "provider-"+mode, profile), H2Options: a}
				file := p.Database + ".mv.db"
				before, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				opened, err := provider.Open(ctx, p, password)
				if err != nil {
					t.Fatal("native original driver read-only session unavailable", err)
				}
				s := opened.(*Session)
				var captured struct {
					DriverProfile string   `json:"driver_profile"`
					Paths         []string `json:"jdbc_driver_paths"`
				}
				if json.Unmarshal(s.params, &captured) != nil {
					t.Fatal("original request snapshot unavailable")
				}
				if mode == "auto" && (captured.DriverProfile != profile || s.profile.H2Options.DriverProfile != "h2-auto") {
					t.Fatal("automatic selection changed original profile or failed to freeze actual driver")
				}
				if mode == "custom" {
					if captured.DriverProfile != "h2-custom" || len(captured.Paths) == 0 {
						t.Fatal("native custom driver identity lost")
					}
					for _, path := range captured.Paths {
						if !strings.HasPrefix(path, filepath.Join(directory, "provider-store")+string(filepath.Separator)) {
							t.Fatal("mutable source path reached native executor")
						}
					}
				}
				data := contract.ComposeData(s)
				page, err := data.Loader(ctx, p.ID, "PUBLIC", "driver_records", 0, 10)
				if err != nil || page == nil || len(page.Rows) != 1 || page.Rows[0][0].Value.Text != "9007199254740993" || data.Saver != nil {
					t.Fatal("read-only native typed row or write capability lost", err)
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				after, err := os.ReadFile(file)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatal("read-only automatic/custom open changed original file", err)
				}
				p.H2Options.ReadOnly, p.H2Options.AutoServer = false, true
				testNativeDriverLifecycle(t, ctx, provider, p, password)
			})
		}
	}
	t.Run("page-store", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		a := options
		a.DriverProfile, a.AutoServer, a.ReadOnly = "h2-auto", false, true
		p := connections.Profile{ID: 902, Kind: connections.H2, Name: "original PageStore", Username: "sa", Database: filepath.Join(directory, "provider-auto", "page-store"), H2Options: a}
		before, err := os.ReadFile(p.Database + ".h2.db")
		if err != nil {
			t.Fatal(err)
		}
		opened, err := provider.Open(ctx, p, password)
		if err != nil {
			t.Fatal(err)
		}
		s := opened.(*Session)
		var request struct {
			Profile string `json:"driver_profile"`
		}
		json.Unmarshal(s.params, &request)
		if request.Profile != "h2-v1" {
			t.Fatal("original PageStore selected another driver")
		}
		page, err := contract.ComposeData(s).Loader(ctx, p.ID, "PUBLIC", "driver_records", 0, 1)
		if err != nil || page.Rows[0][0].Value.Text != "9007199254740993" {
			t.Fatal("native PageStore data lost", err)
		}
		s.Close()
		after, err := os.ReadFile(p.Database + ".h2.db")
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("PageStore read changed original bytes")
		}
	})
	t.Run("new-file", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		a := options
		a.DriverProfile, a.AutoServer, a.RequireExisting = "h2-auto", false, false
		p := connections.Profile{ID: 905, Kind: connections.H2, Name: "new automatic file", Username: "sa", Database: filepath.Join(directory, "provider-auto", "new-file"), H2Options: a}
		if _, err := os.Stat(p.Database + ".mv.db"); !os.IsNotExist(err) {
			t.Fatal("new automatic fixture already exists", err)
		}
		opened, err := provider.Open(ctx, p, password)
		if err != nil {
			t.Fatal("automatic new-file creation failed", err)
		}
		s := opened.(*Session)
		var request struct {
			Profile string `json:"driver_profile"`
		}
		if json.Unmarshal(s.params, &request) != nil || request.Profile != "h2-v3" {
			s.Close()
			t.Fatal("new automatic file did not freeze original V3 driver")
		}
		lease, err := s.ReserveQuery(1)
		if err != nil {
			s.Close()
			t.Fatal(err)
		}
		_, err = lease.Execute(ctx, "PUBLIC", `CREATE TABLE "new_records"("id" BIGINT PRIMARY KEY);INSERT INTO "new_records" VALUES(9007199254740993)`)
		lease.Release()
		closeErr := s.Close()
		if err != nil || closeErr != nil {
			t.Fatal("new automatic file write failed", err, closeErr)
		}
		before, err := os.ReadFile(p.Database + ".mv.db")
		if err != nil {
			t.Fatal("new automatic database file missing", err)
		}
		p.H2Options.RequireExisting, p.H2Options.ReadOnly = true, true
		reopened, err := provider.Open(ctx, p, password)
		if err != nil {
			t.Fatal("new automatic file could not be reopened", err)
		}
		page, err := contract.ComposeData(reopened).Loader(ctx, p.ID, "PUBLIC", "new_records", 0, 10)
		closeErr = reopened.Close()
		after, readErr := os.ReadFile(p.Database + ".mv.db")
		if err != nil || closeErr != nil || readErr != nil || page == nil || len(page.Rows) != 1 || page.Rows[0][0].Value.Text != "9007199254740993" || !bytes.Equal(before, after) {
			t.Fatal("new automatic file lost persisted typed row or changed during read-only reopen", err, closeErr, readErr)
		}
	})
	t.Run("auto-rejections", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
		defer cancel()
		a := options
		a.DriverProfile, a.Mode, a.RequireExisting, a.AutoServer = "h2-auto", "memory", false, false
		p := connections.Profile{ID: 903, Kind: connections.H2, Name: "original automatic memory", Database: "driver_original", Username: "sa", H2Options: a}
		opened, err := provider.Open(ctx, p, password)
		if err != nil {
			t.Fatal(err)
		}
		defer opened.Close()
		s := opened.(*Session)
		lease, err := s.ReserveQuery(901)
		if err != nil {
			t.Fatal(err)
		}
		defer lease.Release()
		original, err := lease.Execute(ctx, "PUBLIC", "SELECT SESSION_ID()")
		if err != nil {
			t.Fatal(err)
		}
		invalid := filepath.Join(directory, "provider-auto", "invalid")
		if err := os.WriteFile(invalid+".mv.db", bytes.Repeat([]byte("invalid!"), 1024), 0600); err != nil {
			t.Fatal(err)
		}
		mixed := filepath.Join(directory, "provider-auto", "ambiguous")
		for suffix, source := range map[string]string{".h2.db": "page-store.h2.db", ".mv.db": "h2-v3.mv.db"} {
			raw, err := os.ReadFile(filepath.Join(directory, "provider-auto", source))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(mixed+suffix, raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
		for _, base := range []string{filepath.Join(directory, "provider-auto", "missing"), invalid, mixed, filepath.Join(directory, "provider-auto", "encrypted")} {
			before := map[string][]byte{}
			for _, suffix := range []string{".mv.db", ".h2.db"} {
				raw, err := os.ReadFile(base + suffix)
				if err == nil {
					before[suffix] = raw
				}
			}
			bad := p
			bad.ID = 904
			bad.Database = base
			bad.H2Options.Mode, bad.H2Options.RequireExisting = "file", true
			if raw, err := provider.Open(ctx, bad, password); err == nil {
				raw.Close()
				t.Fatal("missing/invalid/ambiguous/encrypted automatic file admitted")
			}
			for _, suffix := range []string{".mv.db", ".h2.db"} {
				raw, err := os.ReadFile(base + suffix)
				old, existed := before[suffix]
				if existed && (err != nil || !bytes.Equal(raw, old)) || !existed && !os.IsNotExist(err) {
					t.Fatal("rejected automatic file was changed or created")
				}
			}
		}
		imported, err := provider.Store.ImportJDBC(ctx, driverstore.Product{Key: "h2", Runtime: "java", MainClass: "com.dbx.agent.h2.H2Agent"}, connections.JDBCImport{BaseVersion: options.DriverVersion, BaseSHA256: options.DriverSHA256, Profile: "h2-custom", Class: "fixture.missing.JDBCDriver", Paths: []string{fixture.DriverJARs["h2-v3"]}})
		if err != nil {
			t.Fatal("original custom class rejection fixture unavailable", err)
		}
		bad := p
		bad.ID = 906
		bad.H2Options.DriverProfile, bad.H2Options.DriverVersion, bad.H2Options.DriverSHA256 = "h2-custom", imported.Version, imported.SHA256
		if opened, err := provider.Open(ctx, bad, password); err == nil {
			opened.Close()
			t.Fatal("missing original custom JDBC class admitted")
		}
		after, err := lease.Execute(ctx, "PUBLIC", "SELECT SESSION_ID()")
		if err != nil || after.Sets[0].Rows[0][0].Text != original.Sets[0].Rows[0][0].Text {
			t.Fatal("rejected opens replaced original automatic physical session", err)
		}
	})
}

func writeNativeDriverEntry(t *testing.T, path, manifest string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	entry, err := archive.Create("META-INF/MANIFEST.MF")
	if err != nil {
		t.Fatal(err)
	}
	entry.Write([]byte(manifest))
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func testNativeDriverLifecycle(t *testing.T, ctx context.Context, provider Provider, p connections.Profile, password string) {
	t.Helper()
	opened, err := provider.Open(ctx, p, password)
	if err != nil {
		t.Fatal("native original writable driver session unavailable", err)
	}
	s := opened.(*Session)
	defer s.Close()
	lease, err := s.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	id := func() string {
		t.Helper()
		q, err := lease.Execute(ctx, "PUBLIC", "SELECT SESSION_ID()")
		if err != nil {
			t.Fatal(err)
		}
		return q.Sets[0].Rows[0][0].Text
	}
	original := id()
	names, err := s.Namespaces(ctx)
	if err != nil || len(names) == 0 {
		t.Fatal("original native namespace lost", err)
	}
	object := s.Namespace("PUBLIC").Object("table", "driver_records")
	structure, err := s.TableMetadata(ctx, object)
	if err != nil || structure.Definition == "" || len(structure.Columns) != 2 {
		t.Fatal("original native structure lost", err)
	}
	data := contract.ComposeData(s)
	page, err := data.Loader(ctx, p.ID, "PUBLIC", object.Name, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	note := "confirmed original driver"
	receipt, err := data.Saver(ctx, p.ID, "PUBLIC", object.Name, []contract.DataEdit{{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{"note": {Text: note}}}})
	if err != nil || receipt == nil || !receipt.CommitConfirmed || receipt.NativeJSON == "" {
		t.Fatal("native driver save lost original receipt", err)
	}
	holder, err := s.ReserveQuery(2)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()
	observer, err := s.ReserveQuery(3)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Release()
	page, err = data.Loader(ctx, p.ID, "PUBLIC", object.Name, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = holder.Execute(ctx, "PUBLIC", `BEGIN;UPDATE "driver_records" SET "note"="note" WHERE "id"=9007199254740993`); err != nil {
		t.Fatal(err)
	}
	blocked, cancel := context.WithCancel(ctx)
	type completion struct {
		result *contract.DataEditResult
		err    error
	}
	done := make(chan completion, 1)
	go func() {
		result, err := data.Saver(blocked, p.ID, "PUBLIC", object.Name, []contract.DataEdit{{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{"note": {Text: "cancelled original driver"}}}})
		done <- completion{result, err}
	}()
	observed := false
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		q, err := observer.Execute(ctx, "PUBLIC", `SELECT COUNT(*) FROM INFORMATION_SCHEMA.SESSIONS WHERE BLOCKER_ID IS NOT NULL`)
		if err != nil {
			cancel()
			holder.Execute(ctx, "PUBLIC", "ROLLBACK")
			<-done
			t.Fatal(err)
		}
		if q.Sets[0].Rows[0][0].Text != "0" {
			observed = true
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	cancel()
	if _, err = holder.Execute(ctx, "PUBLIC", "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	select {
	case complete := <-done:
		if !observed || !errors.Is(complete.err, context.Canceled) || complete.result == nil || complete.result.CommitConfirmed {
			t.Fatal("original native blocked saver did not acknowledge cancellation", complete.err)
		}
		var native transactionReceipt
		if json.Unmarshal([]byte(complete.result.NativeJSON), &native) != nil || len(native.Statements) != 1 || !native.Statements[0].Dispatched || !native.Statements[0].ExecutorAcknowledged && !native.Statements[0].ProcessExitConfirmed {
			t.Fatal("native driver cancellation lost original executor receipt")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("native driver cancellation acknowledgement missing")
	}
	wrong := p
	wrong.ID++
	if opened, err := provider.Open(ctx, wrong, password+"-wrong"); err == nil {
		opened.Close()
		t.Fatal("native wrong password admitted")
	}
	if id() != original {
		t.Fatal("cancel/authentication changed original native physical session")
	}
	lease.Release()
	holder.Release()
	observer.Release()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := lease.Execute(ctx, "PUBLIC", "SELECT 1"); err == nil {
		t.Fatal("closed original driver lease recovered implicitly")
	}
	recovered, err := provider.Open(ctx, p, password)
	if err != nil {
		t.Fatal("explicit original driver recovery unavailable", err)
	}
	defer recovered.Close()
	page, err = contract.ComposeData(recovered).Loader(ctx, p.ID, "PUBLIC", object.Name, 0, 10)
	if err != nil || len(page.Rows) != 1 || page.Rows[0][1].Value.Text != note {
		t.Fatal("driver recovery replayed cancelled save or lost committed original data", err)
	}
}

package manticore

import (
	"context"
	"crypto/rand"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func authNativeConfig(t *testing.T) (connections.ManticoreConfig, string) {
	t.Helper()
	if os.Getenv("NYADB_MANTICORE_AUTH_IT") != "1" {
		t.Skip("requires owned authentication-enabled Manticore fixture")
	}
	host, port, err := net.SplitHostPort(os.Getenv("NYADB_MANTICORE_ENDPOINT"))
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	secret := os.Getenv("NYADB_MANTICORE_AUTH_PASSWORD")
	if secret == "" {
		t.Fatal("missing private fixture credential")
	}
	return connections.ManticoreConfig{Host: host, Port: uint16(n), Username: "nyadb_admin", QueryTimeoutSeconds: 20, SQLMaxConnections: 2}, secret
}

func authSecret(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return "Aa7!" + hex.EncodeToString(b[:])
}

func authOpen(t *testing.T, cfg connections.ManticoreConfig, secret string) *Session {
	t.Helper()
	s, err := Open(context.Background(), 69, cfg, secret)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if !strings.HasPrefix(s.Version(), os.Getenv("NYADB_MANTICORE_VERSION")+" ") {
		t.Fatal("wrong native fixture version")
	}
	return s
}

func authSetup(t *testing.T, cfg connections.ManticoreConfig, secret string) func(string) {
	t.Helper()
	c, err := mysqldriver.NewConnector(connectionConfig(cfg, secret))
	if err != nil {
		t.Fatal("fixture connector failed")
	}
	conn, err := c.Connect(context.Background())
	if err != nil {
		t.Fatal("fixture authentication failed")
	}
	t.Cleanup(func() { conn.Close() })
	return func(sql string) {
		t.Helper()
		// Never pass credential-bearing statements or returned bearer tokens to
		// test logs, SQL history or product receipts.
		if _, err := conn.(driver.ExecerContext).ExecContext(context.Background(), sql, nil); err != nil {
			t.Fatal("private fixture provisioning rejected")
		}
	}
}

func authReader(t *testing.T) (*Session, *Session, connections.ManticoreConfig, string, func(string), string) {
	t.Helper()
	cfg, secret := authNativeConfig(t)
	admin := authOpen(t, cfg, secret)
	setup := authSetup(t, cfg, secret)
	name := fmt.Sprintf("nyadb_auth83_%d", time.Now().UnixNano())
	q := nativeLease(t, admin, 1)
	runNative(t, q, "CREATE TABLE "+name+" (title text, qty integer)")
	runNative(t, q, "INSERT INTO "+name+" (id,title,qty) VALUES (1,'original',7)")
	user, password := name+"_reader", authSecret(t)
	setup("CREATE USER '" + user + "' IDENTIFIED BY '" + password + "'")
	setup("GRANT read ON * TO '" + user + "'")
	t.Cleanup(func() { setup("DROP USER '" + user + "'"); setup("DROP TABLE IF EXISTS " + name) })
	cfg.Username = user
	reader := authOpen(t, cfg, password)
	return admin, reader, cfg, password, setup, name
}

func TestNativeAuthCredentialsAndRestrictedMetadata(t *testing.T) {
	cfg, secret := authNativeConfig(t)
	for _, mode := range []string{"wrong", "empty", "missing", "case"} {
		t.Run(mode, func(t *testing.T) {
			c, password := cfg, authSecret(t)
			switch mode {
			case "empty":
				password = ""
			case "missing":
				c.Username = "nyadb_missing"
				password = secret
			case "case":
				c.Username = "Nyadb_Admin"
				password = secret
			}
			s, err := Open(context.Background(), 69, c, password)
			if s != nil || err == nil || !strings.Contains(err.Error(), "[1045]") || strings.Contains(err.Error(), secret) || password != "" && strings.Contains(err.Error(), password) {
				t.Fatal("original authentication rejection was lost or leaked")
			}
			objectEvidence(t, "auth-"+mode+".json", map[string]any{"opened": false, "error": err.Error()})
		})
	}
	_, reader, _, _, _, table := authReader(t)
	namespaces, err := reader.Namespaces(context.Background())
	if err != nil || len(namespaces) != 1 || namespaces[0].Ref != reader.Namespace("Manticore") {
		t.Fatal(namespaces, err)
	}
	objects, err := reader.Browse(context.Background(), reader.Namespace("Manticore"))
	var partial *contract.PartialMetadataError
	if !errors.As(err, &partial) || partial.Observation.Command != "SHOW PLUGINS" || partial.Observation.ErrorCode != 1045 || partial.Observation.SQLState != "42000" {
		t.Fatal("denied UDF list became successful empty metadata", err)
	}
	found := false
	for _, o := range objects {
		if o.Name == table {
			found = true
		}
		if o.Kind != "TABLE" {
			t.Fatal("unread functions fabricated", o)
		}
	}
	if !found {
		t.Fatal("independently read table lost")
	}
	structure, err := reader.TableMetadata(context.Background(), reader.Namespace("Manticore").Object("TABLE", table))
	if err != nil || structure == nil || len(structure.Columns) != 3 || structure.Definition == "" {
		t.Fatal(structure, err)
	}
	objectEvidence(t, "auth-restricted-metadata.json", map[string]any{"namespaces": namespaces, "objects": objects, "partial": partial.Observation, "structure": structure})
}

func TestNativeAuthOriginalSQLDataSchemaDenialsAndPermissionChanges(t *testing.T) {
	admin, reader, cfg, _, setup, table := authReader(t)
	q := nativeLease(t, reader, 2)
	denied := func(source string) contract.SQLResult {
		t.Helper()
		r, err := q.Execute(context.Background(), "Manticore", source)
		var receipt nativeSQLReceipt
		if e := json.Unmarshal([]byte(r.NativeJSON), &receipt); e != nil {
			t.Fatal(e)
		}
		if err == nil || errors.Is(err, contract.ErrSQLSessionLost) || len(receipt.Statements) != 1 || receipt.Statements[0].State != "failed" || !receipt.Statements[0].Acknowledged || receipt.Statements[0].ErrorCode != 1045 || receipt.Statements[0].SQLState != "42000" {
			t.Fatal("native permission rejection changed", r, err)
		}
		return r
	}
	original := denied("UPDATE " + table + " SET qty=8 WHERE id=1")
	if got := runNative(t, q, "SELECT qty FROM "+table); fmt.Sprint(got.Rows) != "[[7]]" {
		t.Fatal(got)
	}
	_, runtime := nativeData(t, reader, table)
	page, err := runtime.Loader(context.Background(), 69, "Manticore", table, 0, 10)
	if err != nil || len(page.RowSnapshots) != 1 {
		t.Fatal(page, err)
	}
	plan := previewNative(t, runtime, table, contract.DataEdit{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{"qty": {Kind: contract.Integer, Text: "8"}}})
	out, err := runtime.SubmitEdits(context.Background(), 69, "Manticore", table, plan)
	if err == nil || out == nil || out.Sequential == nil || len(out.Sequential.Statements) != 1 || out.Sequential.Statements[0].State != contract.DataWriteFailed {
		t.Fatal(out, err)
	}
	objectEvidence(t, "auth-data-denied.json", out)
	setup("GRANT write ON '" + table + "' TO '" + cfg.Username + "'")
	if _, err = runtime.SubmitEdits(context.Background(), 69, "Manticore", table, plan); err == nil {
		t.Fatal("consumed denied data plan replayed after grant")
	}
	if got := runNative(t, q, "SELECT qty FROM "+table); fmt.Sprint(got.Rows) != "[[7]]" {
		t.Fatal("grant replayed original write", got)
	}
	runNative(t, q, "UPDATE "+table+" SET qty=8 WHERE id=1")
	setup("REVOKE write ON '" + table + "' FROM '" + cfg.Username + "'")
	revoked := denied("UPDATE " + table + " SET qty=9 WHERE id=1")
	newTable := table + "_new"
	d, target := nativeSchema(t, reader, newTable, "auth-denied-create")
	snapshot := schemaOriginal(t, d, target)
	request := schemaCreateRequest(snapshot, false)
	created := d.Operations().Apply(context.Background(), request)
	var native nativeWriteReceipt
	if created.Receipt == nil {
		t.Fatal(created)
	}
	if err = json.Unmarshal([]byte(created.Receipt.NativeJSON), &native); err != nil || created.Error == nil || created.State != "not_executed" || !native.Dispatched || !native.Acknowledged || native.ErrorCode != 1045 {
		t.Fatal(created, err)
	}
	setup("GRANT schema ON * TO '" + cfg.Username + "'")
	if again := d.Operations().Apply(context.Background(), request); !errors.Is(again.Error, schemaddl.ErrConflict) || again.Receipt != nil {
		t.Fatal("consumed denied schema proof replayed", again)
	}
	objects, err := reader.Browse(context.Background(), reader.Namespace("Manticore"))
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objects {
		if o.Name == newTable {
			t.Fatal("permission grant created table")
		}
	}
	control := nativeLease(t, admin, 3)
	if got := runNative(t, control, "SELECT qty FROM "+table); fmt.Sprint(got.Rows) != "[[8]]" {
		t.Fatal(got)
	}
	objectEvidence(t, "auth-original-denials.json", map[string]any{"sql": original, "revoked": revoked, "schema": created.Receipt, "after_grant_objects": objects})
}

func TestNativeAuthPasswordRotationOriginalLeasesAndExplicitRecovery(t *testing.T) {
	_, reader, cfg, password, setup, table := authReader(t)
	q := nativeLease(t, reader, 20)
	runNative(t, q, "SELECT qty FROM "+table)
	newPassword := authSecret(t)
	setup("SET PASSWORD '" + newPassword + "' FOR '" + cfg.Username + "'")
	// The server permits the original authenticated physical connection to
	// remain usable. Fresh physical owners must still authenticate separately.
	retained := runNative(t, q, "SELECT qty FROM "+table)
	if _, err := reader.Namespaces(context.Background()); err == nil || !strings.Contains(err.Error(), "[1045]") {
		t.Fatal(err)
	}
	other := nativeLease(t, reader, 21)
	source := "UPDATE " + table + " SET qty=99 WHERE id=1"
	r, err := other.Execute(context.Background(), "Manticore", source)
	var receipt nativeSQLReceipt
	if e := json.Unmarshal([]byte(r.NativeJSON), &receipt); e != nil {
		t.Fatal(e)
	}
	if err == nil || len(receipt.Statements) != 1 || receipt.Statements[0].Dispatched || receipt.Statements[0].State != "not_executed" {
		t.Fatal("failed reauthentication dispatched SQL", r, err)
	}
	if other.(*queryLease).slot || other.(*queryLease).conn != nil {
		t.Fatal("failed handshake retained capacity or channel")
	}
	fresh := authOpen(t, cfg, newPassword)
	recovered := nativeLease(t, fresh, 22)
	if err = recovered.Preconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	readback := runNative(t, recovered, "SELECT qty FROM "+table)
	if fmt.Sprint(readback.Rows) != "[[7]]" {
		t.Fatal("explicit credential recovery replayed SQL", readback)
	}
	objectEvidence(t, "auth-password-rotation.json", map[string]any{"retained": retained, "not_sent": r, "recovered": readback})
	if err = reader.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = q.Execute(context.Background(), "Manticore", "SELECT qty FROM "+table); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed original owner recovered implicitly", err)
	}
	if reader.profileSecret() != "" || password == newPassword {
		t.Fatal("closed credentials retained")
	}
}

func TestNativeAuthCanceledOpenNeverPublishesAuthenticatedSession(t *testing.T) {
	cfg, password := authNativeConfig(t)
	gate := newPacketGate(t, net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port))))
	host, port, _ := net.SplitHostPort(gate.listener.Addr().String())
	n, _ := strconv.Atoi(port)
	cfg.Host, cfg.Port = host, uint16(n)
	observed := gate.arm("SHOW DATABASES", "hold")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		s   *Session
		err error
	}
	done := make(chan result, 1)
	go func() { s, err := Open(ctx, 69, cfg, password); done <- result{s, err} }()
	select {
	case <-observed:
	case <-time.After(10 * time.Second):
		t.Fatal("real authenticated probe did not arrive")
	}
	cancel()
	select {
	case out := <-done:
		if out.s != nil || out.err == nil {
			t.Fatal("canceled authenticated session published")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("original canceled executor did not settle")
	}
	if gate.calls.Load() != 1 {
		t.Fatal("authenticated probe retried")
	}
	objectEvidence(t, "auth-canceled-open.json", map[string]any{"published": false, "original_probe_calls": gate.calls.Load()})
}

func TestNativeAuthMetadataLossCannotBecomePartialSuccess(t *testing.T) {
	_, _, cfg, password, _, _ := authReader(t)
	gate := newPacketGate(t, net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port))))
	host, port, _ := net.SplitHostPort(gate.listener.Addr().String())
	n, _ := strconv.Atoi(port)
	cfg.Host, cfg.Port = host, uint16(n)
	bound := authOpen(t, cfg, password)
	observed := gate.arm("SHOW PLUGINS", "drop")
	objects, err := bound.Browse(context.Background(), bound.Namespace("Manticore"))
	var partial *contract.PartialMetadataError
	if err == nil || errors.As(err, &partial) || objects != nil {
		t.Fatal("lost response published partial success", objects, err)
	}
	select {
	case packet := <-observed:
		if len(packet) < 7 || packet[4] != 0xff {
			t.Fatal("expected original native error packet")
		}
	case <-time.After(time.Second):
		t.Fatal("original plugin response absent")
	}
	disarm(gate)
	observed = gate.arm("SELECT DATABASE()", "hold")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		s   *Session
		err error
	}
	done := make(chan result, 1)
	go func() { s, err := Open(ctx, 69, cfg, password); done <- result{s, err} }()
	select {
	case <-observed:
	case <-time.After(10 * time.Second):
		t.Fatal("original fallback response absent")
	}
	cancel()
	select {
	case out := <-done:
		if out.s != nil || out.err == nil {
			t.Fatal("canceled fallback published a catalog")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("canceled original fallback did not settle")
	}
	objectEvidence(t, "auth-metadata-loss.json", map[string]any{"partial_success": false, "published": false, "original_calls": gate.calls.Load()})
}

func TestNativeAnonymousCredentialsDoNotProveAuthentication(t *testing.T) {
	cfg := nativeConfig(t)
	cfg.Username = "nyadb_unprovisioned_user"
	for _, secret := range []string{authSecret(t), ""} {
		s := authOpen(t, cfg, secret)
		q := nativeLease(t, s, 83)
		if got := runNative(t, q, "SELECT 1"); fmt.Sprint(got.Rows) != "[[1]]" {
			t.Fatal(got)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
	objectEvidence(t, "auth-anonymous-default.json", map[string]any{"credentials_ignored": true, "authentication_enforced": false, "version": os.Getenv("NYADB_MANTICORE_VERSION")})
}

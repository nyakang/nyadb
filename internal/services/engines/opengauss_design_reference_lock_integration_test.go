package engines

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

// This is native lock/permission evidence only. It does not authorize foreign
// key execution in the designer or substitute for reference catalog/callback
// verification. Administration is limited to the explicitly owned fixture.
func gaussDesignFixtureRoleSQL(t *testing.T, sql string) {
	t.Helper()
	container := os.Getenv("NYADB_OPENGAUSS_CONTAINER")
	if !strings.HasPrefix(container, "nyadb-opengauss-it-") {
		t.Fatal("reference permission test requires the owned native fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "inspect", "--format", `{{index .Config.Labels "nyadb.fixture"}}`, container)
	if output, err := cmd.Output(); err != nil || strings.TrimSpace(string(output)) != "opengauss" {
		t.Fatal("refusing administration outside the owned native fixture", err)
	}
	cmd = exec.CommandContext(ctx, "docker", "exec", "-i", "--user", "omm", "-e", "LD_LIBRARY_PATH=/usr/local/opengauss/lib", container, "/usr/local/opengauss/bin/gsql", "-d", "nyadb_opengauss_it", "-v", "ON_ERROR_STOP=1")
	cmd.Stdin = strings.NewReader(sql)
	if output, err := cmd.CombinedOutput(); err != nil {
		message := string(output)
		if password := os.Getenv("NYADB_OPENGAUSS_PASSWORD"); password != "" {
			message = strings.ReplaceAll(message, strings.ReplaceAll(password, "'", "''"), "[redacted]")
			message = strings.ReplaceAll(message, password, "[redacted]")
		}
		t.Fatalf("owned reference fixture role administration: %v: %s", err, message)
	}
}

func TestOpenGaussDesignNativeReferenceAnchorLocksAndReferencesOnlyPermissionFixture(t *testing.T) {
	s, db, target := openGaussTableFixture(t)
	if os.Getenv("NYADB_OPENGAUSS_CONTAINER") == "" {
		t.Skip("requires the explicitly owned fixture for a disposable restricted role")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	role := fmt.Sprintf("nyadb_design_ref_%d", time.Now().UnixNano())
	// Authenticate the restricted role on its own physical TLS connection;
	// native SET ROLE requires another password-bearing SQL statement.
	password := os.Getenv("NYADB_OPENGAUSS_PASSWORD")
	if password == "" {
		t.Fatal("restricted role requires the private fixture credential")
	}
	gaussDesignFixtureRoleSQL(t, "BEGIN; CREATE ROLE "+quote(role)+" WITH LOGIN PASSWORD '"+strings.ReplaceAll(password, "'", "''")+"'; COMMIT;")
	t.Cleanup(func() { gaussDesignFixtureRoleSQL(t, "DROP OWNED BY "+quote(role)+"; DROP ROLE "+quote(role)+";") })
	parent := quote(target.Object.Schema) + ".other"
	anchor := quote(target.Object.Schema) + ".reference_anchor"
	sequence := quote(target.Object.Schema) + ".reference_parent_seq"
	if _, err := db.ExecContext(ctx, "ALTER TABLE "+parent+" ADD PRIMARY KEY(id); CREATE SEQUENCE "+sequence+" OWNED BY "+parent+".id; ALTER TABLE "+parent+" ALTER COLUMN id SET DEFAULT nextval('"+sequence+"'::regclass); GRANT USAGE,CREATE ON SCHEMA "+quote(target.Object.Schema)+" TO "+quote(role)+"; GRANT REFERENCES(id) ON "+parent+" TO "+quote(role)); err != nil {
		t.Fatal(err)
	}
	profile := s.backupProfile
	profile.Username = role
	restricted, err := (openGaussDialect{}).open(profile, password, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer restricted.Close()
	c, err := restricted.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.ExecContext(ctx, "BEGIN; SET LOCAL search_path=pg_catalog; SET LOCAL standard_conforming_strings=on; CREATE TABLE "+quote(target.Object.Schema)+".owned_child(id bigint); SAVEPOINT permission_test"); err != nil {
		t.Fatal(err)
	}
	defer rollbackDesign(c)
	parentTarget := target
	parentTarget.Object.Name = "other"
	before, err := openGaussReadDesignReference(ctx, c, parentTarget)
	if err != nil || before.CanManage || before.RoleOID == 0 {
		t.Fatal("reference metadata needs parent ownership or lost original role", err)
	}
	if _, err = c.ExecContext(ctx, "SAVEPOINT management_boundary"); err != nil {
		t.Fatal(err)
	}
	if _, err = openGaussReadTable(ctx, c, parentTarget); err == nil {
		t.Fatal("reference metadata relaxed the public management owner boundary")
	}
	if _, err = c.ExecContext(ctx, "ROLLBACK TO SAVEPOINT management_boundary; RELEASE SAVEPOINT management_boundary"); err != nil {
		t.Fatal(err)
	}
	childTarget := target
	childTarget.Object.Name = "owned_child"
	child, err := openGaussReadDesign(ctx, c, childTarget)
	if err != nil {
		t.Fatal("restricted actor's original table", err)
	}
	desired := child.Schema.Table.Clone()
	desired.Clauses = append(desired.Clauses,
		schemaddl.Clause{ID: "new:parent", Kind: "column", Name: "parent_id", SQL: "bigint"},
		schemaddl.Clause{ID: "new:fk", Kind: "constraint", Name: "parent_fk", SQL: "CONSTRAINT parent_fk FOREIGN KEY(parent_id) REFERENCES " + parent + "(id)"},
	)
	baseline, err := schemaddl.CaptureOpenGaussReferenceBaseline(child.Schema, desired, before)
	if err != nil {
		t.Fatal("independent restricted reference baseline", err)
	}
	unchanged, err := openGaussReadDesignReference(ctx, c, parentTarget)
	if err != nil || schemaddl.VerifyOpenGaussReferenceUnchanged(baseline, child.Schema, desired, unchanged) != nil {
		t.Fatal("reference baseline was not repeatable", err)
	}
	// Recompute a valid fingerprint after adding an unknown exact property.
	// It must remain a conflict, and constructing the proof must not alias its
	// caller's raw catalog bytes or permit use by another desired declaration.
	beforeSignature := schemaddl.Signature(before)
	corrupt := unchanged.Clone()
	for i, entry := range corrupt.Entries {
		if entry.Kind == "relation" && entry.OID == corrupt.RelationOID {
			var raw map[string]json.RawMessage
			if json.Unmarshal(entry.Data, &raw) != nil {
				t.Fatal("native reference raw record")
			}
			raw["unknown_reference_property"] = json.RawMessage(`90071992547409931234567890.12345678901234567890`)
			corrupt.Entries[i].Data, err = json.Marshal(raw)
			if err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if _, err = corrupt.Fingerprint(); err != nil || !errors.Is(schemaddl.VerifyOpenGaussReferenceUnchanged(baseline, child.Schema, desired, corrupt), schemaddl.ErrConflict) || schemaddl.Signature(before) != beforeSignature {
		t.Fatal("unknown reference property accepted or independent snapshot changed", err)
	}
	foreign := desired.Clone()
	foreign.Name = "different_request"
	if !errors.Is(schemaddl.VerifyOpenGaussReferenceUnchanged(baseline, child.Schema, foreign, unchanged), schemaddl.ErrConflict) {
		t.Fatal("independent reference proof authorized another request")
	}
	if _, err = c.ExecContext(ctx, "SELECT last_value FROM "+sequence); err == nil {
		t.Fatal("REFERENCES-only actor unexpectedly gained source sequence SELECT")
	}
	if _, err = c.ExecContext(ctx, "ROLLBACK TO SAVEPOINT permission_test"); err != nil {
		t.Fatal(err)
	}
	if _, err = c.ExecContext(ctx, "LOCK TABLE "+parent+" IN SHARE ROW EXCLUSIVE MODE"); err == nil {
		t.Fatal("ordinary LOCK unexpectedly accepted REFERENCES without SELECT")
	}
	if _, err = c.ExecContext(ctx, "ROLLBACK TO SAVEPOINT permission_test; RELEASE SAVEPOINT permission_test; CREATE TABLE "+anchor+"(id bigint,parent_id bigint); INSERT INTO "+anchor+" VALUES(999,999); ALTER TABLE "+anchor+" ADD CONSTRAINT held_reference FOREIGN KEY(parent_id) REFERENCES "+parent+"(id) NOT VALID"); err != nil {
		t.Fatal("native NOT VALID reference binding with only REFERENCES", err)
	}
	var valid bool
	if err = c.QueryRowContext(ctx, "SELECT convalidated FROM pg_constraint WHERE conrelid=$1::regclass AND conname='held_reference'", anchor).Scan(&valid); err != nil || valid {
		t.Fatal("anchor scanned its preexisting invalid value", valid, err)
	}
	withAnchor, err := openGaussReadDesignReference(ctx, c, parentTarget)
	if err != nil || !errors.Is(schemaddl.VerifyOpenGaussReferenceUnchanged(baseline, child.Schema, desired, withAnchor), schemaddl.ErrConflict) {
		t.Fatal("temporary parent FK mutations became unchanged independent context", err)
	}
	probeTarget := target
	probeTarget.Object.Name = "reference_anchor"
	probeChild, err := openGaussReadDesign(ctx, c, probeTarget)
	if err != nil {
		t.Fatal("native independent reference probe child", err)
	}
	if err = openGaussCheckDesignReferenceCallbacks(ctx, c, probeChild.Schema.OpenGauss, withAnchor); err != nil {
		for _, entry := range withAnchor.Entries {
			if entry.Kind == "index" {
				t.Logf("native captured index metadata: %s", entry.Data)
			}
		}
		t.Fatal("native actually bound reference callbacks", err)
	}
	badCallbacks := probeChild.Schema.OpenGauss.Clone()
	for i, entry := range badCallbacks.Entries {
		if entry.Kind != "constraint" {
			continue
		}
		var raw map[string]json.RawMessage
		if json.Unmarshal(entry.Data, &raw) != nil {
			t.Fatal("native FK callback record")
		}
		raw["conpfeqop"] = json.RawMessage(`[1]`)
		badCallbacks.Entries[i].Data, _ = json.Marshal(raw)
		break
	}
	if _, err = badCallbacks.Fingerprint(); err != nil || !errors.Is(openGaussCheckDesignReferenceCallbacks(ctx, c, badCallbacks, withAnchor), errOpenGaussDesignDeclaration) {
		t.Fatal("low numeric OID alone authorized an FK equality callback", err)
	}
	probeNames := schemaddl.OpenGaussDesignNames{Columns: map[string]string{"id": child.Schema.Table.Columns()[0].ID, "parent_id": "new:parent"}, Constraints: map[string]string{"held_reference": "new:fk"}}
	referenceProbe, err := schemaddl.CaptureOpenGaussReferenceProbe(baseline, child.Schema, desired, probeChild.Schema.OpenGauss, probeNames, withAnchor)
	if err != nil {
		t.Fatal("native independent parent FK changes did not restore exact before context", err)
	}
	for i, entry := range withAnchor.Entries {
		if entry.Kind != "relation" || entry.OID != withAnchor.RelationOID {
			continue
		}
		badParent := withAnchor.Clone()
		var row map[string]json.RawMessage
		if json.Unmarshal(entry.Data, &row) != nil {
			t.Fatal("native parent relation")
		}
		row["unknown_probe_parent_property"] = json.RawMessage(`900719925474099312345678901234567890`)
		badParent.Entries[i].Data, _ = json.Marshal(row)
		if _, err = schemaddl.CaptureOpenGaussReferenceProbe(baseline, child.Schema, desired, probeChild.Schema.OpenGauss, probeNames, badParent); !errors.Is(err, schemaddl.ErrConflict) {
			t.Fatal("known FK effects hid an unrelated parent property change", err)
		}
		break
	}
	var pid int64
	if err = c.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatal(err)
	}
	assertHeld := func() {
		t.Helper()
		var held bool
		if err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=$1 AND relation=$2::regclass AND mode='ShareRowExclusiveLock' AND granted)", pid, parent).Scan(&held); err != nil || !held {
			t.Fatal("native reference lock lost across private declaration cleanup", held, err)
		}
	}
	assertHeld()
	if _, err = c.ExecContext(ctx, "SAVEPOINT declaration_probe; CREATE TABLE "+quote(target.Object.Schema)+".empty_probe(id bigint); ROLLBACK TO SAVEPOINT declaration_probe; RELEASE SAVEPOINT declaration_probe"); err != nil {
		t.Fatal(err)
	}
	assertHeld()
	writer, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err = writer.ExecContext(ctx, "BEGIN"); err != nil {
		t.Fatal(err)
	}
	defer rollbackDesign(writer)
	done := make(chan error, 1)
	go func() {
		_, err := writer.ExecContext(ctx, "ALTER TABLE "+parent+" ADD COLUMN committed_after_release bigint; COMMIT")
		done <- err
	}()
	parentRef := target.Object
	parentRef.Name = "other"
	openGaussViewWaiting(t, db, parentRef)
	if _, err = c.ExecContext(ctx, "DROP TABLE "+anchor+" RESTRICT"); err != nil {
		t.Fatal("anchor cleanup", err)
	}
	assertHeld()
	released, err := openGaussReadDesignReference(ctx, c, parentTarget)
	if err != nil || schemaddl.VerifyOpenGaussReferenceProbeReleased(referenceProbe, child.Schema, desired, released, "drop") != nil {
		t.Fatal("explicit native DROP left unverified parent probe effects", err)
	}
	if !errors.Is(schemaddl.VerifyOpenGaussReferenceProbeReleased(referenceProbe, child.Schema, desired, withAnchor, "drop"), schemaddl.ErrConflict) {
		t.Fatal("cleanup proof accepted live probe FK/triggers/dependencies")
	}
	select {
	case err := <-done:
		t.Fatal("reference writer finished before transaction lock release", err)
	default:
	}
	if err = rollbackDesign(c); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("reference writer did not finish after lock release", err)
		}
	case <-ctx.Done():
		t.Fatal("reference lock release was not acknowledged", ctx.Err())
	}
	var exists bool
	if err = db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_attribute WHERE attrelid=$1::regclass AND attname='committed_after_release' AND NOT attisdropped)", parent).Scan(&exists); err != nil || !exists {
		t.Fatal("reference writer did not actually commit its DDL", exists, err)
	}
}

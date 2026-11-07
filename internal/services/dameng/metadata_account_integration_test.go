package dameng

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func accountCertificate(t *testing.T, c connections.DamengConfig) connections.DamengConfig {
	t.Helper()
	if c.TLSMode == "disable" {
		return c
	}
	folder := t.TempDir()
	c.Options.CertificateFile, c.Options.KeyFile = filepath.Join(folder, "client.pem"), filepath.Join(folder, "client.key")
	csr, ext := filepath.Join(folder, "client.csr"), filepath.Join(folder, "extensions")
	if err := os.WriteFile(ext, []byte("basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=clientAuth\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"req", "-new", "-newkey", "rsa:2048", "-nodes", "-keyout", c.Options.KeyFile, "-out", csr, "-subj", "/CN=" + c.Username},
		{"x509", "-req", "-in", csr, "-CA", c.Options.CAFile, "-CAkey", filepath.Join(filepath.Dir(c.Options.CAFile), "..", "ca-key.pem"), "-CAcreateserial", "-out", c.Options.CertificateFile, "-days", "1", "-sha256", "-extfile", ext},
	} {
		if err := exec.Command("openssl", args...).Run(); err != nil {
			t.Fatal("owned account certificate generation failed")
		}
	}
	if err := os.Chmod(c.Options.KeyFile, 0o600); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNativeDamengOrdinaryMetadataVisibilityAndGeneratedColumnProof(t *testing.T) {
	c, password := nativeFixture(t)
	admin := nativeDB(t, c, password)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	user := "NYADB_METADATA_" + strings.ToUpper(nativeSuffix(t))
	secret := "Aa9!" + nativeSuffix(t)
	if _, err := admin.ExecContext(ctx, "CREATE USER "+quote(user)+" IDENTIFIED BY "+quote(secret)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.ExecContext(context.Background(), "DROP USER "+quote(user)+" CASCADE") })
	extra := "NYADB_USER_SCHEMA_" + nativeSuffix(t)
	foreign := "NYADB_GRANTED_" + nativeSuffix(t)
	hidden := "NYADB_HIDDEN_" + nativeSuffix(t)
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+quote(extra)+" AUTHORIZATION "+quote(user)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.ExecContext(context.Background(), "DROP SCHEMA "+quote(extra)+" CASCADE") })
	for _, name := range []string{foreign, hidden} {
		if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+quote(name)+" AUTHORIZATION SYSDBA"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { admin.ExecContext(context.Background(), "DROP SCHEMA "+quote(name)+" CASCADE") })
	}
	for _, schema := range []string{user, extra, foreign, hidden} {
		if _, err := admin.ExecContext(ctx, "CREATE TABLE "+quote(schema)+`."Data"(ID BIGINT PRIMARY KEY,I BIGINT IDENTITY(1,1),G BIGINT GENERATED ALWAYS AS(ID+1) VIRTUAL,T VARCHAR(64) DEFAULT 'IDENTITY(1,1)')`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := admin.ExecContext(ctx, "GRANT SELECT ON "+quote(foreign)+`."Data" TO `+quote(user)); err != nil {
		t.Fatal(err)
	}
	c.Username, c.Options.MaxConnections = user, 1
	c = accountCertificate(t, c)
	s := fixtureSession(t, c, secret)
	namespaces, err := s.Namespaces(ctx)
	if err != nil {
		t.Fatal("ordinary native schema browse failed", err)
	}
	seen := make(map[string]bool)
	for _, n := range namespaces {
		seen[n.Ref.Schema] = true
	}
	if !seen[user] || !seen[extra] || !seen[foreign] {
		t.Fatal("ordinary metadata lost an owned/granted schema", seen)
	}
	t.Log("native catalog also enumerates an ungranted schema:", seen[hidden])
	for _, schema := range []string{user, extra, foreign} {
		objects, err := s.Browse(ctx, s.Namespace(schema))
		if err != nil || len(objects) != 1 || objects[0].Ref.Schema != schema || objects[0].Name != "Data" {
			t.Fatal("ordinary native object visibility failed", schema, err, objects)
		}
		table, err := s.TableMetadata(ctx, s.Namespace(schema).Object("table", "Data"))
		if err != nil || len(table.Columns) != 4 || !table.Columns[1].Generated || !table.Columns[2].Generated || table.Columns[3].Generated || table.Editable {
			t.Fatal("ordinary native generated-field metadata failed", schema, err, table)
		}
	}
	objects, err := s.Browse(ctx, s.Namespace(hidden))
	if err != nil || len(objects) != 0 {
		t.Fatal("native object privileges exposed an ungranted table", err, objects)
	}
	t.Log("native catalog objects in ungranted schema:", len(objects))
	if rows, err := s.db.QueryContext(ctx, "SELECT * FROM "+quote(hidden)+`."Data"`); err == nil {
		rows.Close()
		t.Fatal("ungranted native table data became readable")
	}
}

package engines

import (
	"errors"
	"reflect"
	"testing"

	gausspq "gitcode.com/opengauss/openGauss-connector-go-pq"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestOpenGaussNativeConfigKeepsExplicitTLSCredentialsAndSettings(t *testing.T) {
	t.Setenv("PGPASSWORD", "environment-password")
	t.Setenv("PGSSLCERT", "/missing/environment.crt")
	t.Setenv("PGSSLKEY", "/missing/environment.key")
	t.Setenv("PGSSLROOTCERT", "/missing/environment-ca.crt")
	t.Setenv("PGSSLNEGOTIATION", "direct")
	p, _ := connections.Defaults(connections.OpenGauss)
	p.Name, p.Database, p.Username = "fixture", "fixed", "reader"
	p.PostgreSQLOptions.DefaultSchema = `a"b`
	p.PostgreSQLOptions.StatementTimeoutMS = 1234
	p.PostgreSQLOptions.ApplicationName = "openGauss settings"
	cfg, err := openGaussConfig(p, "explicit-password", nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Password != "explicit-password" || cfg.User != "reader" || cfg.Database != "fixed" || cfg.TLSConfig == nil || cfg.TLSConfig.InsecureSkipVerify || cfg.TLSConfig.ServerName != "localhost" || cfg.RuntimeParams["search_path"] != `"a""b"` || cfg.RuntimeParams["statement_timeout"] != "1234" || cfg.RuntimeParams["application_name"] != "openGauss settings" || cfg.Logger != nil {
		t.Fatal("explicit profile settings were not preserved")
	}
	if _, ok := cfg.RuntimeParams["lock_timeout"]; ok {
		t.Fatal("PostgreSQL lock timeout injected")
	}
	if _, ok := cfg.RuntimeParams["lockwait_timeout"]; ok {
		t.Fatal("administrator-only timeout injected")
	}
	if len(cfg.Fallbacks) != 0 {
		t.Fatal("verified TLS can fall back to plaintext")
	}
	cfg, err = openGaussConfig(p, "", nil)
	if err != nil || cfg.Password != "" {
		t.Fatal("environment password was used", err)
	}
	p.PostgreSQLOptions.LockTimeoutMS = 100
	if _, err = openGaussConfig(p, "", nil); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("unsupported lock timeout accepted", err)
	}
}

func TestOpenGaussCatalogVectorsAndUnsafeKeys(t *testing.T) {
	for _, raw := range []string{"1 2 9", "{1,2,9}"} {
		got, err := openGaussNumbers(raw)
		if err != nil || !reflect.DeepEqual(got, []int{1, 2, 9}) {
			t.Fatal("attribute vector", raw, err, got)
		}
	}
	for _, raw := range []string{"1 NULL", "{1,broken}", "-1", "32768", "[0:1]={1,2}"} {
		if _, err := openGaussNumbers(raw); err == nil {
			t.Fatal("invalid vector accepted", raw)
		}
	}
	numbers := map[int]string{1: "id", 2: "note"}
	fields := map[string]contract.Field{"id": {Name: "id", DatabaseType: "integer"}, "note": {Name: "note", Nullable: true}}
	i := openGaussIndex{unique: true, keyCount: 1, numbers: []int{1, 2}}
	if key := openGaussKey(i, numbers, fields); !reflect.DeepEqual(key, []string{"id"}) {
		t.Fatal("included column incorrectly used as a key", key)
	}
	for _, bad := range []openGaussIndex{{unique: true, unsafe: true, keyCount: 1, numbers: []int{1}}, {unique: true, keyCount: 1, numbers: []int{2}}, {unique: true, keyCount: 1, numbers: []int{0}}} {
		if len(openGaussKey(bad, numbers, fields)) != 0 {
			t.Fatal("unsafe key accepted")
		}
	}
	fields["id"] = contract.Field{Name: "id", Generated: true}
	if len(openGaussKey(i, numbers, fields)) != 0 {
		t.Fatal("generated key used for import identity")
	}
}

func TestOpenGaussNativeErrorsKeepSQLStateAndUnicodePosition(t *testing.T) {
	native := &gausspq.Error{Severity: "ERROR", Code: "42601", Message: "syntax error", Position: "10", Detail: "detail", Hint: "hint"}
	sql := "SELECT '长'"
	err := queryDiagnostic(errors.Join(errors.New("wrapped"), native), sql, 2)
	var query *contract.QueryError
	if !errors.As(err, &query) || !errors.Is(err, native) || query.Diagnostic.Code != "42601" || query.Diagnostic.Detail != "detail" || query.Diagnostic.Hint != "hint" || query.Diagnostic.Statement != 2 || !reflect.DeepEqual(query.Diagnostic.Position, contract.QueryCharacterPosition(sql, 10)) {
		t.Fatal("native error details lost", err)
	}
	if failureState(connections.OpenGauss, "INSERT INTO records VALUES(1)", native) != "not_executed" || failureState(connections.OpenGauss, "CALL procedure()", native) != "uncertain" {
		t.Fatal("native failure outcome incorrectly classified")
	}
	if failureState(connections.OpenGauss, "COMMIT", &gausspq.Error{Severity: "FATAL", Code: "08006"}) != "uncertain" {
		t.Fatal("lost commit claimed to be unexecuted")
	}
}

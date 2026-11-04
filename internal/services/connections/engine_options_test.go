package connections_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestSQLiteOptionsValidateNamesFilesAndCloneAttachments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "attached.sqlite")
	a := connections.DefaultSQLiteOptions()
	a.Attachments = []connections.SQLiteAttachment{{Name: " attached ", File: path}}
	n, err := connections.NormalizeSQLiteOptions(a)
	if err != nil || n.Attachments[0].Name != "attached" {
		t.Fatalf("normalize: %+v %v", n, err)
	}
	n.Attachments[0].Name = "changed"
	if a.Attachments[0].Name != " attached " {
		t.Fatal("normalized options share mutable attachments")
	}
	for _, bad := range []string{"", "MAIN", "temp", "sqlite_internal", "control\nname"} {
		invalid := connections.DefaultSQLiteOptions()
		invalid.Attachments = []connections.SQLiteAttachment{{Name: bad, File: path}}
		if _, err := connections.NormalizeSQLiteOptions(invalid); !errors.Is(err, connections.ErrInvalid) {
			t.Fatalf("accepted invalid name %q: %v", bad, err)
		}
	}
	for _, attachments := range [][]connections.SQLiteAttachment{
		{{Name: "a", File: "relative.sqlite"}},
		{{Name: "a", File: path}, {Name: "A", File: path + "2"}},
		{{Name: "a", File: path}, {Name: "b", File: path}},
	} {
		invalid := connections.DefaultSQLiteOptions()
		invalid.Attachments = attachments
		if _, err := connections.NormalizeSQLiteOptions(invalid); !errors.Is(err, connections.ErrInvalid) {
			t.Fatalf("accepted invalid attachments: %v", err)
		}
	}
	legacy, err := connections.NormalizeSQLiteOptions(connections.SQLiteOptions{})
	if err != nil || legacy.RequireExisting || !legacy.ForeignKeys || legacy.BusyTimeoutMS != 1000 {
		t.Fatalf("legacy behavior changed: %+v %v", legacy, err)
	}
}

func TestEngineOptionsRejectInvalidAndCrossEngineSettings(t *testing.T) {
	for _, mutate := range []func(*connections.PostgreSQLOptions){
		func(a *connections.PostgreSQLOptions) { a.MaxConnections = 0 },
		func(a *connections.PostgreSQLOptions) { a.ConnectTimeoutSeconds = 3601 },
		func(a *connections.PostgreSQLOptions) { a.StatementTimeoutMS = -1 },
		func(a *connections.PostgreSQLOptions) { a.CertificateFile = "unpaired.crt" },
		func(a *connections.PostgreSQLOptions) { a.DefaultSchema = "schema\n" },
	} {
		a := connections.DefaultPostgreSQLOptions()
		mutate(&a)
		if _, err := connections.NormalizePostgreSQLOptions(a); !errors.Is(err, connections.ErrInvalid) {
			t.Fatalf("accepted invalid PostgreSQL settings: %v", err)
		}
	}
	p, _ := connections.Defaults(connections.MySQL)
	p.Name = "invalid"
	p.SQLiteOptions = connections.DefaultSQLiteOptions()
	if _, err := connections.NormalizeProfile(p); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("accepted SQLite options on MySQL")
	}
	p.SQLiteOptions = connections.SQLiteOptions{}
	p.PostgreSQLOptions = connections.DefaultPostgreSQLOptions()
	if _, err := connections.NormalizeProfile(p); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("accepted PostgreSQL options on MySQL")
	}
}

func TestIndependentPoolLimitDefaultsAndBounds(t *testing.T) {
	for _, limit := range []int{-1, 0, 1, 64, 65} {
		pg := connections.DefaultPostgreSQLOptions()
		pg.SQLMaxConnections = limit
		n, err := connections.NormalizePostgreSQLOptions(pg)
		if limit < 0 || limit > 64 {
			if !errors.Is(err, connections.ErrInvalid) {
				t.Fatal(limit, err)
			}
		} else if err != nil || limit == 0 && n.SQLMaxConnections != 8 || limit > 0 && n.SQLMaxConnections != limit {
			t.Fatal(limit, n, err)
		}
		for _, sqlBudget := range []bool{false, true} {
			sq := connections.DefaultSQLiteOptions()
			if sqlBudget {
				sq.SQLMaxConnections = limit
			} else {
				sq.MaxConnections = limit
			}
			n, err := connections.NormalizeSQLiteOptions(sq)
			if limit < 0 || limit > 64 {
				if !errors.Is(err, connections.ErrInvalid) {
					t.Fatal(limit, err)
				}
			} else {
				want := limit
				if want == 0 {
					want = 8
				}
				got := n.MaxConnections
				if sqlBudget {
					got = n.SQLMaxConnections
				}
				if err != nil || got != want {
					t.Fatal(limit, got, err)
				}
			}
		}
	}
}

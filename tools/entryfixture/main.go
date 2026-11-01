// entryfixture is an opt-in acceptance helper, never a production startup path.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	mysqldriver "github.com/go-sql-driver/mysql"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/credentials"
	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/internal/storage/localdb"
	keyring "github.com/zalando/go-keyring"
)

func require(ok bool, message string) {
	if !ok {
		panic(message)
	}
}
func check(err error) {
	if err != nil {
		panic(err)
	}
}
func guard() string {
	root, err := filepath.Abs(os.Getenv("NYADB_ENTRY_ROOT"))
	check(err)
	marker, err := os.ReadFile(filepath.Join(root, ".owned"))
	check(err)
	require(os.Getenv("NYADB_ENTRY_IT") == "1" && len(bytes.TrimSpace(marker)) == 12, "explicit owned fixture required")
	require(os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "unix:path="+root+"/runtime/bus", "refusing non-fixture keyring")
	require(os.Getenv("XDG_CONFIG_HOME") == filepath.Join(root, "config"), "refusing shared config")
	return root
}
func digest(ctx context.Context, repo queries.Repository) [32]byte {
	var rs []queries.Record
	for _, kind := range []string{"workspace", "query", "query_deleted", "query_catalog"} {
		r, e := repo.List(ctx, kind, 0)
		check(e)
		rs = append(rs, r...)
	}
	b, e := json.Marshal(rs)
	check(e)
	return sha256.Sum256(b)
}
func workspace() queries.Workspace {
	return queries.Workspace{ActiveID: 7, Revision: 1, Documents: []queries.Document{{ID: 7, QueryID: "entry-query", Name: "密钥环查询", ProfileID: 11, Database: "entry_source", Text: "SELECT 42; -- unsaved fixture draft", SavedText: "SELECT 42;", Revision: 2, NamedRevision: 1, Cursor: 9, Anchor: 9}}}
}
func named() queries.Named {
	return queries.Named{ID: "entry-query", ProfileID: 11, Name: "密钥环查询", Database: "entry_source", Text: "SELECT 42;", Revision: 1}
}
func main() {
	root := guard()
	require(len(os.Args) == 2, "one phase required")
	ctx := context.Background()
	db, e := localdb.Open(filepath.Join(root, "config", "vault.sqlite"))
	check(e)
	defer db.Close()
	repo := localdb.NewSQLRepository(db)
	store := queries.NewStore(repo, credentials.WorkspaceKey{})
	vault := credentials.NewOSVault()
	switch os.Args[1] {
	case "write":
		for _, id := range []int64{11, 12} {
			check(vault.Set(id, "", os.Getenv("NYADB_ENTRY_PASSWORD")))
			check(vault.Set(id, "备用", os.Getenv("NYADB_ENTRY_READ_PASSWORD")))
		}
		check(store.SaveNamed(ctx, named()))
		check(store.SaveCatalog(ctx, queries.Catalog{ProfileID: 11, Revision: 1, Groups: []queries.Group{{ID: "entry-group", Name: "验收分组"}}, Membership: map[string]string{"entry-query": "entry-group"}}))
		check(store.SaveWorkspace(ctx, workspace()))
	case "read":
		// Each invocation is a fresh OS process: no MockInit or cached key material.
		for _, id := range []int64{11, 12} {
			for variant, want := range map[string]string{"": os.Getenv("NYADB_ENTRY_PASSWORD"), "备用": os.Getenv("NYADB_ENTRY_READ_PASSWORD")} {
				got, found, e := vault.Get(id, variant)
				check(e)
				require(found && got == want, "credential isolation/recovery failed")
			}
		}
		ns, e := store.ListNamed(ctx, 11)
		check(e)
		require(len(ns) == 1 && ns[0] == named(), "query recovery failed")
		other, e := store.ListNamed(ctx, 12)
		check(e)
		require(len(other) == 0, "query crossed profile")
		cs, e := store.ListCatalogs(ctx)
		check(e)
		require(len(cs) == 1 && cs[0].Groups[0].Name == "验收分组" && cs[0].Membership["entry-query"] == "entry-group", "catalog recovery failed")
		w, e := store.LoadWorkspace(ctx)
		check(e)
		a, _ := json.Marshal(w)
		b, _ := json.Marshal(workspace())
		require(bytes.Equal(a, b), "draft/caret recovery failed")
	case "keys":
		key, found, e := (credentials.WorkspaceKey{}).Get()
		check(e)
		require(found, "original key missing")
		// Backup remains in the isolated encrypted Secret Service, never on disk.
		check(vault.Set(999, "workspace-backup", base64.StdEncoding.EncodeToString(key)))
		defer func() { check((credentials.WorkspaceKey{}).Set(key)); check(vault.Delete(999, "workspace-backup")) }()
		before := digest(ctx, repo)
		check(keyring.Delete("NyaDB SQL workspace", "master-v1"))
		lost := queries.NewStore(repo, credentials.WorkspaceKey{})
		w := workspace()
		w.Revision++
		require(errors.Is(lost.SaveWorkspace(ctx, w), queries.ErrKeyUnavailable), "missing key allowed write")
		_, exists, e := (credentials.WorkspaceKey{}).Get()
		check(e)
		require(!exists, "replacement key generated")
		require(before == digest(ctx, repo), "missing key changed ciphertext")
		wrong := make([]byte, 32)
		_, e = rand.Read(wrong)
		check(e)
		check((credentials.WorkspaceKey{}).Set(wrong))
		// Test write first: this caught the previous unauthenticated workspace overwrite.
		require(queries.NewStore(repo, credentials.WorkspaceKey{}).SaveWorkspace(ctx, w) != nil, "wrong key replaced workspace")
		n := named()
		n.Revision++
		require(queries.NewStore(repo, credentials.WorkspaceKey{}).SaveNamed(ctx, n) != nil, "wrong key replaced query")
		c := queries.Catalog{ProfileID: 11, Revision: 2}
		require(queries.NewStore(repo, credentials.WorkspaceKey{}).SaveCatalog(ctx, c) != nil, "wrong key replaced catalog")
		require(before == digest(ctx, repo), "wrong key changed ciphertext")
	case "unavailable":
		before := digest(ctx, repo)
		w := workspace()
		w.Revision++
		require(errors.Is(store.SaveWorkspace(ctx, w), queries.ErrKeyUnavailable), "unavailable keyring accepted write")
		require(before == digest(ctx, repo), "unavailable service changed records")
		check(queries.WriteFile(filepath.Join(root, "evidence", "retained-draft.sql"), w.Documents[0].Text))
		text, e := queries.ReadFile(filepath.Join(root, "evidence", "retained-draft.sql"))
		check(e)
		require(text == w.Documents[0].Text, "explicit SQL file lost text")
	case "seed":
		seed(root)
	case "native-renamed", "native-deleted", "native-saved", "native-grouped":
		nativeCheck(root, os.Args[1])
	case "native-prepare", "native-restore", "native-account-created", "native-account-granted":
		nativeDatabase(root, os.Args[1])
	default:
		panic("unknown fixture phase")
	}
	fmt.Println("PASS real Secret Service / SQLite phase:", os.Args[1])
}
func seed(root string) {
	db, e := localdb.Open(filepath.Join(root, "config", "NyaDB", "config.sqlite"))
	check(e)
	defer db.Close()
	ctx := context.Background()
	svc := connections.NewService(localdb.NewProfileStore(db))
	require(os.Getenv("NYADB_ENTRY_PORT") != "", "fixture port required")
	var port uint16
	_, e = fmt.Sscan(os.Getenv("NYADB_ENTRY_PORT"), &port)
	check(e)
	var admin connections.Profile
	for _, spec := range []struct{ name, user, password string }{{"验收管理员", "entry_admin", os.Getenv("NYADB_ENTRY_PASSWORD")}, {"验收只读", "entry_read", os.Getenv("NYADB_ENTRY_READ_PASSWORD")}} {
		p, e := svc.Create(ctx, connections.Profile{Name: spec.name, Kind: connections.MySQL, Host: "127.0.0.1", Port: port, Username: spec.user, Database: "entry_source", MySQLAdvanced: connections.DefaultMySQLAdvanced()})
		check(e)
		check(credentials.NewOSVault().Set(p.ID, "", spec.password))
		if spec.user == "entry_admin" {
			admin = p
		}
	}
	sqlitePath := filepath.Join(root, "fixture.sqlite")
	fixture, e := localdb.Open(sqlitePath)
	check(e)
	_, e = fixture.Exec("CREATE TABLE fixture(id INTEGER PRIMARY KEY, label TEXT); INSERT INTO fixture VALUES(1,'native fixture')")
	check(e)
	check(fixture.Close())
	_, e = svc.Create(ctx, connections.Profile{Name: "验收 SQLite", Kind: connections.SQLite, Database: sqlitePath})
	check(e)
	check(localdb.SetPreference(db, "appearance", "dark"))
	store := queries.NewStore(localdb.NewSQLRepository(db), credentials.WorkspaceKey{})
	n := named()
	n.ProfileID = admin.ID
	n.Name = "原生验收查询"
	check(store.SaveNamed(ctx, n))
	check(store.SaveCatalog(ctx, queries.Catalog{ProfileID: admin.ID, Revision: 1, Groups: []queries.Group{{ID: "entry-group", Name: "验收分组"}}, Membership: map[string]string{n.ID: "entry-group"}}))
	w := workspace()
	w.Documents[0].ProfileID = admin.ID
	w.Documents[0].Name = n.Name
	check(store.SaveWorkspace(ctx, w))
}

func nativeCheck(root, phase string) {
	db, e := localdb.Open(filepath.Join(root, "config/NyaDB/config.sqlite"))
	check(e)
	defer db.Close()
	ctx := context.Background()
	repo := localdb.NewSQLRepository(db)
	deadline := time.Now().Add(5 * time.Second)
	for {
		store := queries.NewStore(repo, credentials.WorkspaceKey{})
		ns, e := store.ListNamed(ctx, 0)
		check(e)
		cs, e := store.ListCatalogs(ctx)
		check(e)
		w, e := store.LoadWorkspace(ctx)
		check(e)
		var doc queries.Document
		for _, d := range w.Documents {
			if d.ID == 7 {
				doc = d
			}
		}
		text := workspace().Documents[0].Text
		valid := doc.Text == text
		switch phase {
		case "native-renamed":
			valid = valid && len(ns) == 1 && ns[0].ID == "entry-query" && ns[0].Name == "已重命名查询" && ns[0].Text == named().Text && doc.Name == ns[0].Name && doc.NamedRevision == ns[0].Revision && doc.SavedText == named().Text && doc.Text != doc.SavedText
		case "native-deleted":
			valid = valid && len(ns) == 0 && doc.QueryID == "" && doc.SavedText == ""
		case "native-saved", "native-grouped":
			valid = valid && len(ns) == 1 && ns[0].ID != "entry-query" && ns[0].Name == "删除后新查询" && ns[0].Text == text && doc.QueryID == ns[0].ID && doc.SavedText == text
			if phase == "native-grouped" && valid {
				grouped := false
				for _, c := range cs {
					for _, g := range c.Groups {
						if g.Name == "原生新增组" && c.Membership[ns[0].ID] == g.ID {
							grouped = true
						}
					}
				}
				valid = valid && grouped
			}
		}
		if valid {
			evidence := struct {
				Phase            string
				Queries          []queries.Named
				Catalogs         []queries.Catalog
				Document         queries.Document
				CiphertextSHA256 string
			}{phase, ns, cs, doc, fmt.Sprintf("%x", digest(ctx, repo))}
			// Only the deliberately seeded fixture SQL is included; never vault values.
			b, e := json.MarshalIndent(evidence, "", "  ")
			check(e)
			check(os.WriteFile(filepath.Join(root, "evidence", phase+".json"), b, 0600))
			return
		}
		require(time.Now().Before(deadline), "native UI did not persist expected "+phase+" state")
		time.Sleep(100 * time.Millisecond)
	}
}
func nativeDatabase(root, phase string) {
	cfg := mysqldriver.NewConfig()
	cfg.User = "entry_admin"
	cfg.Passwd = os.Getenv("NYADB_ENTRY_PASSWORD")
	cfg.Net = "tcp"
	cfg.Addr = "127.0.0.1:" + os.Getenv("NYADB_ENTRY_PORT")
	cfg.Timeout = 5 * time.Second
	connector, e := mysqldriver.NewConnector(cfg)
	check(e)
	db := sql.OpenDB(connector)
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if phase == "native-prepare" {
		for _, stmt := range []string{"DROP DATABASE IF EXISTS entry_native_target", "CREATE DATABASE entry_native_target", "DROP USER IF EXISTS 'entry_native_account'@'%'"} {
			_, e = db.ExecContext(ctx, stmt)
			check(e)
		}
		return
	}
	if phase == "native-account-created" || phase == "native-account-granted" {
		var n int
		check(db.QueryRowContext(ctx, "SELECT COUNT(*) FROM mysql.user WHERE User='entry_native_account' AND Host='%' ").Scan(&n))
		require(n == 1, "native account was not created")
		if phase == "native-account-granted" {
			check(db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.SCHEMA_PRIVILEGES WHERE GRANTEE="'entry_native_account'@'%'" AND TABLE_SCHEMA='entry_source' AND PRIVILEGE_TYPE='SELECT'`).Scan(&n))
			require(n == 1, "native account grant absent")
		}
		b, e := json.MarshalIndent(map[string]any{"phase": phase, "verified_from_database": true}, "", "  ")
		check(e)
		check(os.WriteFile(filepath.Join(root, "evidence", phase+".json"), b, 0600))
		return
	}
	rows, e := db.QueryContext(ctx, "SELECT id,CAST(amount AS CHAR),optional,HEX(payload) FROM entry_native_target.samples ORDER BY id")
	check(e)
	defer rows.Close()
	var actual []string
	for rows.Next() {
		var id int
		var amount, hex string
		var optional sql.NullString
		check(rows.Scan(&id, &amount, &optional, &hex))
		v := "<NULL>"
		if optional.Valid {
			v = optional.String
		}
		actual = append(actual, fmt.Sprintf("%d|%s|%s|%s", id, amount, v, hex))
	}
	check(rows.Err())
	require(strings.Join(actual, "\n") == "1|12345678901234567890.12345678901234567890|<NULL>|0001FF\n2|-0.00000000000000000001|fixture|", "native restore typed data mismatch")
	b, e := json.MarshalIndent(map[string]any{"phase": phase, "schema": "entry_native_target", "rows": actual, "verified_from_database": true}, "", "  ")
	check(e)
	check(os.WriteFile(filepath.Join(root, "evidence", phase+".json"), b, 0600))
}

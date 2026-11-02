package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/credentials"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/internal/storage/localdb"
)

func seedClickHouse(root string) {
	config := filepath.Join(root, "config/NyaDB/config.sqlite")
	_, err := os.Stat(config)
	require(os.IsNotExist(err), "ClickHouse seed requires empty owned config")
	port, err := strconv.ParseUint(os.Getenv("NYADB_CLICKHOUSE_PORT"), 10, 16)
	check(err)
	require(port > 0 && os.Getenv("NYADB_CLICKHOUSE_NATIVE_OWNED") == "1" && len(os.Getenv("NYADB_CLICKHOUSE_PASSWORD")) == 64, "owned native ClickHouse fixture required")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	db, err := localdb.Open(config)
	check(err)
	defer db.Close()
	p, err := connections.Defaults(connections.ClickHouse)
	check(err)
	p.Name, p.Host, p.Port, p.Username = "验收 ClickHouse", "127.0.0.1", uint16(port), "nyadb_it"
	p, err = localdb.NewProfileStore(db).Create(ctx, p)
	check(err)
	password := os.Getenv("NYADB_CLICKHOUSE_PASSWORD")
	check(credentials.NewOSVault().Set(p.ID, "", password))
	m := dbruntime.New()
	dbruntime.RegisterBuiltins(m, nil)
	defer m.Close()
	s, err := m.ConnectSession(ctx, p, password)
	check(err)
	provider, err := contract.Require[contract.QueryProvider](s)
	check(err)
	l, err := provider.ReserveQuery(7)
	check(err)
	defer l.Release()
	_, err = l.Execute(ctx, "default", `CREATE TABLE records(id UInt64, label String, amount Decimal(38,20), note Nullable(String)) ENGINE=MergeTree ORDER BY id; INSERT INTO records VALUES(9007199254740993,'native ClickHouse fixture','123456789012345678.12345678901234567890',NULL),(2,'second native row','0.00000000000000000001','NULL')`)
	check(err)
	store := queries.NewStore(localdb.NewSQLRepository(db), credentials.WorkspaceKey{})
	text := "SELECT id,label,amount,note FROM records ORDER BY id DESC;"
	check(store.SaveNamed(ctx, queries.Named{ID: "clickhouse-native-query", ProfileID: p.ID, Database: "default", Name: "ClickHouse 示例", Text: text, Revision: 1}))
	// Begin on the object page so the navigation replay's Enter keystrokes
	// cannot edit the restored SQL document before it is explicitly activated.
	check(store.SaveWorkspace(ctx, queries.Workspace{ActiveID: 0, Revision: 1, Documents: []queries.Document{{ID: 7, QueryID: "clickhouse-native-query", Name: "ClickHouse 示例", ProfileID: p.ID, Database: "default", Text: text, SavedText: text, Revision: 1, NamedRevision: 1}}}))
	check(localdb.SetPreference(db, "appearance", "dark"))
}

func nativeClickHouse(root string) {
	db, err := localdb.Open(filepath.Join(root, "config/NyaDB/config.sqlite"))
	check(err)
	defer db.Close()
	ctx := context.Background()
	profiles, err := localdb.NewProfileStore(db).List(ctx)
	check(err)
	var profile int64
	for _, p := range profiles {
		if p.Kind == connections.ClickHouse {
			profile = p.ID
		}
	}
	require(profile > 0, "native ClickHouse profile missing")
	store := queries.NewStore(localdb.NewSQLRepository(db), credentials.WorkspaceKey{})
	text := "SELECT id,label,amount,note FROM records ORDER BY id DESC;"
	deadline := time.Now().Add(5 * time.Second)
	for {
		w, err := store.LoadWorkspace(ctx)
		check(err)
		document := false
		for _, d := range w.Documents {
			if d.QueryID == "clickhouse-native-query" {
				document = d.ProfileID == profile && d.Database == "default" && d.Text == text && d.SavedText == text
			}
		}
		h, err := store.LoadHistory(ctx, time.Now())
		check(err)
		executed := false
		for _, e := range h.Entries {
			// Running the caret statement omits the trailing separator; the
			// saved editor text still keeps it verbatim.
			executed = executed || e.ProfileID == profile && e.Database == "default" && e.Source == "query" && e.SQL == strings.TrimSuffix(text, ";") && e.State == "success"
		}
		if document && executed {
			return
		}
		if time.Now().After(deadline) {
			panic(fmt.Sprintf("native ClickHouse checkpoint failed: document=%t execution=%t", document, executed))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

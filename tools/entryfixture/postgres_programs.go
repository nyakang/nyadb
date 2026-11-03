package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/storage/localdb"
)

func seedPostgresPrograms(root string) {
	config := filepath.Join(root, "config/NyaDB/config.sqlite")
	_, err := os.Stat(config)
	require(os.IsNotExist(err), "PostgreSQL programs seed requires an empty owned config")
	port, err := strconv.ParseUint(os.Getenv("NYADB_PROGRAM_PORT"), 10, 16)
	check(err)
	require(port != 0, "owned PostgreSQL fixture port required")
	db, err := localdb.Open(config)
	check(err)
	defer db.Close()
	p, err := connections.Defaults(connections.PostgreSQL)
	check(err)
	p.Name, p.Host, p.Port, p.Username, p.Database, p.TLSMode = "验收 PostgreSQL 程序目录", "127.0.0.1", uint16(port), "nyadb_it", "nyadb_it", "disable"
	_, err = localdb.NewProfileStore(db).Create(context.Background(), p)
	check(err)
	check(localdb.SetPreference(db, "appearance", "dark"))
}

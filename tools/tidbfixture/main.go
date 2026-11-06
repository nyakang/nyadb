// Command tidbfixture initializes only explicitly enabled disposable TiDB fixtures.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
	"strconv"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
)

func initialize() error {
	// Readiness retries can hit the SQL listener before TiDB finishes startup.
	// Keep those transport diagnostics out of fixture output and credentials.
	mysqldriver.SetLogger(log.New(io.Discard, "", 0))
	if os.Getenv("NYADB_TIDB_IT") != "1" {
		return errors.New("disposable TiDB fixture opt-in required")
	}
	port, err := strconv.Atoi(os.Getenv("NYADB_TIDB_PORT"))
	if err != nil || port < 1024 || port > 65535 {
		return errors.New("explicit loopback fixture port required")
	}
	password := os.Getenv("NYADB_TIDB_PASSWORD")
	if !regexp.MustCompile(`^[a-f0-9]{48}$`).MatchString(password) {
		return errors.New("fixture credential required")
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_TIDB_CA_FILE"))
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(raw) {
		return errors.New("fixture CA required")
	}
	if err = mysqldriver.RegisterTLSConfig("tidb-fixture", &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: "127.0.0.1"}); err != nil {
		return err
	}
	defer mysqldriver.DeregisterTLSConfig("tidb-fixture")
	config := mysqldriver.NewConfig()
	config.User = "root"
	config.Net = "tcp"
	config.Addr = fmt.Sprintf("127.0.0.1:%d", port)
	config.TLSConfig = "tidb-fixture"
	config.Timeout = time.Second
	config.ReadTimeout = 10 * time.Second
	config.WriteTimeout = 10 * time.Second
	db, err := sql.Open("mysql", config.FormatDSN())
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	for {
		err = db.PingContext(ctx)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		time.Sleep(200 * time.Millisecond)
	}
	var version string
	if err = db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version); err != nil {
		return err
	}
	if !regexp.MustCompile(`(?i)TiDB-v8\.5\.`).MatchString(version) {
		return errors.New("fixture server must be TiDB 8.5")
	}
	for _, statement := range []string{
		"CREATE DATABASE nyadb_tidb_it CHARACTER SET utf8mb4 COLLATE utf8mb4_bin",
		"CREATE USER 'nyadb_tidb'@'%' IDENTIFIED BY '" + password + "' REQUIRE SSL",
		"GRANT ALL PRIVILEGES ON *.* TO 'nyadb_tidb'@'%' WITH GRANT OPTION",
	} {
		if _, err = db.ExecContext(ctx, statement); err != nil {
			return errors.New("fixture initialization failed")
		}
	}
	fmt.Println("TiDB fixture initialized:", version)
	return nil
}
func main() {
	if err := initialize(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

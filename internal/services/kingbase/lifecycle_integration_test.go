package kingbase

import (
	"context"
	"database/sql"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNativeKingbaseTLSVerifiesIdentityWithoutPlaintextFallback(t *testing.T) {
	c, password := nativeFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if c.Options.CAFile == "" {
		c.TLSMode = "require"
		db, err := OpenDB(c, password, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if err = db.PingContext(ctx); err == nil {
			t.Fatal("plaintext server accepted required TLS")
		}
		return
	}
	// A captured dial target preserves the server socket while testing the
	// configured hostname. verify-full must reject this certificate mismatch.
	address := net.JoinHostPort(c.Host, strconv.Itoa(int(c.Port)))
	dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	c.Host = "incorrect-fixture.invalid"
	db, err := OpenDB(c, password, dial)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.PingContext(ctx); err == nil {
		t.Fatal("verify-full accepted the wrong native hostname")
	}
	db.Close()
	c.TLSMode = "verify-ca"
	db, err = OpenDB(c, password, dial)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.PingContext(ctx); err != nil {
		t.Fatal("verify-ca incorrectly requires a hostname", err)
	}
	c.TLSMode = "verify-full"
	c.Host = "127.0.0.1"
	db, err = OpenDB(c, password, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var ssl bool
	if err = db.QueryRowContext(ctx, "SELECT ssl FROM pg_stat_ssl WHERE pid=pg_backend_pid()").Scan(&ssl); err != nil || !ssl {
		t.Fatal("native session did not prove TLS", err)
	}
	// Removing the configured CA must not fall back to unverified TLS.
	c.Options.CAFile = ""
	untrusted, err := OpenDB(c, password, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer untrusted.Close()
	if err = untrusted.PingContext(ctx); err == nil {
		t.Fatal("untrusted certificate accepted")
	}
}

func TestNativeKingbaseCancellationAcknowledgesAndRetiresOriginalSession(t *testing.T) {
	c, password := nativeFixture(t)
	ctx, stop := context.WithTimeout(context.Background(), 60*time.Second)
	defer stop()
	db, err := OpenDB(c, password, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	monitor, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer monitor.Close()
	for _, method := range []string{"query", "exec", "prepared-query", "prepared-exec", "transaction"} {
		t.Run(method, func(t *testing.T) {
			original, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer original.Close()
			var pid int
			if err = original.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
				t.Fatal(err)
			}
			query, cancel := context.WithCancel(ctx)
			defer cancel()
			var statement *sql.Stmt
			if strings.HasPrefix(method, "prepared-") {
				statement, err = original.PrepareContext(ctx, "SELECT pg_sleep($1)")
				if err != nil {
					t.Fatal(err)
				}
				defer statement.Close()
			}
			var tx *sql.Tx
			if method == "transaction" {
				tx, err = original.BeginTx(query, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
			}
			completed := make(chan error, 1)
			go func() {
				var rows *sql.Rows
				var err error
				switch method {
				case "query":
					rows, err = original.QueryContext(query, "SELECT pg_sleep(30)")
				case "exec":
					_, err = original.ExecContext(query, "SELECT pg_sleep(30)")
				case "prepared-query":
					rows, err = statement.QueryContext(query, 30)
				case "prepared-exec":
					_, err = statement.ExecContext(query, 30)
				case "transaction":
					_, err = tx.ExecContext(query, "SELECT pg_sleep(30)")
				}
				if rows != nil {
					for rows.Next() {
					}
					if err == nil {
						err = rows.Err()
					}
					rows.Close()
				}
				completed <- err
			}()
			deadline := time.Now().Add(5 * time.Second)
			for {
				var running bool
				if err = monitor.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND state='active' AND query LIKE '%pg_sleep%')", pid).Scan(&running); err != nil {
					t.Fatal(err)
				}
				if running {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("native query did not start")
				}
				time.Sleep(5 * time.Millisecond)
			}
			cancel()
			select {
			case err = <-completed:
				if err == nil {
					t.Fatal("cancelled native query reported success")
				}
			case <-time.After(12 * time.Second):
				t.Fatal("native cancellation did not acknowledge cleanup")
			}
			if tx != nil {
				_ = tx.Rollback()
			}
			if _, err = original.ExecContext(ctx, "SELECT 1"); err == nil {
				t.Fatal("cancelled physical session silently recovered")
			}
			var active bool
			if err = monitor.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND state='active' AND query LIKE '%pg_sleep%')", pid).Scan(&active); err != nil || active {
				t.Fatal("cancellation returned with original query still active", err)
			}
			original.Close()
			recovery, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer recovery.Close()
			var replacement int
			if err = recovery.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&replacement); err != nil || replacement == pid {
				t.Fatal("explicit recovery reused cancelled physical session", err)
			}
		})
	}
}

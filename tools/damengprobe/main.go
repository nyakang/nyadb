// Command damengprobe audits an unmodified external driver against an owned
// disposable DM8 fixture. It is not linked into or shipped with the workbench.
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"time"

	_ "gitee.com/chunanyong/dm"
)

type config struct {
	Endpoint     string `json:"endpoint"`
	PasswordFile string `json:"password_file"`
	Output       string `json:"output"`
}

type report struct {
	Driver                    string   `json:"driver"`
	Server                    []string `json:"server"`
	ExactValues               bool     `json:"exact_values"`
	ExpectedDecimal           string   `json:"expected_decimal"`
	ObservedBoundDecimal      string   `json:"observed_bound_decimal"`
	ObservedTextCastDecimal   string   `json:"observed_text_cast_decimal"`
	Rollback                  bool     `json:"rollback"`
	Commit                    bool     `json:"commit"`
	UnknownWriteAttemptError  bool     `json:"unknown_write_attempt_error"`
	UnknownWriteCommittedRows int      `json:"unknown_write_committed_rows"`
	NoSwitchCommittedRows     int      `json:"no_switch_committed_rows"`
	SafeForWorkbench          bool     `json:"safe_for_workbench"`
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: damengprobe private-config.json")
		os.Exit(2)
	}
	if err := run(os.Args[1]); err != nil {
		// Do not print driver diagnostics: authentication errors can include DSNs.
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func open(endpoint, password string, disableSwitch bool) (*sql.DB, error) {
	props := url.Values{"escapeProcess": {"true"}, "dec2Double": {"false"}, "driverReconnect": {"false"}, "rwSeparate": {"0"}, "svcConfPath": {os.DevNull}, "logLevel": {"off"}, "socketTimeout": {"10"}}
	if disableSwitch {
		props.Set("doSwitch", "0")
	}
	db, err := sql.Open("dm", "dm://SYSDBA:"+url.PathEscape(password)+"@"+endpoint+"?"+props.Encode())
	if err == nil {
		db.SetMaxOpenConns(1)
	}
	return db, err
}

func run(path string) (runErr error) {
	stage := "private configuration"
	defer func() {
		if runErr != nil {
			runErr = fmt.Errorf("DM8 isolated driver audit failed at %s; inspect the owned fixture", stage)
		}
	}()
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var cfg config
	if err = json.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	password, err := os.ReadFile(cfg.PasswordFile)
	if err != nil {
		return err
	}
	stage = "authentication"
	db, err := open(cfg.Endpoint, string(password), true)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err = db.PingContext(ctx); err != nil {
		return err
	}
	stage = "server identity"
	r := report{Driver: "gitee.com/chunanyong/dm v1.8.23 (unmodified external research dependency)"}
	rows, err := db.QueryContext(ctx, "SELECT BANNER FROM V$VERSION")
	if err != nil {
		return err
	}
	for rows.Next() {
		var version string
		if err = rows.Scan(&version); err != nil {
			rows.Close()
			return err
		}
		r.Server = append(r.Server, version)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	stage = "bound exact values"
	_, err = db.ExecContext(ctx, "CREATE TABLE NYADB_DRIVER_AUDIT (ID BIGINT, N DECIMAL(38,20), T VARCHAR(200), B VARBINARY(200), Z VARCHAR(20), E VARCHAR(20))")
	if err != nil {
		return err
	}
	defer db.ExecContext(context.Background(), "DROP TABLE NYADB_DRIVER_AUDIT")
	stage = "parameter binding"
	const number = "123456789012345678.12345678901234567891"
	text := "数据库 ' \\ 🌸"
	binary := []byte{0, 1, 128, 255}
	if _, err = db.ExecContext(ctx, "INSERT INTO NYADB_DRIVER_AUDIT VALUES (?, ?, ?, ?, ?, ?)", int64(9007199254740993), number, text, binary, nil, ""); err != nil {
		return err
	}
	var id int64
	var n, value, empty string
	var b []byte
	var null sql.NullString
	stage = "bound result reading"
	if err = db.QueryRowContext(ctx, "SELECT ID,N,T,B,Z,E FROM NYADB_DRIVER_AUDIT").Scan(&id, &n, &value, &b, &null, &empty); err != nil {
		return err
	}
	r.ExactValues = id == 9007199254740993 && n == number && value == text && bytes.Equal(b, binary) && !null.Valid && empty == ""
	r.ExpectedDecimal, r.ObservedBoundDecimal = number, n
	if id != 9007199254740993 || value != text || !bytes.Equal(b, binary) || null.Valid || empty != "" {
		return errors.New("value mismatch")
	}
	stage = "explicit text-to-decimal binding"
	if _, err = db.ExecContext(ctx, "UPDATE NYADB_DRIVER_AUDIT SET N=CAST(CAST(? AS VARCHAR(100)) AS DECIMAL(38,20))", number); err != nil {
		return err
	}
	if err = db.QueryRowContext(ctx, "SELECT N FROM NYADB_DRIVER_AUDIT").Scan(&r.ObservedTextCastDecimal); err != nil {
		return err
	}
	count := func() (int, error) {
		var total int
		err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM NYADB_DRIVER_AUDIT").Scan(&total)
		return total, err
	}
	stage = "transactions"
	for _, commit := range []bool{false, true} {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO NYADB_DRIVER_AUDIT (ID) VALUES (?)", int64(42)); err != nil {
			tx.Rollback()
			return err
		}
		if commit {
			err = tx.Commit()
		} else {
			err = tx.Rollback()
		}
		if err != nil {
			return err
		}
		total, err := count()
		if err != nil {
			return err
		}
		if commit {
			r.Commit = total == 2
		} else {
			r.Rollback = total == 1
		}
	}
	stage = "lost write reply"
	if _, err = db.ExecContext(ctx, "CREATE TABLE NYADB_UNKNOWN_WRITE (ID INT)"); err != nil {
		return err
	}
	defer db.ExecContext(context.Background(), "DROP TABLE NYADB_UNKNOWN_WRITE")
	proxy, err := newDropProxy(cfg.Endpoint)
	if err != nil {
		return err
	}
	defer proxy.Close()
	pool, err := open(proxy.listener.Addr().String(), string(password), false)
	if err != nil {
		return err
	}
	defer pool.Close()
	_, err = pool.ExecContext(ctx, "INSERT INTO NYADB_UNKNOWN_WRITE VALUES (1)")
	r.UnknownWriteAttemptError = err != nil
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM NYADB_UNKNOWN_WRITE").Scan(&r.UnknownWriteCommittedRows); err != nil {
		return err
	}
	// The observation must reproduce the defect. This is an audit, not a test
	// that blesses the candidate driver for integration.
	if !r.UnknownWriteAttemptError || r.UnknownWriteCommittedRows != 3 {
		stage = fmt.Sprintf("lost write reply (error=%t committed_rows=%d)", r.UnknownWriteAttemptError, r.UnknownWriteCommittedRows)
		return errors.New("unexpected lost-response behavior; reassess driver")
	}
	if _, err = db.ExecContext(ctx, "DELETE FROM NYADB_UNKNOWN_WRITE"); err != nil {
		return err
	}
	noSwitch, err := open(proxy.listener.Addr().String(), string(password), true)
	if err != nil {
		return err
	}
	defer noSwitch.Close()
	if _, err = noSwitch.ExecContext(ctx, "INSERT INTO NYADB_UNKNOWN_WRITE VALUES (1)"); err == nil {
		return errors.New("lost reply unexpectedly succeeded")
	}
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM NYADB_UNKNOWN_WRITE").Scan(&r.NoSwitchCommittedRows); err != nil {
		return err
	}
	if r.NoSwitchCommittedRows != 1 {
		return errors.New("explicit no-switch still replays")
	}
	stage = "report output"
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cfg.Output, append(data, '\n'), 0o600)
}

// dropProxy forwards the complete write to the real server and discards the
// reply. It uses no protocol modification and never inspects credentials.
type dropProxy struct {
	listener net.Listener
	mu       sync.Mutex
	sockets  []net.Conn
	wg       sync.WaitGroup
	closed   bool
}

func newDropProxy(endpoint string) (*dropProxy, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &dropProxy{listener: listener}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			if p.closed {
				p.mu.Unlock()
				client.Close()
				return
			}
			p.sockets = append(p.sockets, client)
			p.wg.Add(1)
			p.mu.Unlock()
			go p.serve(client, endpoint)
		}
	}()
	return p, nil
}

func (p *dropProxy) serve(client net.Conn, endpoint string) {
	defer p.wg.Done()
	defer client.Close()
	server, err := net.DialTimeout("tcp", endpoint, 5*time.Second)
	if err != nil {
		return
	}
	defer server.Close()
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.sockets = append(p.sockets, server)
	p.mu.Unlock()
	var drop atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer client.Close()
		defer server.Close()
		buffer := make([]byte, 64*1024)
		for {
			n, err := server.Read(buffer)
			if n > 0 {
				if drop.Load() {
					return
				}
				if _, err = io.Copy(client, bytes.NewReader(buffer[:n])); err != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	buffer := make([]byte, 64*1024)
	var tail []byte
	for {
		n, err := client.Read(buffer)
		if n > 0 {
			// Keep only a short boundary suffix. No payload is logged or saved.
			combined := append(tail, buffer[:n]...)
			if bytes.Contains(combined, []byte("INSERT INTO NYADB_UNKNOWN_WRITE")) {
				drop.Store(true)
			}
			tail = append([]byte(nil), combined[max(0, len(combined)-64):]...)
			if _, err = io.Copy(server, bytes.NewReader(buffer[:n])); err != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}
	server.Close()
	client.Close()
	<-done
}

func (p *dropProxy) Close() {
	p.mu.Lock()
	p.closed = true
	p.listener.Close()
	for _, socket := range p.sockets {
		socket.Close()
	}
	p.mu.Unlock()
	p.wg.Wait()
}

// Command tdengine_fixture_probe checks an explicitly supplied, owned fixture.
// It is an isolated native protocol verifier, not a public NyaDB provider.
package main

import (
	"crypto/tls"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/taosdata/driver-go/v3/ws/unified"
)

type fixtureConfig struct {
	Endpoint, Username, PasswordFile, DatabasePrefix, Version string
}

func loadConfig(path string) (fixtureConfig, string, error) {
	var cfg fixtureConfig
	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg, "", err
	}
	if err = json.Unmarshal(raw, &cfg); err != nil {
		return cfg, "", err
	}
	secret, err := os.ReadFile(cfg.PasswordFile)
	return cfg, strings.TrimSpace(string(secret)), err
}

func connect(cfg fixtureConfig, password string) (*unified.Client, error) {
	c := unified.NewConfig([]string{cfg.Endpoint})
	c.User, c.Passwd = cfg.Username, password
	c.AutoReconnect, c.AdapterHA = false, false
	c.ReadTimeout, c.WriteTimeout = 3*time.Second, 3*time.Second
	c.Timezone = time.UTC
	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	client, err := unified.NewClient(c, "/ws", unified.WithDialFunc(func(endpoint string) (*websocket.Conn, error) {
		conn, _, e := dialer.Dial(endpoint, nil)
		if e == nil {
			// The connector's initial version handshake also needs a bound.
			e = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		}
		return conn, e
	}))
	if err != nil {
		return nil, err
	}
	if err = client.Connect(); err != nil {
		client.Close()
		return nil, err
	}
	return client, nil
}

type column struct {
	Name, Type string
}

type cell struct {
	Null     bool   `json:"null,omitempty"`
	Text     string `json:"text,omitempty"`
	Binary   string `json:"base64,omitempty"`
	UnixNano string `json:"unix_nanoseconds,omitempty"`
}

type result struct {
	Columns []column `json:"columns"`
	Rows    [][]cell `json:"rows"`
}

// Values stay typed by their owning column. No JSON float conversion is used
// for integer, decimal, binary or timestamp values.
func encodeCell(value driver.Value, typ string) (cell, error) {
	if value == nil {
		return cell{Null: true}, nil
	}
	c := cell{}
	switch v := value.(type) {
	case string:
		c.Text = v
	case []byte:
		if typ == "VARBINARY" || typ == "GEOMETRY" || typ == "BLOB" || typ == "MEDIUMBLOB" {
			c.Binary = base64.StdEncoding.EncodeToString(v)
		} else {
			c.Text = string(v)
		}
	case time.Time:
		c.Text = v.UTC().Format(time.RFC3339Nano)
		c.UnixNano = strconv.FormatInt(v.UnixNano(), 10)
	case bool:
		c.Text = strconv.FormatBool(v)
	case float32:
		c.Text = strconv.FormatFloat(float64(v), 'g', -1, 32)
	case float64:
		c.Text = strconv.FormatFloat(v, 'g', -1, 64)
	case int8, int16, int32, int64, int, uint8, uint16, uint32, uint64, uint:
		c.Text = fmt.Sprint(v)
	case fmt.Stringer:
		c.Text = v.String()
	default:
		return c, fmt.Errorf("unhandled native value type %T for %s", value, typ)
	}
	return c, nil
}

func query(c *unified.Client, sql string) (result, error) {
	out := result{Rows: [][]cell{}}
	r, err := c.Query(0, sql)
	if err != nil {
		return out, err
	}
	if r == nil {
		return out, errors.New("query verifier expected a result handle")
	}
	defer r.Close()
	for i, n := range r.Columns() {
		out.Columns = append(out.Columns, column{n, r.ColumnTypeDatabaseTypeName(i)})
	}
	values := make([]driver.Value, len(out.Columns))
	for {
		if err = r.Next(values); err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return out, err
		}
		row := make([]cell, len(values))
		for i, v := range values {
			if row[i], err = encodeCell(v, out.Columns[i].Type); err != nil {
				return out, err
			}
		}
		out.Rows = append(out.Rows, row)
	}
}

func main() {
	path := flag.String("config", "", "private owned-fixture configuration")
	flag.Parse()
	cfg, password, err := loadConfig(*path)
	if err == nil {
		var c *unified.Client
		c, err = connect(cfg, password)
		if err == nil {
			defer c.Close()
			var r result
			r, err = query(c, "SELECT server_version()")
			if err == nil && (len(r.Rows) != 1 || len(r.Rows[0]) != 1 || r.Rows[0][0].Text != cfg.Version) {
				err = errors.New("actual native version does not match the pinned fixture")
			}
			if err == nil {
				_ = json.NewEncoder(os.Stdout).Encode(r)
			}
		}
	}
	if err != nil {
		message := err.Error()
		if password != "" {
			message = strings.ReplaceAll(message, password, "[redacted]")
		}
		fmt.Fprintln(os.Stderr, message)
		os.Exit(1)
	}
}

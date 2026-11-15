package tdengine

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type wire struct {
	conn *websocket.Conn
	seq  uint64
	once sync.Once
}

type serverError struct {
	Code    int
	Message string
}

func (e *serverError) Error() string {
	return fmt.Sprintf("TDengine server error [%x]: %s", e.Code, e.Message)
}

type nativeResponse struct {
	Code      int      `json:"code"`
	Message   string   `json:"message"`
	Action    string   `json:"action"`
	Request   uint64   `json:"req_id"`
	ID        uint64   `json:"id"`
	Update    bool     `json:"is_update"`
	Affected  int64    `json:"affected_rows"`
	Fields    int      `json:"fields_count"`
	Names     []string `json:"fields_names"`
	Types     []byte   `json:"fields_types"`
	Lengths   []int64  `json:"fields_lengths"`
	Precision int      `json:"precision"`
}

func openWire(ctx context.Context, p connections.Profile, password string) (*wire, error) {
	u := url.URL{Scheme: "ws", Host: net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))), Path: "/ws"}
	d := websocket.Dialer{HandshakeTimeout: time.Duration(p.TDengineOptions.ConnectTimeoutSeconds) * time.Second}
	if p.TLSMode != "disable" {
		u.Scheme = "wss"
		d.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: p.Host}
		if p.TDengineOptions.CAFile != "" {
			raw, err := os.ReadFile(p.TDengineOptions.CAFile)
			if err != nil {
				return nil, connections.ErrInvalid
			}
			pool, err := x509.SystemCertPool()
			if err != nil {
				pool = x509.NewCertPool()
			}
			if !pool.AppendCertsFromPEM(raw) {
				return nil, connections.ErrInvalid
			}
			d.TLSClientConfig.RootCAs = pool
		}
	}
	c, response, err := d.DialContext(ctx, u.String(), nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return nil, err
	}
	w := &wire{conn: c}
	c.SetReadLimit(32 << 20)
	_, err = w.text(ctx, "conn", map[string]any{"user": p.Username, "password": password, "db": p.Database, "tz": "UTC", "app": "NyaDB"})
	if err != nil {
		w.close()
		return nil, err
	}
	return w, nil
}

func (w *wire) close() { w.once.Do(func() { _ = w.conn.Close() }) }

// Cancellation closes only this captured physical socket and remains pending
// until the original reader/writer returns. No request reconnects or retries.
func (w *wire) scope(ctx context.Context) (func(), error) {
	if ctx == nil {
		return nil, connections.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(60 * time.Second)
	}
	if err := w.conn.SetReadDeadline(deadline); err != nil {
		return nil, err
	}
	if err := w.conn.SetWriteDeadline(deadline); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { w.close(); close(done) })
	return func() {
		if !stop() {
			<-done
		}
	}, nil
}

func (w *wire) text(ctx context.Context, action string, args map[string]any) ([]byte, error) {
	done, err := w.scope(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	w.seq++
	args["req_id"] = w.seq
	raw, err := json.Marshal(map[string]any{"action": action, "args": args})
	if err != nil {
		return nil, err
	}
	if err = w.conn.WriteMessage(websocket.TextMessage, raw); err != nil {
		return nil, err
	}
	kind, raw, err := w.conn.ReadMessage()
	if err != nil {
		return nil, err
	}
	var response nativeResponse
	if kind != websocket.TextMessage || json.Unmarshal(raw, &response) != nil || response.Request != w.seq || response.Action != action {
		return nil, contract.ErrUnsupported
	}
	if response.Code != 0 {
		return raw, &serverError{response.Code, response.Message}
	}
	return raw, nil
}

func (w *wire) query(ctx context.Context, sql string, sink func([]contract.Column, []contract.Cell) error, budget *int) (set contract.ResultSet, receipt json.RawMessage, err error) {
	done, err := w.scope(ctx)
	if err != nil {
		return set, nil, err
	}
	defer func() {
		done()
		if err != nil && ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	w.seq++
	req := make([]byte, 30+len(sql))
	binary.LittleEndian.PutUint64(req, w.seq)
	binary.LittleEndian.PutUint64(req[16:], 6)
	binary.LittleEndian.PutUint16(req[24:], 1)
	binary.LittleEndian.PutUint32(req[26:], uint32(len(sql)))
	copy(req[30:], sql)
	if err = w.conn.WriteMessage(websocket.BinaryMessage, req); err != nil {
		return set, nil, err
	}
	kind, raw, err := w.conn.ReadMessage()
	if err != nil {
		return set, nil, err
	}
	var response nativeResponse
	if kind != websocket.TextMessage || json.Unmarshal(raw, &response) != nil || response.Request != w.seq || response.Action != "binary_query" {
		return set, nil, contract.ErrUnsupported
	}
	receipt = append(json.RawMessage(nil), raw...)
	if response.Code != 0 {
		return set, receipt, &serverError{response.Code, response.Message}
	}
	if response.Update {
		if response.Affected < 0 {
			return set, receipt, contract.ErrUnsupported
		}
		set.AffectedRows = response.Affected
		return set, receipt, nil
	}
	if response.Fields < 1 || response.Fields > 512 || len(response.Names) != response.Fields || len(response.Types) != response.Fields || len(response.Lengths) != response.Fields || response.ID == 0 {
		return set, receipt, contract.ErrUnsupported
	}
	for i, name := range response.Names {
		if !utf8.ValidString(name) || response.Lengths[i] < 0 {
			return set, receipt, contract.ErrUnsupported
		}
		typ, e := typeName(response.Types[i])
		if e != nil {
			return set, receipt, e
		}
		set.Columns = append(set.Columns, contract.Column{Name: name, DatabaseType: typ, Nullable: true})
	}
	flags := map[string]bool{}
	for {
		if err = ctx.Err(); err != nil {
			return set, receipt, err
		}
		w.seq++
		request := make([]byte, 26)
		binary.LittleEndian.PutUint64(request, w.seq)
		binary.LittleEndian.PutUint64(request[8:], response.ID)
		binary.LittleEndian.PutUint64(request[16:], 7)
		binary.LittleEndian.PutUint16(request[24:], 1)
		if err = w.conn.WriteMessage(websocket.BinaryMessage, request); err != nil {
			return set, receipt, err
		}
		kind, raw, err = w.conn.ReadMessage()
		if err != nil {
			return set, receipt, err
		}
		if kind != websocket.BinaryMessage || len(raw) < 51 || binary.LittleEndian.Uint16(raw[16:]) != 1 || binary.LittleEndian.Uint64(raw[26:]) != w.seq {
			return set, receipt, contract.ErrUnsupported
		}
		msgLen := int(binary.LittleEndian.Uint32(raw[38:]))
		if msgLen > len(raw)-51 {
			return set, receipt, contract.ErrUnsupported
		}
		if binary.LittleEndian.Uint64(raw[42+msgLen:]) != response.ID {
			return set, receipt, contract.ErrUnsupported
		}
		if code := int(binary.LittleEndian.Uint32(raw[34:])); code != 0 {
			return set, receipt, &serverError{code, string(raw[42 : 42+msgLen])}
		}
		if raw[50+msgLen] == 1 {
			break
		}
		if raw[50+msgLen] != 0 || len(raw) < 55+msgLen {
			return set, receipt, contract.ErrUnsupported
		}
		length := int(binary.LittleEndian.Uint32(raw[51+msgLen:]))
		if length != len(raw)-55-msgLen {
			return set, receipt, contract.ErrUnsupported
		}
		rows, e := decodeBlock(raw[55+msgLen:], response.Types, response.Precision)
		if e != nil {
			return set, receipt, e
		}
		for _, row := range rows {
			if sink != nil {
				streamRow := make([]contract.Cell, len(row))
				for i := range row {
					streamRow[i] = row[i].Clone()
				}
				if e = sink(append([]contract.Column(nil), set.Columns...), streamRow); e != nil {
					w.close()
					return set, receipt, e
				}
			}
			if budget == nil {
				set.Rows = append(set.Rows, row)
				continue
			}
			if len(set.Rows) >= 10000 {
				flags["rows"] = true
				continue
			}
			size := 64 * len(row)
			for i := range row {
				if len(row[i].Text) > 4096 {
					n := 4096
					for n > 0 && !utf8.ValidString(row[i].Text[:n]) {
						n--
					}
					row[i].Text = strings.Clone(row[i].Text[:n])
					row[i].Truncated = true
				}
				if len(row[i].Bytes) > 4096 {
					row[i].Bytes = append([]byte(nil), row[i].Bytes[:4096]...)
					row[i].Truncated = true
				}
				if row[i].Truncated {
					flags["cell"] = true
				}
				size += len(row[i].Text) + len(row[i].Bytes)
			}
			if size > *budget {
				flags["bytes"] = true
				continue
			}
			*budget -= size
			set.Rows = append(set.Rows, row)
		}
	}
	for _, flag := range []string{"rows", "bytes", "cell"} {
		if flags[flag] {
			set.Truncation = append(set.Truncation, flag)
		}
	}
	return set, receipt, nil
}

func safeError(err error, password string) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{contract.ErrRevoked, contract.ErrSQLSessionLost, contract.ErrUnsupported, connections.ErrInvalid, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, known) {
			return known
		}
	}
	var e *serverError
	if errors.As(err, &e) {
		message := e.Message
		if password != "" {
			message = strings.ReplaceAll(message, password, "[redacted]")
		}
		if len(message) > 512 {
			message = message[:512]
		}
		return &serverError{e.Code, message}
	}
	return errors.New("TDengine connection or operation failed")
}

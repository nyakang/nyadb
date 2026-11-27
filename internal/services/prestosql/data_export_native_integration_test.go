package prestosql

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/jdbcplugin"
)

type nativeWriteFunc func([]byte) (int, error)

func (f nativeWriteFunc) Write(p []byte) (int, error) { return f(p) }

func TestPrestoSQLPrivateExportNativeCompleteCursorAndConsumerStop(t *testing.T) {
	f, p, c, e := nativeProvider(t)
	s, q, _ := nativeDataSession(t, p, c)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	const name = "export66"
	queryNative(t, q, "", `CREATE TABLE memory.default.export66 AS SELECT CAST(n AS BIGINT) AS id, IF(n=1,rpad('',2049,'猫'),'row'||CAST(n AS VARCHAR)) AS note, IF(n=1,concat(X'00ff80',to_utf8(rpad('',2046,'x'))),CAST(NULL AS VARBINARY)) AS payload FROM UNNEST(concat(sequence(1,10000),sequence(10001,10005))) t(n)`)
	l, r, label := nativeDataDocument(t, s, name)
	page, err := r.Loader(ctx, 65, label, name, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if page.RowRefs == nil || len(page.RowRefs) != 2 {
		t.Fatal("original row observations missing")
	}
	count := 0
	err = r.Exporter(ctx, 65, label, name, contract.DataOptions{SortColumn: "id"}, []string{"note", "payload", "id"}, func(values []contract.DataValue) error {
		count++
		if len(values) != 3 || values[2].Kind != contract.Integer || values[2].Text != strconv.Itoa(count) {
			return fmt.Errorf("native export order/columns at %d", count)
		}
		if count == 1 {
			if values[0].Text != strings.Repeat("猫", 2049) || !bytes.Equal(values[1].Bytes, append([]byte{0, 255, 128}, bytes.Repeat([]byte{'x'}, 2046)...)) {
				return errors.New("complete exported values were clipped")
			}
			// This later INSERT must not enter the already-open original cursor.
			queryNative(t, q, "", `INSERT INTO memory.default.export66 VALUES (10006,'later',NULL)`)
			values[1].Bytes[0] = 99
		} else if !values[1].Null {
			return errors.New("SQL NULL changed during export")
		}
		return nil
	})
	if err != nil || count != 10005 {
		t.Fatal("single original cursor export", err, count)
	}
	var filtered []string
	err = r.Exporter(ctx, 65, label, name, contract.DataOptions{FilterColumn: "id", FilterMode: "greater", FilterValue: "10000", SortColumn: "id"}, []string{"note"}, func(values []contract.DataValue) error { filtered = append(filtered, values[0].Text); return nil })
	if err != nil || strings.Join(filtered, ",") != "row10001,row10002,row10003,row10004,row10005,later" {
		t.Fatal("filter/order on omitted export field", err, filtered)
	}
	hasher := sha256.New()
	seen := map[string]bool{}
	err = r.JSONL(ctx, l.object, nativeWriteFunc(func(raw []byte) (int, error) {
		var row struct {
			Object  contract.ObjectRef         `json:"object"`
			Columns []contract.StructureColumn `json:"columns"`
			Values  []contract.Cell            `json:"values"`
		}
		if json.Unmarshal(raw, &row) != nil || row.Object != l.object || len(row.Columns) != 3 || len(row.Values) != 3 || row.Values[0].Kind != contract.Integer {
			return 0, errors.New("invalid typed JSONL")
		}
		id := row.Values[0].Text
		if seen[id] {
			return 0, errors.New("replayed JSONL row")
		}
		seen[id] = true
		if id == "1" && (row.Values[1].Text != strings.Repeat("猫", 2049) || len(row.Values[2].Bytes) != 2049 || row.Values[2].Bytes[0] != 0) {
			return 0, errors.New("JSONL lost original values")
		}
		return hasher.Write(raw)
	}))
	if err != nil || len(seen) != 10006 {
		t.Fatal("complete typed JSONL", err, len(seen))
	}
	stop := errors.New("consumer intentionally stopped")
	stopped := 0
	err = r.Exporter(ctx, 65, label, name, contract.DataOptions{SortColumn: "id"}, nil, func([]contract.DataValue) error { stopped++; return stop })
	if !errors.Is(err, stop) || stopped != 1 {
		t.Fatal("consumer stop ignored", err, stopped)
	}
	// Complete exports are not constrained to the editor's 1 MiB value limit.
	queryNative(t, q, "", `CREATE TABLE memory.default.export_large66 AS SELECT rpad('',400000,'猫') AS note, to_utf8(rpad('',1048593,'b')) AS payload`)
	_, large, largeLabel := nativeDataDocument(t, s, "export_large66")
	largeRows := 0
	err = large.Exporter(ctx, 65, largeLabel, "export_large66", contract.DataOptions{}, nil, func(v []contract.DataValue) error {
		largeRows++
		if len(v) != 2 || len(v[0].Text) != 1200000 || len(v[1].Bytes) != 1048593 {
			return errors.New("complete large export clipped")
		}
		return nil
	})
	if err != nil || largeRows != 1 {
		t.Fatal("large complete export", err)
	}
	again, err := r.Loader(ctx, 65, label, name, 0, 2)
	if err != nil || len(again.Rows) != 2 || !s.Alive() {
		t.Fatal("export retired original page", err)
	}
	calls := map[string]int{}
	closed := 0
	e.mu.Lock()
	for _, item := range e.receipts {
		method := item["method"].(string)
		if strings.HasPrefix(method, "data-export-") {
			tail := method[strings.IndexByte(method, '/')+1:]
			calls[tail]++
			if tail == "closeQuerySession" {
				receipt := item["receipt"].(jdbcplugin.Receipt)
				if receipt.ExecutorAcknowledged && string(receipt.Result) == `{"ok":true}` {
					closed++
				}
			}
		}
	}
	e.mu.Unlock()
	if calls["executeQueryPage"] != 5 || closed != 1 {
		t.Fatal("export repeated SQL or failed to close abandoned cursor", calls, closed)
	}
	providerProof(t, f, "data-export-cursor.json", map[string]any{"initial_cursor_rows": count, "later_filtered": filtered, "jsonl_rows": len(seen), "jsonl_sha256": hex.EncodeToString(hasher.Sum(nil)), "consumer_rows": stopped, "cursor_calls": calls, "abandoned_cursors_closed": closed, "large_row_text_bytes": 1200000, "large_row_binary_bytes": 1048593, "original_page_preserved": true})
	queryNative(t, q, "", `DROP TABLE memory.default.export66; DROP TABLE memory.default.export_large66`)
	_ = s.Close()
	if err = p.Store.Remove(ctx, "prestosql", c.DriverVersion); err != nil {
		t.Fatal(err)
	}
}

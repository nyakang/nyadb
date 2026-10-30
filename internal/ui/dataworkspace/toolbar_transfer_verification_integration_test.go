package dataworkspace

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

// These files are reusable inputs for independently operated importers. The
// result is a fixture, not a claim that another application accepted it.
func TestDataTransferVerificationArtifacts(t *testing.T) {
	folder := os.Getenv("NYADB_TRANSFER_VERIFICATION_ARTIFACT_DIR")
	if folder == "" {
		t.Skip("requires an explicit isolated output directory for interoperability fixtures")
	}
	if err := os.MkdirAll(folder, 0700); err != nil {
		t.Fatal(err)
	}
	fields := []string{"id", "note", "payload", "amount", "at"}
	rows := [][]mysqlsvc.MySQLDataValue{
		{{Text: "1"}, {Text: "alpha"}, {Binary: true, Bytes: []byte{0, 255, 128}}, {Text: "9007199254740993.1234567890"}, {Text: "2026-10-30 12:34:56.123456"}},
		{{Text: "2"}, {Text: "中文"}, {Binary: true, Bytes: []byte{}}, {Text: "-0.0000000001"}, {Text: "2026-10-30 16:00:00.000001"}},
		{{Text: "3"}, {Text: ""}, {Null: true}, {Text: "0.0000000000"}, {Null: true}},
		{{Text: "4"}, {Null: true}, {Binary: true, Bytes: []byte{0}}, {Null: true}, {Null: true}},
		{{Text: "5"}, {Text: "line\ntwo"}, {Binary: true, Bytes: []byte{1, 2}}, {Text: "12.5000000000"}, {Null: true}},
		{{Text: "6"}, {Text: "=1+2"}, {Binary: true, Bytes: []byte{255}}, {Text: "-42.7500000000"}, {Text: "2026-10-29 01:02:03.456789"}},
	}
	for _, format := range mysqlDataTransferFormatChoices("export") {
		path := filepath.Join(folder, "nyadb-six-rows."+strings.ToLower(format))
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		encoder, err := newMySQLDataTransferEncoder(file, format, "toolbar_verification", "transfer_six_rows", fields)
		if err == nil {
			for _, row := range rows {
				if err = encoder.Row(row); err != nil {
					break
				}
			}
		}
		if err == nil {
			err = encoder.Close()
		}
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("%s: encode=%v close=%v", format, err, closeErr)
		}
		t.Logf("fixture %s", path)
	}
	manifest, err := json.MarshalIndent(struct {
		Fields []string
		Rows   [][]mysqlsvc.MySQLDataValue
	}{fields, rows}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(folder, "nyadb-six-rows-values.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
}

type verificationScaleRows struct {
	count, total int
	note         string
}

func (r *verificationScaleRows) Fields() []string { return []string{"id", "note"} }
func (r *verificationScaleRows) Close() error     { return nil }
func (r *verificationScaleRows) Next(ctx context.Context) ([]mysqlsvc.MySQLDataValue, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.count == r.total {
		return nil, io.EOF
	}
	r.count++
	return []mysqlsvc.MySQLDataValue{{Text: strconv.Itoa(r.count)}, {Text: r.note}}, nil
}

// Run each size in a separate test process so external RSS samples measure a
// comparable cold process rather than retained heap from a previous size.
func TestDataTransferScaleVerification(t *testing.T) {
	count, err := strconv.Atoi(os.Getenv("NYADB_TRANSFER_SCALE_ROWS"))
	if err != nil || count < 1 {
		t.Skip("requires an explicit synthetic source row count for process memory measurement")
	}
	note := strings.Repeat("0123456789abcdef", 8)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	spool, preview, _, err := mysqlDataSpoolRows(context.Background(), &verificationScaleRows{total: count, note: note})
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	if spool.rows != int64(count) || len(preview.Rows) != min(100, count) {
		t.Fatal("source was capped or preview retained all rows", spool.rows, len(preview.Rows))
	}
	info, err := os.Stat(spool.path)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := spool.open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	for i := 1; i <= count; i++ {
		row, err := reader.Next(context.Background())
		if err != nil || row[0].Text != strconv.Itoa(i) || row[1].Text != note {
			t.Fatalf("row %d changed: %v", i, err)
		}
	}
	if _, err = reader.Next(context.Background()); err != io.EOF {
		t.Fatal(err)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	// Keep the cached preview live across the measurement.
	runtime.KeepAlive(preview)
	retained := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	if retained > 8<<20 {
		t.Fatalf("retained input memory exceeded bounded preview allowance: %d", retained)
	}
	result := fmt.Sprintf(`{"rows":%d,"source_value_bytes":%d,"spool_bytes":%d,"preview_rows":%d,"retained_heap_delta_bytes":%d}`, count, int64(count)*int64(len(note)), info.Size(), len(preview.Rows), retained)
	t.Log(result)
}

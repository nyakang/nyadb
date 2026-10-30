package dataworkspace

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func TestDataTransferAdditionalFormatsPreserveValues(t *testing.T) {
	fields := []string{"id", "note", "payload", "amount"}
	rows := [][]mysqlsvc.MySQLDataValue{
		{{Text: "9007199254740993"}, {Text: "中文\r\n=1+1 trailing "}, {Binary: true, Bytes: []byte{0, 255, 128, 0}}, {Text: "9007199254740993.1234567890"}},
		{{Text: "2"}, {Text: ""}, {Binary: true, Bytes: []byte{}}, {Text: "-0.0000000001"}},
		{{Text: "3"}, {Null: true}, {Null: true}, {Null: true}},
	}
	for _, format := range []string{"XLS", "DBF", "Parquet"} {
		t.Run(format, func(t *testing.T) {
			var out bytes.Buffer
			e, err := newMySQLDataTransferEncoder(&out, format, "db", "t", fields)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				if err = e.Row(row); err != nil {
					t.Fatal(err)
				}
			}
			if err = e.Close(); err != nil {
				t.Fatal(err)
			}
			input, err := mysqlDataParseImport(bytes.NewReader(out.Bytes()), format)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(input.Fields, fields) || len(input.Rows) != len(rows) {
				t.Fatalf("fields/rows: %+v", input)
			}
			for i, row := range rows {
				for j, want := range row {
					got := input.Rows[i][j]
					if got.Text != want.Text || got.Null != want.Null || got.Binary != want.Binary || !bytes.Equal(got.Bytes, want.Bytes) {
						t.Fatalf("%d/%d: %+v != %+v", i, j, got, want)
					}
				}
			}
		})
	}
}

func TestDataTransferXLSSharedStringsContinuationAndLimits(t *testing.T) {
	var out bytes.Buffer
	e, err := newMySQLDataXLSEncoder(&out, []string{"note"})
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("中文🙂", 5000) + "_x0000_\r\n"
	if err = e.Row([]mysqlsvc.MySQLDataValue{{Text: text}}); err != nil {
		t.Fatal(err)
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	input, err := mysqlDataParseXLSImport(out.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if input.Rows[0][0].Text != text {
		t.Fatal("continued SST string changed")
	}
	e, err = newMySQLDataXLSEncoder(io.Discard, []string{"note"})
	if err != nil {
		t.Fatal(err)
	}
	defer e.spool.close()
	if err = e.Row([]mysqlsvc.MySQLDataValue{{Text: strings.Repeat("a", 32768)}}); err == nil {
		t.Fatal("overlong XLS string accepted")
	}
}

func TestDataTransferDBFLimitsNamesAndCancellation(t *testing.T) {
	for _, fields := range [][]string{{"elevenchars"}, {"é"}, {"note", "NOTE"}, {"_NullFlags"}} {
		if e, err := newMySQLDataDBFEncoder(io.Discard, fields); err == nil {
			e.Abort()
			t.Fatal("invalid DBF field name accepted", fields)
		}
	}
	e, err := newMySQLDataDBFEncoder(io.Discard, []string{"note"})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Abort()
	if err = e.Row([]mysqlsvc.MySQLDataValue{{Text: strings.Repeat("a", 255)}}); err == nil {
		t.Fatal("memo-sized field accepted")
	}
	e, err = newMySQLDataDBFEncoder(io.Discard, []string{"note"})
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Row([]mysqlsvc.MySQLDataValue{{Text: "x"}}); err != nil {
		t.Fatal(err)
	}
	name := e.spool.file.Name()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = e.CloseContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = os.Stat(name); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled codec retained staging file", err)
	}
}

func TestDataTransferAdditionalReadersUseExternalFixtures(t *testing.T) {
	for _, format := range []string{"XLS", "DBF", "Parquet"} {
		t.Run(format, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "transfer_formats", "external."+strings.ToLower(format)))
			if err != nil {
				t.Fatal(err)
			}
			input, err := mysqlDataParseImport(bytes.NewReader(data), format)
			if err != nil {
				t.Fatal(err)
			}
			if len(input.Rows) != 2 {
				t.Fatal(input)
			}
			if input.Rows[0][0].Text != "9007199254740993" {
				t.Fatal("external BIGINT text changed", input.Rows[0][0])
			}
			if format != "DBF" && input.Rows[0][1].Text != "中文\nline" {
				t.Fatal("external Unicode changed", input.Rows[0][1])
			}
			if input.Rows[0][2].Text != "12.5000000000" {
				t.Fatal("external DECIMAL changed", input.Rows[0][2])
			}
		})
	}
}

func TestDataTransferAdditionalExternalReadersIntegration(t *testing.T) {
	if os.Getenv("NYADB_TRANSFER_FORMAT_IT") != "1" {
		t.Skip("requires independent Python xlrd/pyarrow/dbf packages in isolated research directory")
	}
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	folder := t.TempDir()
	fields := []string{"id", "note", "payload", "amount"}
	for _, format := range []string{"XLS", "DBF", "Parquet"} {
		f, err := os.Create(filepath.Join(folder, "nyadb."+strings.ToLower(format)))
		if err != nil {
			t.Fatal(err)
		}
		e, err := newMySQLDataTransferEncoder(f, format, "db", "t", fields)
		if err != nil {
			f.Close()
			t.Fatal(err)
		}
		row := []mysqlsvc.MySQLDataValue{{Text: "9007199254740993"}, {Text: "中文\nline "}, {Binary: true, Bytes: []byte{0, 255, 128, 0}}, {Text: "12.5000000000"}}
		if err = e.Row(row); err == nil {
			err = e.Row([]mysqlsvc.MySQLDataValue{{Text: "2"}, {Text: ""}, {Binary: true, Bytes: []byte{}}, {Null: true}})
		}
		if err == nil {
			err = e.Close()
		}
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	script := `import sys
sys.path.insert(0,sys.argv[1])
import xlrd,pyarrow.parquet as pq,dbf
from pathlib import Path
p=Path(sys.argv[2])
b=xlrd.open_workbook(p/'nyadb.xls');s=b.sheet_by_index(0)
assert s.cell_value(1,0)=='9007199254740993'
assert s.cell_value(1,1)=='中文\nline '
assert s.cell_value(1,2)=='0x00ff8000'
assert s.cell_value(1,3)=='12.5000000000'
t=pq.read_table(p/'nyadb.parquet').to_pydict()
assert t['id'][0]=='9007199254740993' and t['payload']==[b'\x00\xff\x80\x00',b'']
assert t['note']==['中文\nline ',''] and t['amount']==['12.5000000000',None]
print('xlrd and pyarrow actual external reads PASS')
`
	cmd := exec.Command("python3", "-c", script, filepath.Join(root, "temp", "research", "transfer-codecs-python"), folder)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("independent reader: %v\n%s", err, output)
	}
	t.Log(string(output))
	if os.Getenv("NYADB_TRANSFER_DBF_PROBE_IT") == "1" {
		probe := exec.Command("go", "run", ".", filepath.Join(folder, "nyadb.dbf"))
		probe.Dir = filepath.Join(root, "temp", "research", "transfer-dbf-probe")
		probe.Env = append(os.Environ(), "GOWORK=off")
		output, err := probe.CombinedOutput()
		if err != nil {
			t.Fatalf("independent DBF reader: %v\n%s", err, output)
		}
		t.Log(string(output))
	}
	if artifact := os.Getenv("NYADB_TRANSFER_FORMAT_ARTIFACT_DIR"); artifact != "" {
		if err := os.MkdirAll(artifact, 0700); err != nil {
			t.Fatal(err)
		}
		for _, format := range []string{"xls", "dbf", "parquet"} {
			data, err := os.ReadFile(filepath.Join(folder, "nyadb."+format))
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(artifact, "nyadb-preserve."+format), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestDataTransferDBFSelectedEncodingsRejectReplacement(t *testing.T) {
	fixture := func(raw []byte, code byte) []byte {
		header := make([]byte, 65)
		header[0], header[29] = 3, code
		binary.LittleEndian.PutUint32(header[4:], 1)
		binary.LittleEndian.PutUint16(header[8:], 65)
		binary.LittleEndian.PutUint16(header[10:], uint16(len(raw)+1))
		copy(header[32:], "note")
		header[43], header[48], header[64] = 'C', byte(len(raw)), 0x0d
		return append(append(header, ' '), raw...)
	}
	for _, test := range []struct {
		encoding, want string
		raw            []byte
		code           byte
	}{
		{"", "é", []byte{0xe9}, 3},
		{"Windows-1252", "é", []byte{0xe9}, 0},
		{"GB18030", "中文", []byte{0xd6, 0xd0, 0xce, 0xc4}, 0},
		{"Shift_JIS", "あ", []byte{0x82, 0xa0}, 0},
		{"UTF-16LE", "中文", []byte{0x2d, 0x4e, 0x87, 0x65}, 0},
		{"UTF-16BE", "中文", []byte{0x4e, 0x2d, 0x65, 0x87}, 0},
		{"UTF-16LE", "A", []byte{0x41, 0}, 0},
	} {
		t.Run(test.encoding, func(t *testing.T) {
			r, err := mysqlDataOpenDBFRowsWithEncoding(bytes.NewReader(fixture(test.raw, test.code)), test.encoding)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			row, err := r.Next(context.Background())
			if err != nil || row[0].Text != test.want {
				t.Fatal(row, err)
			}
		})
	}
	for _, test := range []struct {
		encoding string
		raw      []byte
	}{
		{"UTF-8", []byte{0xff}},
		{"Shift_JIS", []byte{0x82}},
		{"GB18030", []byte{0xd6}},
		{"UTF-16LE", []byte{0x00, 0xd8}},
	} {
		r, err := mysqlDataOpenDBFRowsWithEncoding(bytes.NewReader(fixture(test.raw, 0)), test.encoding)
		if err != nil {
			t.Fatal(err)
		}
		_, err = r.Next(context.Background())
		r.Close()
		if err == nil {
			t.Fatal("invalid encoded bytes silently replaced", test.encoding)
		}
	}
}

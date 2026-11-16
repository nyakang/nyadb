package trino

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestTrinoNativeDataProjectionCompositePrecisionAndCompleteExport(t *testing.T) {
	p, secret := nativeProfile(t)
	s, err := OpenSession(context.Background(), p, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	verifyNativeDataProjections(t, s.(*Session))
}

func TestTrinoManagedNativeDataProjectionCompositePrecisionAndCompleteExport(t *testing.T) {
	p, secret, provider := managedNativeProvider(t)
	s, err := provider.Open(context.Background(), p, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	verifyNativeDataProjections(t, s.(*Session))
}

func verifyNativeDataProjections(t *testing.T, s *Session) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if _, err := s.Namespaces(ctx); err != nil {
		t.Fatal(err)
	}
	label := namespaceLabel("memory", "default")
	lease, err := s.ReserveQuery(91)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	run := func(sql string) {
		t.Helper()
		if _, err := lease.Execute(ctx, label, sql); err != nil {
			t.Fatal("original fixture statement failed", err)
		}
	}
	name := "owned_projection_" + strings.ToLower(rand.Text())
	o := s.Namespace(label).Object("TABLE", name)
	qname := qualified(o)
	run("CREATE TABLE " + qname + "(id BIGINT,b VARBINARY,r ROW(\"quoted field\" BIGINT,b VARBINARY,n DECIMAL(38,20),t TIME(12),j JSON),a ARRAY(ROW(BIGINT,VARBINARY)),m MAP(VARBINARY,ROW(BIGINT,ARRAY(DECIMAL(38,20)))),tm MAP(TIME(12),VARCHAR),t TIME(12),tz TIME(12) WITH TIME ZONE,ts TIMESTAMP(12),tsz TIMESTAMP(12) WITH TIME ZONE,j JSON,f ARRAY(DOUBLE),ch CHAR(5),ca ARRAY(CHAR(5)),cm MAP(CHAR(5),BIGINT),u UUID,ip IPADDRESS,dt DATE)")
	defer func() { _, _ = lease.Execute(context.Background(), label, "DROP TABLE IF EXISTS "+qname) }()
	run("INSERT INTO " + qname + " VALUES(1,X'00ff',ROW(9007199254740993,X'00ff',DECIMAL '123456789012345678.12345678901234567890',TIME '01:02:03.123456789123',JSON '{\"n\":9007199254740993}'),ARRAY[ROW(9007199254740993,X'00ff'),NULL],MAP(ARRAY[X'00ff',X''],ARRAY[ROW(9007199254740993,ARRAY[DECIMAL '1.23000000000000000000']),NULL]),MAP(ARRAY[TIME '01:02:03.123456789123'],ARRAY['中文\"quote']),TIME '01:02:03.123456789123',TIME '01:02:03.123456789123 +05:30',TIMESTAMP '2026-11-16 01:02:03.123456789123',TIMESTAMP '2026-11-16 01:02:03.123456789123 UTC',JSON '{\"n\":9007199254740993}',ARRAY[nan(),infinity(),-infinity()],'中文',ARRAY[CAST('中文' AS CHAR(5))],MAP(ARRAY[CAST('中文' AS CHAR(5))],ARRAY[9007199254740993]),UUID '11111111-2222-3333-4444-555555555555',IPADDRESS '192.168.0.1',DATE '2026-11-16'),(2,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL),(10,X'',ROW(NULL,NULL,NULL,NULL,NULL),ARRAY[],MAP(ARRAY[X''],ARRAY[ROW(1,ARRAY[])]),MAP(ARRAY[TIME '00:00:00'],ARRAY['']),TIME '00:00:00',TIME '00:00:00 +00:00',TIMESTAMP '2026-11-16 00:00:00',TIMESTAMP '2026-11-16 00:00:00 UTC',JSON 'null',ARRAY[],'',ARRAY[],MAP(ARRAY[CAST('' AS CHAR(5))],ARRAY[0]),NULL,NULL,NULL)")
	metadata, err := s.TableMetadata(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	// The independent native HTTP read is the reference, including parametric
	// temporal types and lossless JSON numbers. JDBC never switches transport.
	cfg, secret := nativeConfig(t)
	native, err := Open(ctx, cfg, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer native.Close()
	options := contract.DataOptions{Object: o, SortColumn: "id"}
	query, _, err := dataQuery(metadata, options, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	var reference [][]contract.Cell
	if err := native.Read(ctx, query, func(_ []contract.Column, row []contract.Cell) error {
		reference = append(reference, row)
		return nil
	}); err != nil || len(reference) != 3 {
		t.Fatal("independent native type reference failed", err)
	}
	equal := func(want, got contract.Cell) bool {
		if want.Kind != got.Kind || got.Truncated || !bytes.Equal(want.Bytes, got.Bytes) {
			return false
		}
		if want.Kind != contract.JSON {
			return want.Text == got.Text
		}
		decode := func(text string) any {
			var value any
			d := json.NewDecoder(strings.NewReader(text))
			d.UseNumber()
			if err := d.Decode(&value); err != nil {
				t.Fatal("invalid complete composite", err)
			}
			return value
		}
		return reflect.DeepEqual(decode(want.Text), decode(got.Text))
	}
	for _, bounded := range []bool{false, true} {
		projection, err := newDataProjection(metadata, nil, bounded)
		if err != nil {
			t.Fatal(err)
		}
		projected, _, err := dataQueryProjected(metadata, options, nil, false, projection)
		if err != nil {
			t.Fatal(err)
		}
		index := 0
		err = readData(ctx, native, projected, projection, func(cols []contract.Column, row []contract.Cell) error {
			for i, cell := range row {
				if !equal(reference[index][i], cell) || cols[i].DatabaseType != metadata.Columns[i].Type {
					t.Fatalf("native projection %d/%d bounded=%v differs: want=%+v got=%+v", index, i, bounded, reference[index][i], cell)
				}
			}
			index++
			return nil
		})
		if err != nil || index != 3 {
			t.Fatal("native projection did not retain every row", err)
		}
	}
	page, err := s.DataReads().AdvancedLoader(ctx, s.ProfileID(), label, name, contract.DataOptions{Object: o, Limit: 2, SortColumn: "id"})
	if err != nil || page == nil || len(page.Rows) != 2 || !page.HasMore {
		t.Fatal("original shared-grid projection failed", err)
	}
	for n, row := range page.Rows {
		for i, cell := range row {
			if cell.Value == nil || !equal(reference[n][i], *cell.Value) {
				t.Fatal("original shared-grid value changed", n, i, cell)
			}
		}
	}
	index := 0
	err = s.DataExports().Exporter(ctx, s.ProfileID(), label, name, options, nil, func(values []contract.DataValue) error {
		for i, v := range values {
			if !equal(reference[index][i], contract.Cell{Kind: v.Kind, Text: v.Text, Bytes: v.Bytes}) {
				t.Fatal("original complete export differs", index, i, v)
			}
		}
		index++
		return nil
	})
	if err != nil || index != 3 {
		t.Fatal("original complete export failed", err)
	}
	var jsonl bytes.Buffer
	if err := s.DataExports().JSONL(ctx, o, &jsonl); err != nil || bytes.Count(jsonl.Bytes(), []byte("\n")) != 3 {
		t.Fatal("original composite JSONL failed", err)
	}
	// Complete 1 MiB values exceed a 1 MiB wire limit after base64/JSON encoding.
	large := s.Namespace(label).Object("TABLE", name+"_large")
	run("CREATE TABLE " + qualified(large) + " AS SELECT id,IF(id=1,from_hex(rpad('',2097152,'00ff')),IF(id=2,X'',NULL)) b,IF(id=1,rpad('',1048576,'x'),IF(id=2,'',NULL)) s FROM (VALUES 1,2,3) v(id)")
	defer func() { _, _ = lease.Execute(context.Background(), label, "DROP TABLE IF EXISTS "+qualified(large)) }()
	largePage, err := s.DataReads().AdvancedLoader(ctx, s.ProfileID(), label, large.Name, contract.DataOptions{Object: large, Limit: 3, SortColumn: "id"})
	if err != nil || largePage == nil || len(largePage.Rows) != 3 || !largePage.Rows[0][1].Truncated || !largePage.Rows[0][2].Truncated || len(largePage.Rows[0][1].Value.Bytes) != previewCellBytes || len(largePage.Rows[0][2].Text) != previewCellBytes {
		t.Fatal("large preview bounds failed", err)
	}
	index = 0
	err = s.DataExports().Exporter(ctx, s.ProfileID(), label, large.Name, contract.DataOptions{Object: large, SortColumn: "id"}, []string{"b", "s"}, func(v []contract.DataValue) error {
		switch index {
		case 0:
			if len(v[0].Bytes) != contract.DataEditMaxBytes || len(v[1].Text) != contract.DataEditMaxBytes || !bytes.Equal(v[0].Bytes[:4], []byte{0, 255, 0, 255}) {
				return fmt.Errorf("complete 1 MiB value changed: binary=%d text=%d", len(v[0].Bytes), len(v[1].Text))
			}
		case 1:
			if v[0].Bytes == nil || v[0].Null || v[1].Null || v[1].Text != "" {
				return fmt.Errorf("empty large-export value became NULL")
			}
		case 2:
			if !v[0].Null || !v[1].Null {
				return fmt.Errorf("large-export NULL became an empty value")
			}
		}
		index++
		return nil
	})
	if err != nil || index != 3 {
		t.Fatal("complete original large-value export failed", err)
	}
}

package manticore

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func nativeField(t *testing.T, runtime contract.DataRuntime, table, field string, ref contract.RowRef, offset int64, limit int) *contract.FieldChunk {
	t.Helper()
	if runtime.Chunk == nil || runtime.FieldReadScopeHint == "" {
		t.Fatal("explicit original field reader not published")
	}
	v, err := runtime.Chunk(context.Background(), 69, "Manticore", table, field, ref, offset, limit)
	if err != nil {
		t.Fatal(field, offset, err)
	}
	return v
}

func TestNativeFieldOriginalUnicodeTypesChangesAndRetiredGrants(t *testing.T) {
	cfg := nativeConfig(t)
	cfg.MaxConnections = 1
	s := openNative(t, cfg)
	q := nativeLease(t, s, 7501)
	table := createNativeTable(t, q)
	ctx := context.Background()
	original := "头" + strings.Repeat("猫🙂", 1025) + "尾"
	runNative(t, q, "INSERT INTO "+table+" (id,title,tag,qty,price,metadata) VALUES (1,'"+original+"','"+original+"',7,1.25,'{\"x\":\"猫\",\"n\":1}'),(2,'','',0,0,'')")
	_, runtime := nativeData(t, s, table)
	page, err := runtime.Loader(ctx, 69, "Manticore", table, 0, 2)
	if err != nil || len(page.Rows) != 2 {
		t.Fatal(page, err)
	}
	ref := *page.RowRefs[0]
	var evidence []*contract.FieldChunk
	for _, field := range []string{"title", "tag"} {
		complete := ""
		for _, offset := range []int64{0, 1024, 2048} {
			v := nativeField(t, runtime, table, field, ref, offset, 1024)
			expectedEnd := min(int64(2052), offset+1024)
			if v.Offset != offset || v.NextOffset != expectedEnd || v.TotalUnits != 2052 || v.HasMore != (expectedEnd < 2052) || v.Null || v.Binary || !utf8.ValidString(v.Text) || utf8.RuneCountInString(v.Text) != int(expectedEnd-offset) {
				t.Fatal(field, v)
			}
			complete += v.Text
			evidence = append(evidence, v)
		}
		if complete != original {
			t.Fatal("original field was normalized or byte-sliced")
		}
		if v := nativeField(t, runtime, table, field, ref, 2052, 1024); v.Text != "" || v.HasMore || v.NextOffset != 2052 {
			t.Fatal(v)
		}
	}
	for field, expected := range map[string]string{"id": "1", "qty": "7", "price": "1.25", "metadata": `{"x":"猫","n":1}`} {
		if v := nativeField(t, runtime, table, field, ref, 0, 1024); v.Text != expected || v.Null || v.TotalUnits != int64(utf8.RuneCountInString(expected)) {
			t.Fatal(field, v)
		}
	}
	for _, field := range []string{"title", "tag"} {
		if v := nativeField(t, runtime, table, field, *page.RowRefs[1], 0, 1024); v.Text != "" || v.Null || v.TotalUnits != 0 || v.NextOffset != 0 || v.HasMore {
			t.Fatal("empty field became null", field, v)
		}
	}
	// The native original value may be NULL or empty CHAR across table modes;
	// assert exactly what this original page observed, without fabrication.
	nullJSON := page.Rows[1][5].Null
	if v := nativeField(t, runtime, table, "metadata", *page.RowRefs[1], 0, 1024); v.Null != nullJSON || v.Text != "" || v.TotalUnits != 0 {
		t.Fatal("native empty JSON observation changed", v)
	}
	for _, input := range []struct {
		offset int64
		limit  int
	}{{-1, 1}, {0, 0}, {0, 1025}, {2053, 1}, {math.MaxInt64, 1024}} {
		if _, err = runtime.Chunk(ctx, 69, "Manticore", table, "title", ref, input.offset, input.limit); !errors.Is(err, connections.ErrInvalid) {
			t.Fatal("invalid fragment bounds", input, err)
		}
	}
	if _, err = runtime.Chunk(ctx, 70, "Manticore", table, "title", ref, 0, 1); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("foreign profile", err)
	}
	if _, err = runtime.Chunk(ctx, 69, "Manticore", table, "absent", ref, 0, 1); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("foreign field", err)
	}
	_, other := nativeData(t, s, table)
	if _, err = other.Chunk(ctx, 69, "Manticore", table, "title", ref, 0, 1); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("another document accepted original grant", err)
	}
	// Only the selected field is revalidated. A different attribute may change
	// without replacing the captured text; this is not a whole-row CAS claim.
	runNative(t, q, "UPDATE "+table+" SET qty=17 WHERE id=1")
	if v := nativeField(t, runtime, table, "title", ref, 0, 1024); v.Text != string([]rune(original)[:1024]) {
		t.Fatal(v)
	}
	if _, err = runtime.Chunk(ctx, 69, "Manticore", table, "qty", ref, 0, 1024); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("changed selected field published", err)
	}
	runNative(t, q, "REPLACE INTO "+table+" (id,title,tag) VALUES (1,'changed','changed')")
	if _, err = runtime.Chunk(ctx, 69, "Manticore", table, "title", ref, 1024, 1024); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("different field fragments mixed", err)
	}
	runNative(t, q, "DELETE FROM "+table+" WHERE id=1")
	if _, err = runtime.Chunk(ctx, 69, "Manticore", table, "title", ref, 0, 1); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("missing row became empty field", err)
	}
	if _, err = runtime.Loader(ctx, 69, "Manticore", table, 0, 2); err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.Chunk(ctx, 69, "Manticore", table, "title", *page.RowRefs[1], 0, 1); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("retired page grant reused", err)
	}
	objectEvidence(t, "native-field-fragments.json", evidence)
}

func TestNativeFieldLargeViewDoesNotPromoteCompleteEditor(t *testing.T) {
	s := openNative(t, nativeConfig(t))
	q := nativeLease(t, s, 7502)
	table := createNativeTable(t, q)
	original := strings.Repeat("x", contract.DataEditMaxBytes+1)
	if _, err := q.Execute(context.Background(), "Manticore", "INSERT INTO "+table+" (id,title) VALUES (1,'"+original+"')"); err != nil {
		t.Fatal(err)
	}
	_, runtime := nativeData(t, s, table)
	page, err := runtime.Loader(context.Background(), 69, "Manticore", table, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	ref := *page.RowRefs[0]
	if _, err = runtime.RowReader(context.Background(), 69, "Manticore", table, ref); !errors.Is(err, contract.ErrDataEditLimit) {
		t.Fatal("complete editor limit widened", err)
	}
	v := nativeField(t, runtime, table, "title", ref, contract.DataEditMaxBytes-3, 1024)
	if v.Text != "xxxx" || v.TotalUnits != contract.DataEditMaxBytes+1 || v.HasMore {
		t.Fatal(v)
	}
	objectEvidence(t, "native-field-above-edit-limit.json", v)
}

func TestNativePlainFieldOriginalLeadingSpaceAndIndependentAttributes(t *testing.T) {
	requireNativePlain(t)
	s := openNative(t, nativeConfig(t))
	q := nativeLease(t, s, 7503)
	_, runtime := nativeData(t, s, "nyadb_plain")
	page, err := runtime.Loader(context.Background(), 69, "Manticore", "nyadb_plain", 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	ref := *page.RowRefs[0]
	var chunks []*contract.FieldChunk
	text := ""
	for _, offset := range []int64{0, 1024, 2048} {
		v := nativeField(t, runtime, "nyadb_plain", "title", ref, offset, 1024)
		if v.TotalUnits != 2050 || v.NextOffset != min(int64(2050), offset+1024) {
			t.Fatal(v)
		}
		text += v.Text
		chunks = append(chunks, v)
	}
	if text != " "+strings.Repeat("猫", 2049) {
		t.Fatal("plain original leading space lost")
	}
	runNative(t, q, "UPDATE nyadb_plain SET qty=31 WHERE id=1")
	defer runNative(t, q, "UPDATE nyadb_plain SET qty=1 WHERE id=1")
	if _, err = runtime.Chunk(context.Background(), 69, "Manticore", "nyadb_plain", "qty", ref, 0, 1024); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("changed plain field", err)
	}
	if v := nativeField(t, runtime, "nyadb_plain", "title", ref, 2048, 1024); v.Text != "猫猫" || v.HasMore {
		t.Fatal(v)
	}
	objectEvidence(t, "native-plain-field-fragments.json", chunks)
}

func TestNativeFieldOriginalCancellationCloseAndPageReplacement(t *testing.T) {
	cfg := nativeConfig(t)
	control := openNative(t, cfg)
	q := nativeLease(t, control, 7504)
	table := createNativeTable(t, q)
	runNative(t, q, "INSERT INTO "+table+" (id,title,qty) VALUES (1,'original',7)")
	for _, action := range []string{"cancel", "document-close", "session-close", "page-replacement", "schema-change"} {
		t.Run(action, func(t *testing.T) {
			proxied, gate := gateConfig(t, cfg)
			s := openNative(t, proxied)
			d, runtime := nativeData(t, s, table)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			page, err := runtime.Loader(ctx, 69, "Manticore", table, 0, 1)
			if err != nil {
				t.Fatal(err)
			}
			source := "SELECT `title` FROM `Manticore`." + quote(table) + " WHERE id=1 LIMIT 2 OPTION max_matches=2"
			observed := gate.arm(source, "hold")
			type response struct {
				chunk *contract.FieldChunk
				err   error
			}
			done := make(chan response, 1)
			go func() {
				v, e := runtime.Chunk(ctx, 69, "Manticore", table, "title", *page.RowRefs[0], 0, 1024)
				done <- response{v, e}
			}()
			select {
			case <-observed:
			case <-time.After(10 * time.Second):
				t.Fatal("original field response not observed")
			}
			switch action {
			case "cancel":
				cancel()
			case "document-close":
				if err = d.Close(); err != nil {
					t.Fatal(err)
				}
			case "session-close":
				if err = s.Close(); err != nil {
					t.Fatal(err)
				}
			case "page-replacement":
				if _, err = runtime.Loader(context.Background(), 69, "Manticore", table, 0, 1); err != nil {
					t.Fatal(err)
				}
				close(gate.resume)
			case "schema-change":
				runNative(t, q, "ALTER TABLE "+table+" ADD COLUMN changed integer")
				defer runNative(t, q, "ALTER TABLE "+table+" DROP COLUMN changed")
				close(gate.resume)
			}
			select {
			case result := <-done:
				if result.err == nil || result.chunk != nil {
					t.Fatal("revoked field published", result)
				}
				if action == "page-replacement" || action == "schema-change" {
					if !errors.Is(result.err, contract.ErrDataConflict) {
						t.Fatal(result.err)
					}
				}
			case <-time.After(10 * time.Second):
				t.Fatal("original field executor did not settle")
			}
			if gate.calls.Load() != 1 {
				t.Fatal("field replayed", gate.calls.Load())
			}
			if action == "cancel" {
				if n, e := runtime.Count(context.Background(), 69, "Manticore", table); e != nil || n != 1 {
					t.Fatal("field cancellation retired count", n, e)
				}
				if _, e := runtime.Loader(context.Background(), 69, "Manticore", table, 0, 1); e != nil {
					t.Fatal("field cancellation retired page", e)
				}
			}
		})
	}
}

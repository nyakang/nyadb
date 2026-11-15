package rqlite

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// Local stock SQL execution diagnoses the arithmetic only. Native product
// acceptance is separate and uses the pinned official RQLite artifact.
func TestStockSQLSHA256BoundaryRandomAndBatchIdentity(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rng := rand.New(rand.NewPCG(0x6e79616462, 0x72716c697465))
	lengths := []int{0, 1, 3, 54, 55, 56, 57, 63, 64, 65, 119, 120, 127, 128, 129, 255, 256, 257, 1000, 4096}
	for range 24 {
		lengths = append(lengths, rng.IntN(4096))
	}
	input := []string{"SELECT -1 AS id,NULL AS v"}
	expected := map[int]string{}
	for i, n := range lengths {
		data := make([]byte, n)
		for j := range data {
			data[j] = byte(rng.Uint32())
		}
		digest := sha256.Sum256(data)
		// Noncontiguous/reversed IDs must survive interleaved recursion.
		id := 10000 - i*3
		expected[id] = hex.EncodeToString(digest[:])
		input = append(input, fmt.Sprintf("SELECT %d,X'%s'", id, hex.EncodeToString(data)))
	}
	query := "WITH RECURSIVE " + hashCTEs(strings.Join(input, " UNION ALL ")) + " SELECT id,digest FROM _nyadb_hash ORDER BY id"
	rows, err := db.Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[int]bool{}
	for rows.Next() {
		var id int
		var digest sql.NullString
		if err := rows.Scan(&id, &digest); err != nil {
			t.Fatal(err)
		}
		if seen[id] {
			t.Fatal("hash returned the same original input twice")
		}
		seen[id] = true
		if id == -1 {
			if digest.Valid {
				t.Fatal("SQL NULL became the hash of an empty value")
			}
		} else if want, ok := expected[id]; !ok || !digest.Valid || digest.String != want {
			t.Fatalf("original input %d digest mismatch", id)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(seen) != len(input) {
		t.Fatal("batch lost original input identities")
	}
}

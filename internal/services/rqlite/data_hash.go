package rqlite

import (
	"fmt"
	"strconv"
	"strings"
)

// Stock rqlite has no digest function. These CTEs hash complete server BLOBs
// without fetching them through the bounded preview response or installing an
// extension. input is internally generated SQL yielding unique integer id and
// nullable BLOB v columns. The final _nyadb_hash relation yields id and digest;
// SQL NULL remains NULL and the empty BLOB has the standard empty SHA-256.
// Execution uses the original request's database/HTTP limits, never a retry.
func hashCTEs(input string) string {
	const mask = "4294967295"
	rotate := func(x string, n int) string {
		return fmt.Sprintf("((%s >> %d) | ((%s << %d) & %s))", x, n, x, 32-n, mask)
	}
	xor := func(x, y, z string) string {
		// At each bit, the sum minus twice the majority is the XOR of three
		// unsigned words. Intermediate sums stay below signed 64-bit limits.
		return fmt.Sprintf("((%s)+(%s)+(%s)-2*(((%s)&(%s))|((%s)&(%s))|((%s)&(%s))))", x, y, z, x, y, x, z, y, z)
	}
	word := func(pos string) string {
		parts := make([]string, 8)
		for i := range parts {
			parts[i] = fmt.Sprintf("((instr('0123456789ABCDEF',CAST(substr(p.padded,(%s)+%d,1) AS TEXT))-1)*%d)", pos, i, uint64(1)<<uint(4*(7-i)))
		}
		return "(" + strings.Join(parts, "+") + ")"
	}
	constants := [...]uint32{
		0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
		0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
		0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
		0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
		0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
		0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
		0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
		0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
	}
	k := make([]string, len(constants))
	for i, v := range constants {
		k[i] = fmt.Sprintf("(%d,%d)", i, v)
	}
	initialHash := [...]uint32{0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19}
	fields := []string{"id", "blocks", "block", "r"}
	initial := []string{"p.id", "p.blocks", "0", "0"}
	for _, ch := range "abcdefgh" {
		fields = append(fields, string(ch))
	}
	for i := range initialHash {
		fields = append(fields, fmt.Sprintf("h%d", i))
	}
	for range 2 {
		for _, v := range initialHash {
			initial = append(initial, strconv.FormatUint(uint64(v), 10))
		}
	}
	for i := range 16 {
		fields = append(fields, fmt.Sprintf("w%d", i))
		initial = append(initial, word(strconv.Itoa(1+i*8)))
	}
	t1 := "((s.h+" + xor(rotate("s.e", 6), rotate("s.e", 11), rotate("s.e", 25)) + "+((s.e&s.f)|((~s.e)&s.g))+k.v+s.w0)&" + mask + ")"
	t2 := "((" + xor(rotate("s.a", 2), rotate("s.a", 13), rotate("s.a", 22)) + "+((s.a&s.b)|(s.a&s.c)|(s.b&s.c)))&" + mask + ")"
	working := []string{"((" + t1 + "+" + t2 + ")&" + mask + ")", "s.a", "s.b", "s.c", "((s.d+" + t1 + ")&" + mask + ")", "s.e", "s.f", "s.g"}
	updated := []string{"s.id", "s.blocks", "s.block+CASE WHEN s.r=64 THEN 1 ELSE 0 END", "(s.r+1)%65"}
	sums, output := make([]string, 8), make([]string, 8)
	for i, ch := range "abcdefgh" {
		sums[i] = fmt.Sprintf("((s.h%d+s.%c)&%s)", i, ch, mask)
		updated = append(updated, "CASE WHEN s.r<64 THEN "+working[i]+" ELSE "+sums[i]+" END")
		output[i] = "printf('%08x'," + sums[i] + ")"
	}
	for i := range 8 {
		updated = append(updated, fmt.Sprintf("CASE WHEN s.r<64 THEN s.h%d ELSE %s END", i, sums[i]))
	}
	nextWord := "((s.w0+" + xor(rotate("s.w1", 7), rotate("s.w1", 18), "(s.w1>>3)") + "+s.w9+" + xor(rotate("s.w14", 17), rotate("s.w14", 19), "(s.w14>>10)") + ")&" + mask + ")"
	for i := range 16 {
		v := fmt.Sprintf("s.w%d", i+1)
		if i == 15 {
			v = nextWord
		}
		updated = append(updated, "CASE WHEN s.r<64 THEN "+v+" ELSE "+word(strconv.Itoa(1+i*8))+" END")
	}
	// Keep the padded hexadecimal as a BLOB: substr(TEXT) rescans prefixes to
	// count code points. Uppercase the bit-length suffix as well as hex(v).
	// The conditional join KEY is essential: putting r=64 beside id equality
	// still permits SQLite to look up the large padded record on every round.
	// A NULL key prevents that lookup until the next block is actually needed.
	return "_nyadb_hash_input(id,v) AS MATERIALIZED (" + input + ")," +
		"_nyadb_hash_bytes(id,padded,blocks) AS MATERIALIZED (SELECT id,CAST(hex(v)||'80'||substr('" + strings.Repeat("0", 128) + "',1,((55-length(v)%64+64)%64)*2)||upper(printf('%016x',length(v)*8)) AS BLOB),(length(v)+72)/64 FROM _nyadb_hash_input WHERE v IS NOT NULL)," +
		"_nyadb_hash_ordinals(b) AS (SELECT 0 UNION ALL SELECT b+1 FROM _nyadb_hash_ordinals WHERE b+1<(SELECT max(blocks) FROM _nyadb_hash_bytes))," +
		"_nyadb_hash_blocks(id,block,padded,blocks) AS MATERIALIZED (SELECT p.id,b.b,substr(p.padded,b.b*128+1,128),p.blocks FROM _nyadb_hash_bytes p JOIN _nyadb_hash_ordinals b ON b.b<p.blocks)," +
		"_nyadb_hash_constants(i,v) AS (VALUES " + strings.Join(k, ",") + ")," +
		"_nyadb_hash_state(" + strings.Join(fields, ",") + ") AS (SELECT " + strings.Join(initial, ",") + " FROM _nyadb_hash_blocks p WHERE p.block=0 UNION ALL SELECT " + strings.Join(updated, ",") + " FROM _nyadb_hash_state s LEFT JOIN _nyadb_hash_blocks p ON p.id=CASE WHEN s.r=64 THEN s.id END AND p.block=s.block+1 JOIN _nyadb_hash_constants k ON k.i=s.r%64 WHERE s.r<64 OR s.block+1<s.blocks)," +
		"_nyadb_hash(id,digest) AS MATERIALIZED (SELECT s.id," + strings.Join(output, "||") + " FROM _nyadb_hash_state s WHERE s.r=64 AND s.block=s.blocks-1 UNION ALL SELECT id,NULL FROM _nyadb_hash_input WHERE v IS NULL)"
}

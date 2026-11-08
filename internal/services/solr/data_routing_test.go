package solr

import "testing"

// Golden hashes from the shipped Solr 9.9.0 CompositeIdRouter.sliceHash,
// build 386ed096a1946c488cfe576a19a147bdb1153508. They cover UTF-8,
// signed bits, native prefix width overrides and trailing separator behavior.
func TestSolrCompositeRoutingMatchesShippedNativeHashes(t *testing.T) {
	cases := []struct {
		key  string
		hash int32
	}{
		{"", 0},
		{"foo", -156908512},
		{"hello", 613153351},
		{"plain🐱", -58474140},
		{"a!", 1009057792},
		{"a!!", 1009057792},
		{"a!b!", 1021181952},
		{"a!b!!", 1021189364},
		{"app/2!user/4!id!tail", 1419827219},
		{"app/0!user/32!x", -1785447155},
		{"app/32!suffix", 2039377815},
		{"a/!b", -1780580861},
		{"!", 0},
		{"!!", 0},
		{"a!b!c!d", 1021210739},
		{"原生!用户!🐱", 1232313964},
		{"a/0!b", -1780580861},
		{"a/1!b", 366902787},
		{"a/31!b", 1009084851},
	}
	for _, c := range cases {
		got, err := compositeHash(c.key)
		if err != nil || got != c.hash {
			t.Fatalf("native hash %q: got %d, want %d, error %v", c.key, got, c.hash, err)
		}
	}
	for _, key := range []string{"a/33!b", "a/x!b", "a/20!b/20!c", string([]byte{0xff})} {
		if _, err := compositeHash(key); err == nil {
			t.Fatal("malformed routing accepted", key)
		}
	}
	for _, text := range []string{"80000000-ffffffff", "0-7fffffff", "80000000-7fffffff"} {
		if _, err := parseShardRange(text); err != nil {
			t.Fatal(text, err)
		}
	}
	for _, text := range []string{"ffffffff-80000000", "0-100000000", "1-0", "bad", "-1-2"} {
		if _, err := parseShardRange(text); err == nil {
			t.Fatal("malformed range accepted", text)
		}
	}
}

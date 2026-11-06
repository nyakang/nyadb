package rediscommand

import "testing"

func TestClusterSlotsAndSyntheticTags(t *testing.T) {
	for _, tc := range []struct {
		key  string
		slot int
	}{
		{"123456789", 12739}, {"foo", 12182}, {"bar", 5061},
		{"{user1000}.following", 3443}, {"{user1000}.followers", 3443},
	} {
		if got := Slot(tc.key); got != tc.slot {
			t.Fatalf("%q slot %d, want %d", tc.key, got, tc.slot)
		}
	}
	if Slot("foo{}{bar}") == Slot("bar") {
		t.Fatal("empty first tag must hash the full key")
	}
	if Slot("foo{{bar}}") != Slot("{bar") {
		t.Fatal("nested braces do not change the first tag rule")
	}
	for slot := range 16384 {
		if tag := SlotTag(slot); tag == "" || Slot("__nyadb_{"+tag+"}_temporary") != slot {
			t.Fatalf("temporary key cannot share slot %d", slot)
		}
	}
}

package localdb

import (
	"path/filepath"
	"testing"
)

func TestAppearancePreferenceSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	value, err := Preference(db, "appearance")
	if err != nil || value != "" {
		t.Fatalf("default %q: %v", value, err)
	}
	if err := SetPreference(db, "appearance", "system"); err != nil {
		t.Fatal(err)
	}
	if err := SetPreference(db, "appearance", "light"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	value, err = Preference(db, "appearance")
	if err != nil || value != "light" {
		t.Fatalf("saved %q: %v", value, err)
	}
}

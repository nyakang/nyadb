package lucide

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestPinnedLucideIconsAreValidSVG(t *testing.T) {
	names, err := Names()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 16 {
		t.Fatalf("expected 16 Lucide icons, got %d", len(names))
	}
	for _, name := range names {
		data, err := Read(name)
		if err != nil {
			t.Fatal(err)
		}
		var document struct {
			XMLName xml.Name `xml:"svg"`
			ViewBox string   `xml:"viewBox,attr"`
		}
		if err := xml.Unmarshal(data, &document); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if document.XMLName.Local != "svg" || document.ViewBox != "0 0 24 24" {
			t.Fatalf("%s: invalid SVG document / viewport", name)
		}
		if !strings.Contains(string(data), `stroke="currentColor"`) {
			t.Fatalf("%s: missing theme-responsive currentColor stroke", name)
		}
	}
	license, err := License()
	if err != nil || !strings.Contains(string(license), "ISC License") {
		t.Fatalf("upstream license missing: %v", err)
	}
}

func TestLucideReadRejectsInvalidNames(t *testing.T) {
	for _, name := range []string{"", "../LICENSE", "folder.svg", "icons/folder", "Folder", "☠", "does-not-exist"} {
		if _, err := Read(name); err == nil {
			t.Errorf("Read accepted %q", name)
		}
	}
	if _, err := Read("database"); err != nil {
		t.Fatal(err)
	}
}

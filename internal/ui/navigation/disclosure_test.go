package navigation

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestNavigatorDisclosureUsesNativeTriangle(t *testing.T) {
	var tree ui.OutlineState[string]
	ConfigureDisclosure(&tree)
	if tree.Disclosure != ui.OutlineTriangle {
		t.Fatalf("disclosure = %v, want triangle", tree.Disclosure)
	}
	if tree.DisclosureSize != 10 {
		t.Fatalf("disclosure gutter = %v, want 10 DIPs", tree.DisclosureSize)
	}
	ConfigureDisclosure(&tree)
	if tree.Disclosure != ui.OutlineTriangle || tree.DisclosureSize != 10 {
		t.Fatal("enabling disclosure should be idempotent")
	}
}

func TestNavigatorDisclosureNilState(t *testing.T) {
	ConfigureDisclosure(nil)
}

package shell

import "testing"

func TestAllVendorFiltersHaveDistinctProviderBadges(t *testing.T) {
	icons, err := bundledProviderIcons()
	if err != nil {
		t.Fatal(err)
	}
	for _, vendor := range catalogTreeVendorOrder {
		icon := icons[vendor]
		if icon == nil {
			t.Errorf("no provider badge for %q", vendor)
			continue
		}
		if w, h := icon.Size(); w != 64 || h != 64 {
			t.Errorf("bad provider badge size %s: %d×%d", vendor, w, h)
		}
	}
	if len(icons) != len(catalogTreeVendorOrder) {
		t.Fatalf("provider count %d, vendor nodes %d", len(icons), len(catalogTreeVendorOrder))
	}
}

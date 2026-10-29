package theme

import (
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestBundledBrandCachesShareDecodedBitmaps(t *testing.T) {
	for _, tc := range []struct {
		name string
		load func() (map[string]*ui.Bitmap, error)
	}{
		{"connections", BundledConnectionIcons},
		{"providers", BundledProviderIcons},
	} {
		t.Run(tc.name, func(t *testing.T) {
			icons, err := tc.load()
			if err != nil {
				t.Fatal(err)
			}
			if len(icons) == 0 {
				t.Fatal("brand cache is empty")
			}
			var readers sync.WaitGroup
			for range 8 {
				readers.Go(func() {
					again, err := tc.load()
					if err != nil {
						t.Error(err)
						return
					}
					for name, icon := range icons {
						if icon == nil || again[name] != icon {
							t.Errorf("%s was decoded again instead of reusing the cache", name)
						}
					}
				})
			}
			readers.Wait()
		})
	}
}

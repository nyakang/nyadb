package shell

import (
	"fmt"
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/assets/providericons"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

var providerIconCache struct {
	once  sync.Once
	icons map[string]*ui.Bitmap
	err   error
}

func bundledProviderIcons() (map[string]*ui.Bitmap, error) {
	providerIconCache.once.Do(func() {
		manifest, err := providericons.List()
		if err != nil {
			providerIconCache.err = err
			return
		}
		icons := make(map[string]*ui.Bitmap, len(manifest.Entries))
		for _, entry := range manifest.Entries {
			raw, err := providericons.Read(entry.File)
			if err != nil {
				providerIconCache.err = fmt.Errorf("provider %s: %w", entry.Name, err)
				return
			}
			bitmap, err := ui.DecodeBitmap(raw)
			if err != nil {
				providerIconCache.err = fmt.Errorf("provider %s: %w", entry.Name, err)
				return
			}
			icons[entry.Name] = bitmap
		}
		providerIconCache.icons = icons
	})
	return providerIconCache.icons, providerIconCache.err
}

// catalogProviderIcon keeps vendor and engine imagery distinct: a database
// product badge is never misrepresented as the vendor's corporate logo.
func (s *State) catalogProviderIcon(c *ui.Context, vendor string, size float32) {
	if bitmap := s.providerIcons[vendor]; bitmap != nil {
		ui.Image(c, bitmap).Size(size, size).Fit(ui.Contain)
		return
	}
	theme.Icon(c, "catalog-vendor", size, theme.Muted)
}

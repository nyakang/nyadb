package shell

import (
	"fmt"
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/assets/connectionicons"
)

var bundledIconCache struct {
	once  sync.Once
	icons map[string]*ui.Bitmap
	err   error
}

// bundledCatalogIcons decodes ordinary committed assets only once per process.
// Bitmap textures can be reused by many windows, and no runtime filesystem
// access, network access, or research mode setup is required.
func bundledCatalogIcons() (map[string]*ui.Bitmap, error) {
	bundledIconCache.once.Do(func() {
		manifest, err := connectionicons.List()
		if err != nil {
			bundledIconCache.err = err
			return
		}
		icons := make(map[string]*ui.Bitmap, len(manifest.Entries))
		for _, entry := range manifest.Entries {
			raw, err := connectionicons.Read(entry.File)
			if err != nil {
				bundledIconCache.err = fmt.Errorf("read icon %q: %w", entry.Name, err)
				return
			}
			icon, err := ui.DecodeBitmap(raw)
			if err != nil {
				bundledIconCache.err = fmt.Errorf("decode icon %q: %w", entry.Name, err)
				return
			}
			icons[entry.Name] = icon
		}
		bundledIconCache.icons = icons
	})
	return bundledIconCache.icons, bundledIconCache.err
}

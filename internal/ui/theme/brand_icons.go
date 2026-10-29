package theme

import (
	"fmt"
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/assets/connectionicons"
	"github.com/nyakang/nyadb/internal/assets/providericons"
)

var bundledIconCache struct {
	once  sync.Once
	icons map[string]*ui.Bitmap
	err   error
}

// BundledConnectionIcons decodes committed connection assets once per process.
// The returned cache is shared by callers and must not be modified.
// Bitmap textures can be reused by many windows, and no runtime filesystem
// access, network access, or research mode setup is required.
func BundledConnectionIcons() (map[string]*ui.Bitmap, error) {
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

var providerIconCache struct {
	once  sync.Once
	icons map[string]*ui.Bitmap
	err   error
}

// BundledProviderIcons returns a shared, read-only cache of provider badges.
func BundledProviderIcons() (map[string]*ui.Bitmap, error) {
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

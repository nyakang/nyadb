//go:build !linux || (!amd64 && !arm64)

package bootstrap

import "github.com/nyakang/nyadb/internal/ui/theme"

func applyNativeGTKTheme(theme.Mode) {}

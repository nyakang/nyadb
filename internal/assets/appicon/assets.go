// Package appicon provides NyaDB's application icon for native startup.
// MyGo packaging reads the same committed PNG through mygo.json.
package appicon

import _ "embed"

//go:embed icon.png
var pngImage string

// PNG returns an independent copy of the application icon. It does not read
// files at runtime and callers may modify the returned bytes.
func PNG() []byte {
	return []byte(pngImage)
}

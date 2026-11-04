package connections

// Empty uses the theme's default accent. Values are local presentation only.
var profileColorPalette = []string{"", "#4C9AFF", "#36B37E", "#FFAB00", "#FF5630", "#6554C0", "#00B8D9", "#E774AD"}

func ProfileColors() []string { return append([]string(nil), profileColorPalette...) }
func ValidProfileColor(color string) bool {
	for _, value := range profileColorPalette {
		if color == value {
			return true
		}
	}
	return false
}

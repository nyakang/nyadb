package connections

// ManagedDriver is a non-secret installed package snapshot for a connection
// form. Selection pins its version/digest and original directory; it never
// chooses a latest version or launches an executable.
type ManagedDriver struct {
	Key, Version, SHA256, Directory, Platform string
	Profiles                                  []string
	JDBCClass                                 string
}

// JDBCImport captures one explicit local driver graph and its original base
// package. Paths and class are import inputs; the resulting package pins them.
type JDBCImport struct {
	BaseVersion, BaseSHA256, Profile, Class string
	Paths                                   []string
}

func (d ManagedDriver) Supports(profile string) bool {
	if d.Key == "h2" && profile == "h2-legacy" {
		profile = "h2-v2"
	}
	for _, p := range d.Profiles {
		if p == profile {
			return true
		}
	}
	return false
}

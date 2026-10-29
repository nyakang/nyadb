// Package connectioncatalog owns the connection type chooser, independently
// of connection editing, persistence and database sessions.
package connectioncatalog

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

// Choice identifies a database type. An empty Kind denotes a discovery-only
// entry whose driver is not supported.
type Choice struct {
	Name string
	Kind connections.Kind
}

// State is chooser state owned by the UI thread. Create it with New and reuse
// it when reopening the chooser. Never retain ui.Element values.
type State struct {
	selected      Choice
	search        string
	gridMode      bool
	treeOpen      map[string]bool
	recent        []string
	engineFilters map[string]bool
	vendorFilters map[string]bool
	bundledIcons  map[string]*ui.Bitmap
	providerIcons map[string]*ui.Bitmap
	researchIcons map[string]*ui.Bitmap
}

func New() *State {
	s := &State{gridMode: true, treeOpen: map[string]bool{}}
	s.Reset()
	s.bundledIcons, _ = theme.BundledConnectionIcons()
	s.providerIcons, _ = theme.BundledProviderIcons()
	return s
}

// Reset clears the current selection, search and filters. Presentation,
// expansion state, in-session recents and loaded icons remain available.
func (s *State) Reset() {
	s.selected = Choice{}
	s.clearCatalogFilters()
}

func (s *State) Selected() Choice {
	return s.selected
}

// RecordRecent records actual use, after the host successfully opens the
// connection editor. Selecting a card alone must not move the catalog.
func (s *State) RecordRecent(name string) {
	unique := []string{name}
	for _, candidate := range s.recent {
		if candidate != name && len(unique) < 5 {
			unique = append(unique, candidate)
		}
	}
	s.recent = unique
}

func (s *State) chooseCatalogEntry(e catalogEntry, onSelect func(Choice)) {
	s.selected = Choice{Name: e.name, Kind: e.kind}
	if onSelect != nil {
		onSelect(s.selected)
	}
}

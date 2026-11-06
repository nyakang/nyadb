package queries

import (
	"bytes"
	"slices"
)

// CloneWorkspace owns mutable presentation buffers and slices while reusing
// immutable SQL strings. Workers and deferred UI continuations can retain it.
func CloneWorkspace(w Workspace) Workspace {
	w.Shell = bytes.Clone(w.Shell)
	w.Documents = slices.Clone(w.Documents)
	for i := range w.Documents {
		w.Documents[i].Presentation = bytes.Clone(w.Documents[i].Presentation)
	}
	w.Tabs = slices.Clone(w.Tabs)
	for i := range w.Tabs {
		w.Tabs[i].Presentation = bytes.Clone(w.Tabs[i].Presentation)
	}
	return w
}

package navigation

import (
	"encoding/json"
	"sort"
)

type workspacePresentation struct {
	Key, Group string
	Open       []string
	Filter     Filter
}

func (s *State) WorkspaceSnapshot() json.RawMessage {
	v := workspacePresentation{Key: s.Key, Group: s.Group, Filter: s.Filter}
	for key := range s.Tree.Open.All() {
		v.Open = append(v.Open, key)
	}
	sort.Strings(v.Open)
	b, _ := json.Marshal(v)
	return b
}
func (s *State) RestoreWorkspace(b json.RawMessage) error {
	var v workspacePresentation
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	s.Key, s.Group, s.Filter, s.Row = v.Key, v.Group, v.Filter, -1
	s.Tree.Open.Clear()
	for _, key := range v.Open {
		s.Tree.Open.Add(key)
	}
	return nil
}

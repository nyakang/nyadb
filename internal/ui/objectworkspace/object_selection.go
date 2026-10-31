package objectworkspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

type objectKey struct {
	ProfileID            int64
	Database, Kind, Name string
}
type ObjectClipboard struct {
	ProfileID int64 `json:"profileID,omitempty"`

	Format  string                    `json:"format"`
	Version int                       `json:"version"`
	Objects []mysqlsvc.ObjectIdentity `json:"objects"`
}

func EncodeObjectClipboard(objects []mysqlsvc.ObjectIdentity) (string, error) {
	if len(objects) == 0 || len(objects) > 4096 {
		return "", errors.New("invalid object selection")
	}
	raw, e := json.Marshal(ObjectClipboard{Format: "nyadb-objects", Version: 1, Objects: append([]mysqlsvc.ObjectIdentity(nil), objects...)})
	return string(raw), e
}
func EncodeObjectClipboardFor(profile int64, objects []mysqlsvc.ObjectIdentity) (string, error) {
	raw, e := EncodeObjectClipboard(objects)
	if e != nil {
		return "", e
	}
	var clip ObjectClipboard
	if e = json.Unmarshal([]byte(raw), &clip); e != nil {
		return "", e
	}
	clip.ProfileID = profile
	b, e := json.Marshal(clip)
	return string(b), e
}
func DecodeObjectClipboard(raw string) (ObjectClipboard, error) {
	var v ObjectClipboard
	if len(raw) > 1<<20 {
		return v, errors.New("object clipboard is oversized")
	}
	if e := json.Unmarshal([]byte(raw), &v); e != nil {
		return v, e
	}
	if v.Format != "nyadb-objects" || v.Version != 1 || len(v.Objects) == 0 || len(v.Objects) > 4096 {
		return v, errors.New("unsupported object clipboard format")
	}
	for _, o := range v.Objects {
		if o.Database == "" || o.Name == "" || o.Kind == "" {
			return v, errors.New("unqualified clipboard identity")
		}
	}
	return v, nil
}
func (s *State) SelectedObjectIdentities() []mysqlsvc.ObjectIdentity {
	w := s.currentBrowser()
	items := s.SelectedObjects()
	out := []mysqlsvc.ObjectIdentity{}
	for i, o := range items {
		if w.selection.Has(objectKey{w.ProfileID, w.Database, o.Kind, o.Name}) || (w.selection.Len() == 0 && w.Row == i) {
			out = append(out, mysqlsvc.ObjectIdentity{Database: w.Database, Name: o.Name, Kind: o.Kind})
		}
	}
	return out
}
func (s *State) NextObjectName(profile int64, base string) string {
	runes := []rune(base)
	if len(runes) > 58 {
		base = string(runes[:58])
	}
	names := map[string]bool{}
	if s.deps.Browser != nil {
		b := s.deps.Browser()
		if b.ProfileID == profile {
			for _, o := range b.Objects {
				names[strings.ToLower(o.Name)] = true
			}
		}
	}
	if !names[strings.ToLower(base)] {
		return base
	}
	for i := 2; i < 100000; i++ {
		name := fmt.Sprintf("%s_%d", base, i)
		if !names[strings.ToLower(name)] {
			return name
		}
	}
	return base + "_new"
}
func (s *State) AssignObjectGroup(name string) {
	w := s.currentBrowser()
	if w.groups == nil {
		w.groups = make(map[objectKey]string)
	}
	for _, o := range s.SelectedObjectIdentities() {
		w.groups[objectKey{w.ProfileID, o.Database, o.Kind, o.Name}] = strings.TrimSpace(name)
	}
	s.saveObjectGroups(w.ProfileID, w.Database)
}
func (s *State) ObjectGroup(profile int64, database, kind, name string) string {
	return s.presentation(profile).groups[objectKey{profile, database, kind, name}]
}

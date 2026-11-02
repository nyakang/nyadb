// Package favorites stores public navigation references, never SQL or secrets.
package favorites

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

const PreferenceKey = "workbench_favorites_v1"

var ErrConflict = errors.New("favorites changed; refresh before retrying")
var ErrInvalid = errors.New("invalid favorites directory; original record retained")

type Repository interface {
	Read(context.Context) (string, error)
	Publish(context.Context, string, string) (bool, error)
}
type Store struct{ repository Repository }

func NewStore(repository Repository) *Store { return &Store{repository: repository} }

type Entry struct {
	ID             string             `json:"id"`
	ProfileID      int64              `json:"profile_id"`
	Kind           string             `json:"kind"` // object or query; starred connections remain in profiles
	Name           string             `json:"name"`
	GroupID        string             `json:"group_id,omitempty"`
	Object         contract.ObjectRef `json:"object,omitempty"`
	NamespaceLabel string             `json:"namespace_label,omitempty"`
	QueryID        string             `json:"query_id,omitempty"`
}
type Group struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type Snapshot struct {
	Version  int     `json:"version"`
	Revision uint64  `json:"revision"`
	Entries  []Entry `json:"entries"`
	Groups   []Group `json:"groups"`
	token    string
}

func NewID() string {
	var v [16]byte
	if _, e := rand.Read(v[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(v[:])
}
func (s Snapshot) Clone() Snapshot {
	s.Entries = append([]Entry(nil), s.Entries...)
	s.Groups = append([]Group(nil), s.Groups...)
	return s
}
func (e Entry) SameTarget(other Entry) bool {
	return e.Kind == other.Kind && e.ProfileID == other.ProfileID && (e.Kind == "query" && e.QueryID == other.QueryID || e.Kind == "object" && e.Object == other.Object)
}
func (s *Store) Load(ctx context.Context) (Snapshot, error) {
	raw, err := s.repository.Read(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	out := Snapshot{Version: 1, token: raw}
	if raw != "" {
		out.Version = 0
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.DisallowUnknownFields()
		if len(raw) > 8<<20 || decoder.Decode(&out) != nil || decoder.Decode(new(any)) != io.EOF {
			return Snapshot{}, ErrInvalid
		}
	}
	if validate(out) != nil {
		return Snapshot{}, ErrInvalid
	}
	return out, nil
}
func (s *Store) Save(ctx context.Context, before, after Snapshot) (Snapshot, error) {
	after = after.Clone()
	after.Version = 1
	if before.Revision == ^uint64(0) {
		return Snapshot{}, ErrInvalid
	}
	after.Revision = before.Revision + 1
	for i := range after.Entries {
		after.Entries[i].Name = strings.TrimSpace(after.Entries[i].Name)
	}
	for i := range after.Groups {
		after.Groups[i].Name = strings.TrimSpace(after.Groups[i].Name)
	}
	if err := validate(after); err != nil {
		return Snapshot{}, err
	}
	raw, err := json.Marshal(after)
	if err != nil || len(raw) > 8<<20 {
		return Snapshot{}, ErrInvalid
	}
	ok, err := s.repository.Publish(ctx, before.token, string(raw))
	if err != nil {
		return Snapshot{}, err
	}
	if !ok {
		return Snapshot{}, ErrConflict
	}
	after.token = string(raw)
	return after, nil
}
func validName(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && utf8.ValidString(value) && len([]rune(value)) <= 120 && strings.IndexFunc(value, unicode.IsControl) < 0
}
func validate(s Snapshot) error {
	if s.Version != 1 || len(s.Entries) > 10000 || len(s.Groups) > 512 {
		return ErrInvalid
	}
	ids, names := map[string]bool{}, map[string]bool{}
	for _, g := range s.Groups {
		if g.ID == "" || len(g.ID) > 128 || ids[g.ID] || names[g.Name] || !validName(g.Name) {
			return ErrInvalid
		}
		ids[g.ID] = true
		names[g.Name] = true
	}
	entries := map[string]bool{}
	targets := map[string]bool{}
	for _, e := range s.Entries {
		if e.ID == "" || len(e.ID) > 128 || entries[e.ID] || !validName(e.Name) || e.ProfileID <= 0 || e.GroupID != "" && !ids[e.GroupID] {
			return ErrInvalid
		}
		entries[e.ID] = true
		if e.Kind == "query" {
			if e.QueryID == "" || len(e.QueryID) > 128 || e.Object != (contract.ObjectRef{}) || e.NamespaceLabel != "" {
				return ErrInvalid
			}
		} else if e.Kind == "object" {
			if e.QueryID != "" || e.Object.ProfileID != e.ProfileID || e.Object.Name == "" || e.Object.Catalog == "" && e.Object.Schema == "" || e.NamespaceLabel == "" {
				return ErrInvalid
			}
			switch e.Object.Kind {
			case "table", "view", "function", "procedure", "event":
			default:
				return ErrInvalid
			}
			for _, v := range []string{e.Object.Name, e.Object.Catalog, e.Object.Schema, e.NamespaceLabel} {
				if len(v) > 4096 || !utf8.ValidString(v) || strings.IndexFunc(v, unicode.IsControl) >= 0 {
					return ErrInvalid
				}
			}
		} else {
			return ErrInvalid
		}
		raw, _ := json.Marshal(struct {
			Profile     int64
			Kind, Query string
			Object      contract.ObjectRef
		}{e.ProfileID, e.Kind, e.QueryID, e.Object})
		key := string(raw)
		if targets[key] {
			return ErrInvalid
		}
		targets[key] = true
	}
	return nil
}

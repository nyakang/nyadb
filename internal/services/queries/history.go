package queries

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
)

const HistoryMaxBytes = 32 << 20

var ErrInvalidHistoryEntry = errors.New("invalid SQL history entry")

var ErrHistoryChanged = errors.New("SQL history was cleared or disabled after execution started")

// History is an encrypted, bounded log. Its scope tokens revoke executions
// predating a clear, including executions still waiting on a driver receipt.
// SQL and errors never appear in a plaintext storage column.
type History struct {
	Revision      uint64
	Epoch         uint64
	ProfileEpochs map[int64]uint64
	Settings      HistorySettings
	Entries       []HistoryEntry
}
type HistorySettings struct {
	Enabled       bool
	RetentionDays int
	MaxEntries    int
}
type HistoryToken struct{ Epoch, ProfileEpoch uint64 }
type HistoryEntry struct {
	ID                                  string
	ProfileID                           int64
	Database, Source, SQL, State, Error string
	Started                             time.Time
	Duration                            time.Duration
	Rows, AffectedRows                  int64
	Generation                          uint64
	Object                              string
	Summary                             bool
}
type HistoryChange struct {
	Kind      string
	Entry     HistoryEntry
	Token     HistoryToken
	ProfileID int64
	ID        string
	Settings  HistorySettings
}

func DefaultHistory() History {
	return History{Epoch: 1, ProfileEpochs: map[int64]uint64{}, Settings: HistorySettings{Enabled: true, RetentionDays: 30, MaxEntries: 1000}}
}
func (h History) Clone() History {
	out := h
	out.Entries = append([]HistoryEntry(nil), h.Entries...)
	out.ProfileEpochs = make(map[int64]uint64, len(h.ProfileEpochs))
	for id, epoch := range h.ProfileEpochs {
		out.ProfileEpochs[id] = epoch
	}
	return out
}
func (h History) Token(profile int64) HistoryToken {
	epoch := h.ProfileEpochs[profile]
	if epoch == 0 {
		epoch = 1
	}
	return HistoryToken{h.Epoch, epoch}
}
func validHistorySettings(s HistorySettings) bool {
	return s.RetentionDays >= 1 && s.RetentionDays <= 365 && s.MaxEntries >= 1 && s.MaxEntries <= 10000
}
func (s *Store) loadHistory(ctx context.Context) (History, error) {
	h := DefaultHistory()
	r, exists, err := s.record(ctx, "history", "sql-history")
	if err != nil || !exists {
		return h, err
	}
	if err = s.open(ctx, r, &h); err != nil {
		return History{}, err
	}
	if h.Revision != r.Revision || h.Epoch == 0 || !validHistorySettings(h.Settings) {
		return History{}, errors.New("invalid SQL history; original record retained")
	}
	return h, nil
}
func (s *Store) LoadHistory(ctx context.Context, now time.Time) (History, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.loadHistory(ctx)
	if err == nil {
		count := len(h.Entries)
		h.Prune(now)
		if count != len(h.Entries) {
			expected := h.Revision
			h.Revision++
			var r Record
			r, err = s.seal(ctx, Record{ID: "sql-history", Kind: "history", Revision: h.Revision}, h)
			if err == nil {
				err = s.conditional(ctx, r, expected)
			}
		}
	}
	return h, err
}

// Prune preserves complete entries, dropping oldest records at a documented
// 32 MiB aggregate bound instead of truncating SQL into a replayable fragment.
func (h *History) Prune(now time.Time) {
	sort.SliceStable(h.Entries, func(i, j int) bool { return h.Entries[i].Started.After(h.Entries[j].Started) })
	cutoff := now.AddDate(0, 0, -h.Settings.RetentionDays)
	entries := make([]HistoryEntry, 0, min(len(h.Entries), h.Settings.MaxEntries))
	bytes := 0
	for _, entry := range h.Entries {
		size := HistoryEntrySize(entry)
		if entry.Started.Before(cutoff) || len(entries) >= h.Settings.MaxEntries || bytes+size > HistoryMaxBytes {
			continue
		}
		entries = append(entries, entry)
		bytes += size
	}
	h.Entries = entries
}

// HistoryEntrySize bounds both persisted and pending operation metadata.
func HistoryEntrySize(entry HistoryEntry) int {
	return len(entry.ID) + len(entry.SQL) + len(entry.Error) + len(entry.Database) + len(entry.Object) + len(entry.Source) + len(entry.State) + 256
}
func ValidateHistoryEntry(entry HistoryEntry) error {
	if entry.ID == "" || entry.ProfileID <= 0 || entry.Started.IsZero() || entry.Duration < 0 || len(entry.SQL) > 8<<20 || len(entry.Error) > 1<<20 || len(entry.Database) > 64<<10 || len(entry.Object) > 64<<10 || len(entry.Source) > 256 {
		return ErrInvalidHistoryEntry
	}
	switch entry.State {
	case "success", "error", "cancelled", "session_lost", "uncertain", "partial", "pending":
	default:
		return ErrInvalidHistoryEntry
	}
	if strings.TrimSpace(entry.SQL) == "" {
		return ErrInvalidHistoryEntry
	}
	return nil
}

// ApplyHistory publishes a mutation at the caller's revision. Authenticating
// the old record and an atomic conditional write prevent stale/wrong-key
// stores from replacing another process's history or resurrecting cleared SQL.
func (s *Store) ApplyHistory(ctx context.Context, expected uint64, change HistoryChange, now time.Time) (History, error) {
	return s.ApplyHistoryBatch(ctx, expected, []HistoryChange{change}, now)
}

// ApplyHistoryBatch authenticates once and conditionally publishes the entire
// ordered batch once. Invalid entries or conflicts leave the original intact.
func (s *Store) ApplyHistoryBatch(ctx context.Context, expected uint64, changes []HistoryChange, now time.Time) (History, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.loadHistory(ctx)
	if err != nil {
		return History{}, err
	}
	if h.Revision != expected {
		return History{}, ErrStaleRevision
	}
	if len(changes) == 0 || len(changes) > 256 {
		return History{}, errors.New("invalid SQL history batch")
	}
	if h.ProfileEpochs == nil {
		h.ProfileEpochs = map[int64]uint64{}
	}
	bytes := 0
	for _, change := range changes {
		bytes += HistoryEntrySize(change.Entry)
		if bytes > HistoryMaxBytes {
			return History{}, ErrInvalidHistoryEntry
		}
		if err := applyHistoryChange(&h, change); err != nil {
			return History{}, err
		}
	}
	h.Prune(now)
	h.Revision++
	r, err := s.seal(ctx, Record{ID: "sql-history", Kind: "history", Revision: h.Revision}, h)
	if err != nil {
		return History{}, err
	}
	if err = s.conditional(ctx, r, expected); err != nil {
		return History{}, err
	}
	return h, nil
}
func applyHistoryChange(h *History, change HistoryChange) error {
	switch change.Kind {
	case "append":
		entry := change.Entry
		if !h.Settings.Enabled || h.Token(entry.ProfileID) != change.Token {
			return ErrHistoryChanged
		}
		if err := ValidateHistoryEntry(entry); err != nil {
			return err
		}
		for _, old := range h.Entries {
			if old.ID == entry.ID {
				return errors.New("SQL execution was already recorded")
			}
		}
		h.Entries = append([]HistoryEntry{entry}, h.Entries...)
	case "clear":
		if change.ProfileID < 0 {
			return ErrQueryScope
		}
		if change.ProfileID == 0 {
			h.Epoch++
			h.Entries = nil
			h.ProfileEpochs = map[int64]uint64{}
		} else {
			h.ProfileEpochs[change.ProfileID] = h.Token(change.ProfileID).ProfileEpoch + 1
			kept := h.Entries[:0]
			for _, entry := range h.Entries {
				if entry.ProfileID != change.ProfileID {
					kept = append(kept, entry)
				}
			}
			h.Entries = kept
		}
	case "settings":
		if !validHistorySettings(change.Settings) {
			return errors.New("invalid SQL history retention settings")
		}
		if h.Settings.Enabled != change.Settings.Enabled {
			h.Epoch++
		}
		h.Settings = change.Settings
	case "delete":
		found := false
		kept := h.Entries[:0]
		for _, entry := range h.Entries {
			if entry.ID == change.ID && entry.ProfileID == change.ProfileID {
				found = true
			} else {
				kept = append(kept, entry)
			}
		}
		if !found {
			return errors.New("SQL history entry no longer exists")
		}
		h.Entries = kept
	default:
		return errors.New("invalid SQL history operation")
	}
	return nil
}

package sqlworkspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/locales"
)

const historyPendingLimit = 10000
const historyReceiptLimit = 256
const historyErrorLimit = 64 << 10

type historyExecution struct {
	entry queries.HistoryEntry
	out   chan historyReceipt
	// Binding and reservation belong to the UI; workers only read entry/out.
	token    queries.HistoryToken
	bound    bool
	reserved int
}
type historyReceipt struct {
	entries []queries.HistoryEntry
	dropped uint64
}
type historyIO struct {
	snapshot queries.History
	err      error
}
type historyState struct {
	controlConflict                                              bool
	loaded, open, settingsOpen, clearOpen, discardOpen, exitOpen bool
	scope                                                        int64
	snapshot                                                     queries.History
	pending                                                      <-chan historyIO
	loading                                                      bool
	pendingCount                                                 int
	queue                                                        []queries.HistoryChange
	receipts                                                     []*historyExecution
	err                                                          string
	dropped                                                      uint64
	browsers                                                     map[int64]*historyBrowser
	settings                                                     queries.HistorySettings
	openEntry                                                    func(queries.HistoryEntry)
	exitNext                                                     func(ui.Services)
}
type historyBrowser struct {
	search, state, source, age, selected string
	profile                              int64
	row                                  int
	list                                 ui.ListState
}

func (s *State) loadHistory() {
	if s.persistence.closed || s.persistence.store == nil || s.history.pending != nil {
		return
	}
	store, invalidate := s.persistence.store, s.Invalidate
	out := make(chan historyIO, 1)
	s.history.pending, s.history.loading = out, true
	go func() {
		h, err := store.LoadHistory(context.Background(), time.Now())
		out <- historyIO{h, err}
		if invalidate != nil {
			invalidate()
		}
	}()
}
func (s *State) projectedHistory() queries.History {
	h := s.history.snapshot.Clone()
	for _, change := range s.history.queue {
		switch change.Kind {
		case "clear":
			if change.ProfileID == 0 {
				h.Epoch++
				h.ProfileEpochs = map[int64]uint64{}
			} else {
				h.ProfileEpochs[change.ProfileID] = h.Token(change.ProfileID).ProfileEpoch + 1
			}
		case "settings":
			if h.Settings.Enabled != change.Settings.Enabled {
				h.Epoch++
			}
			h.Settings = change.Settings
		}
	}
	return h
}
func (s *State) historyBufferedSize() (int, int) {
	count, size := len(s.history.queue), 0
	for _, change := range s.history.queue {
		size += queries.HistoryEntrySize(change.Entry)
	}
	for _, job := range s.history.receipts {
		count++
		size += job.reserved
	}
	return count, size
}
func (s *State) beginHistoryEntry(entry queries.HistoryEntry) *historyExecution {
	if !s.HistoryAvailable() || entry.ProfileID <= 0 {
		return nil
	}
	h := s.projectedHistory()
	if s.history.loaded && !h.Settings.Enabled {
		return nil
	}
	size := queries.HistoryEntrySize(entry) + historyErrorLimit
	if entry.Source == "file" {
		size = (8 << 20) + historyErrorLimit
	}
	count, bytes := s.historyBufferedSize()
	if len(entry.Database) > 64<<10 || len(entry.Object) > 64<<10 || len(entry.Source) > 256 {
		s.history.dropped++
		return nil
	}
	if len(entry.SQL) > 8<<20 || len(s.history.receipts) >= historyReceiptLimit || count >= historyPendingLimit || bytes+size > queries.HistoryMaxBytes {
		s.history.dropped++
		return nil
	}
	if entry.Started.IsZero() {
		entry.Started = time.Now()
	}
	entry.ID = queries.NewID()
	job := &historyExecution{entry: entry, token: h.Token(entry.ProfileID), bound: s.history.loaded, reserved: size, out: make(chan historyReceipt, 1)}
	s.history.receipts = append(s.history.receipts, job)
	return job
}
func (s *State) beginHistory(d *Document, source, sql string) *historyExecution {
	if d == nil {
		return nil
	}
	return s.beginHistoryEntry(queries.HistoryEntry{ProfileID: d.profileID, Database: d.database, Source: source, SQL: sql})
}

// BeginActivity adapts a neutral injected recorder to the same encrypted log.
// Only this UI method captures tokens. The returned worker callback touches
// the captured entry and private channel, never the State or another scope.
func (s *State) BeginActivity(request contract.ActivityRequest) func(contract.ActivityResult) {
	job := s.beginHistoryEntry(queries.HistoryEntry{ProfileID: request.ProfileID, Generation: request.Generation, Database: request.Database, Object: request.Object, Source: request.Source, SQL: request.SQL, Summary: request.Summary, Started: request.Started})
	if job == nil {
		return nil
	}
	entry, out := job.entry, job.out
	return func(result contract.ActivityResult) {
		entry.Duration = time.Since(entry.Started)
		entry.State, entry.Error, entry.Rows, entry.AffectedRows = result.State, boundedHistoryError(result.Error), result.Rows, result.AffectedRows
		out <- historyReceipt{entries: []queries.HistoryEntry{entry}}
	}
}
func boundedHistoryError(value string) string {
	if len(value) > historyErrorLimit {
		return value[:historyErrorLimit-3] + "…"
	}
	return value
}

// Workers use only their captured ticket and private buffered channel. This
// receipt survives a tab close/disconnect, but cannot access documents or a
// newer modal/profile. The UI is the sole consumer and storage scheduler.
func completeHistory(job *historyExecution, result contract.SQLResult, err error, executed bool) {
	if job == nil {
		return
	}
	r := historyReceipt{}
	if executed {
		entry := job.entry
		entry.Duration = time.Since(entry.Started)
		entry.State = "success"
		if err != nil {
			entry.State = "error"
			entry.Error = boundedHistoryError(err.Error())
			if errors.Is(err, context.Canceled) {
				entry.State = "cancelled"
			}
			if errors.Is(err, contract.ErrRevoked) || errors.Is(err, contract.ErrSQLSessionLost) {
				entry.State = "session_lost"
			}
		}
		for _, set := range result.ResultSets() {
			entry.Rows += int64(len(set.Rows))
			entry.AffectedRows += set.AffectedRows
		}
		if entry.Source == "file" {
			bytes := 0
			for i, step := range result.Steps {
				if step.State == "not_executed" && step.Error == "" {
					continue
				}
				v := entry
				v.ID = fmt.Sprintf("%s/%d", entry.ID, i)
				v.SQL, v.Error = step.SQL, boundedHistoryError(step.Error)
				if step.State == "executed" {
					v.State = "success"
				} else if step.State == "uncertain" {
					v.State = "uncertain"
				} else {
					v.State = "error"
				}
				v.Rows, v.AffectedRows = 0, 0
				size := queries.HistoryEntrySize(v)
				if len(r.entries) >= historyReceiptLimit || bytes+size > (8<<20)+historyErrorLimit {
					r.dropped++
					continue
				}
				bytes += size
				r.entries = append(r.entries, v)
			}
		} else {
			r.entries = []queries.HistoryEntry{entry}
		}
	}
	job.out <- r
}

func (s *State) consumeHistory() {
	if s.persistence.closed {
		return
	}
	h := &s.history
	if h.pending != nil {
		select {
		case result := <-h.pending:
			h.pending = nil
			loading := h.loading
			h.loading = false
			if result.err != nil {
				h.err = result.err.Error()
				h.pendingCount = 0
			} else {
				if loading && result.snapshot.Revision != h.snapshot.Revision {
					for _, change := range h.queue {
						if change.Kind != "append" {
							h.controlConflict = true
							break
						}
					}
				}
				h.snapshot, h.loaded, h.err = result.snapshot, true, ""
				if h.controlConflict {
					h.err = locales.Text("历史已由其它进程修改；请放弃待执行的清除或设置，再重新操作。")
				}
				if !loading && len(h.queue) > 0 {
					clear(h.queue[:h.pendingCount])
					h.queue = h.queue[h.pendingCount:]
					h.pendingCount = 0
				}
				for _, browser := range h.browsers {
					found := false
					for _, entry := range h.snapshot.Entries {
						if entry.ID == browser.selected {
							found = true
							break
						}
					}
					if !found {
						browser.selected = ""
						browser.row = -1
						browser.list.Selection = nil
					}
				}
			}
		default:
		}
	}
	if h.loaded {
		kept := h.receipts[:0]
		for _, job := range h.receipts {
			if !job.bound {
				job.token = h.snapshot.Token(job.entry.ProfileID)
				job.bound = true
			}
			select {
			case receipt := <-job.out:
				h.dropped += receipt.dropped
				for _, entry := range receipt.entries {
					if queries.ValidateHistoryEntry(entry) != nil {
						h.dropped++
						continue
					}
					// Completed receipts replace their reservations, keeping the aggregate bounded.
					h.queue = append(h.queue, queries.HistoryChange{Kind: "append", Entry: entry, Token: job.token})
				}
			default:
				kept = append(kept, job)
			}
		}
		clear(h.receipts[len(kept):])
		h.receipts = kept
		s.trimHistoryQueue()
	}
	if h.pending != nil || !h.loaded || h.err != "" || len(h.queue) == 0 || s.persistence.store == nil {
		return
	}
	// A successful clear/disable revokes only older tickets. Suppress them
	// without treating an intentional deletion as a failed storage write.
	for len(h.queue) > 0 && h.queue[0].Kind == "append" && (!h.snapshot.Settings.Enabled || h.queue[0].Token != h.snapshot.Token(h.queue[0].Entry.ProfileID)) {
		h.queue[0] = queries.HistoryChange{}
		h.queue = h.queue[1:]
	}
	if len(h.queue) == 0 {
		return
	}
	count := 1
	if h.queue[0].Kind == "append" {
		size := queries.HistoryEntrySize(h.queue[0].Entry)
		for count < len(h.queue) && count < 64 && h.queue[count].Kind == "append" {
			change := h.queue[count]
			if !h.snapshot.Settings.Enabled || change.Token != h.snapshot.Token(change.Entry.ProfileID) {
				break
			}
			size += queries.HistoryEntrySize(change.Entry)
			if size > 4<<20 {
				break
			}
			count++
		}
	}
	changes := append([]queries.HistoryChange(nil), h.queue[:count]...)
	expected, store, invalidate := h.snapshot.Revision, s.persistence.store, s.Invalidate
	out := make(chan historyIO, 1)
	h.pending, h.pendingCount = out, count
	go func() {
		snapshot, err := store.ApplyHistoryBatch(context.Background(), expected, changes, time.Now())
		out <- historyIO{snapshot, err}
		if invalidate != nil {
			invalidate()
		}
	}()
}
func (s *State) trimHistoryQueue() {
	h := &s.history
	count, bytes := s.historyBufferedSize()
	for (count > historyPendingLimit || bytes > queries.HistoryMaxBytes) && len(h.queue) > h.pendingCount {
		// Drop the newest append, preserving retained entries and queued user actions.
		index := -1
		for i := len(h.queue) - 1; i >= h.pendingCount; i-- {
			if h.queue[i].Kind == "append" {
				index = i
				break
			}
		}
		if index < 0 {
			break
		}
		bytes -= queries.HistoryEntrySize(h.queue[index].Entry)
		count--
		h.dropped++
		copy(h.queue[index:], h.queue[index+1:])
		h.queue[len(h.queue)-1] = queries.HistoryChange{}
		h.queue = h.queue[:len(h.queue)-1]
	}
}

// DiscardPendingHistory never clears persisted records. It revokes receipt
// channels from this session, so later completions cannot recreate the queue.
func (s *State) DiscardPendingHistory() bool {
	h := &s.history
	if h.pending != nil {
		return false
	}
	h.queue = nil
	h.receipts = nil
	h.controlConflict = false
	return true
}

func (s *State) HistoryAvailable() bool           { return s.persistence.store != nil && !s.persistence.closed }
func (s *State) HistorySnapshot() queries.History { return s.history.snapshot.Clone() }
func (s *State) HistoryPending() bool {
	return s.history.pending != nil || len(s.history.queue) > 0 || !s.history.loaded && len(s.history.receipts) > 0
}
func (s *State) OpenHistory(profile int64, open func(queries.HistoryEntry)) {
	if !s.HistoryAvailable() {
		return
	}
	s.history.scope, s.history.open, s.history.openEntry = profile, true, open
	s.history.settingsOpen, s.history.clearOpen = false, false
	if s.history.browsers == nil {
		s.history.browsers = map[int64]*historyBrowser{}
	}
	if s.history.browsers[profile] == nil {
		s.history.browsers[profile] = &historyBrowser{profile: profile, row: -1, state: "all", source: "all", age: "all"}
	}
}
func (s *State) ClearHistory(profile int64) {
	if !s.history.loaded || s.history.err != "" || profile < 0 || len(s.history.queue) >= historyPendingLimit {
		return
	}
	s.history.queue = append(s.history.queue, queries.HistoryChange{Kind: "clear", ProfileID: profile})
}
func (s *State) SaveHistorySettings(settings queries.HistorySettings) {
	if !s.history.loaded || s.history.err != "" || len(s.history.queue) >= historyPendingLimit {
		return
	}
	s.history.queue = append(s.history.queue, queries.HistoryChange{Kind: "settings", Settings: settings})
}
func (s *State) historyEntries(b *historyBrowser) []queries.HistoryEntry {
	var entries []queries.HistoryEntry
	cutoff := time.Time{}
	if b.age == "day" {
		cutoff = time.Now().Add(-24 * time.Hour)
	} else if b.age == "week" {
		cutoff = time.Now().Add(-7 * 24 * time.Hour)
	}
	search := strings.ToLower(strings.TrimSpace(b.search))
	for _, entry := range s.history.snapshot.Entries {
		if b.profile != 0 && entry.ProfileID != b.profile || b.state != "all" && entry.State != b.state || b.source != "all" && entry.Source != b.source || entry.Started.Before(cutoff) {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(entry.SQL+"\n"+entry.Database+"\n"+entry.Error), search) {
			continue
		}
		entries = append(entries, entry)
	}
	return entries
}

func (s *State) guardHistoryClose(next func(ui.Services)) bool {
	if !s.HistoryPending() {
		return false
	}
	if s.history.err != "" {
		s.history.exitOpen = true
		s.history.exitNext = next
	} else {
		s.persistence.awaitClose = next
	}
	return true
}

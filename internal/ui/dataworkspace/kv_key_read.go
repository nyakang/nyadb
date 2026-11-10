package dataworkspace

import (
	"context"
	"errors"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type kvKeyReadResult struct {
	read *contract.KVKeyRead
	err  error
}
type kvKeyReadOperation struct {
	page       *contract.DataPage
	generation uint64
	profileID  int64
	history    bool
	canceled   bool
	cancel     context.CancelFunc
	out        <-chan kvKeyReadResult
	finished   <-chan struct{}
}

func (s *State) kvKeyReadActive(d *Document) bool {
	return d != nil && !d.closed && s.documents[d.id] == d && d.profileID == s.session().ProfileID && (s.deps.KVReadActive == nil || s.deps.KVReadActive(d))
}
func (s *State) kvKeyReadReady(d *Document, row int) bool {
	if !s.kvKeyReadActive(d) || s.deps.OpenKVEvents == nil || s.runtimeFor(d).CaptureKVKeyRead == nil || d.keyRead != nil || d.loading || d.readRetired || d.edit.Busy || d.edit.Uncertain != nil || len(d.edit.Drafts) > 0 || d.transfer.Busy || d.transfer.Uncertain || dataTransactionUnavailable(d) {
		return false
	}
	session := s.sessionFor(d)
	return session.Connected && session.ProfileID == d.profileID && session.Generation == d.generation && d.page != nil && d.page.Object == d.object && d.page.CurrentCursor != nil && row >= 0 && row < len(d.page.Rows) && row < len(d.page.RowSnapshots) && d.page.RowSnapshots[row] != nil
}

// OpenKeyEvents captures only the original complete row and signed page cursor.
// The adapter verifies scope and grants on its own original worker channel.
func (s *State) OpenKeyEvents(services ui.Services, d *Document, row int, history bool) {
	if !s.kvKeyReadReady(d, row) {
		return
	}
	request := contract.KVKeyReadRequest{Snapshot: cloneSnapshot(d.page.RowSnapshots[row]), Cursor: cloneCursor(d.page.CurrentCursor)}
	runtime := s.runtimeFor(d)
	profile, generation, database, table := d.profileID, d.generation, d.database, d.table
	ctx, cancel := context.WithCancel(context.Background())
	out, finished := make(chan kvKeyReadResult, 1), make(chan struct{})
	d.keyRead = &kvKeyReadOperation{page: d.page, generation: generation, profileID: profile, history: history, cancel: cancel, out: out, finished: finished}
	d.keyReadError = ""
	go func() {
		defer close(finished)
		defer cancel()
		read, err := runtime.CaptureKVKeyRead(ctx, profile, database, table, request.Clone())
		if read != nil {
			copy := read.Clone()
			read = &copy
		}
		if err == nil {
			err = ctx.Err()
		}
		out <- kvKeyReadResult{read: read, err: err}
		services.Invalidate()
	}()
}
func (s *State) cancelKVKeyRead(d *Document) {
	if d != nil && d.keyRead != nil && !d.keyRead.canceled {
		d.keyRead.canceled = true
		d.keyRead.cancel()
	}
}
func (s *State) consumeKVKeyReads(services ui.Services) {
	for _, d := range s.documents {
		if !s.kvKeyReadActive(d) || d.keyRead == nil {
			continue
		}
		select {
		case result := <-d.keyRead.out:
			op := d.keyRead
			d.keyRead = nil
			op.cancel()
			if op.canceled {
				continue
			}
			if result.err != nil {
				if !errors.Is(result.err, context.Canceled) {
					d.keyReadError = locales.Text("无法打开原键事件") + ": " + result.err.Error()
				}
				continue
			}
			session := s.sessionFor(d)
			read := result.read
			if d.page != op.page || d.loading || d.readRetired || d.generation != op.generation || session.Generation != op.generation || session.ProfileID != op.profileID || !session.Connected || read == nil || read.Scope.Namespace.ProfileID != op.profileID || read.Scope.Generation != op.generation || read.Scope.Namespace != d.object.Namespace() || read.Revision <= 0 {
				d.keyReadError = locales.Text("原键读取范围已更改，请重新选择")
				continue
			}
			if s.deps.OpenKVEvents == nil || !s.deps.OpenKVEvents(services, read.Clone(), op.history) {
				d.keyReadError = locales.Text("原键读取范围已更改，请重新选择")
			}
		default:
		}
	}
}
func (s *State) kvKeyReadView(c *ui.Context, d *Document) {
	if d.keyRead != nil {
		ui.Row(c).FillWidth().Gap(8).Padding(3, 8).Children(func() {
			ui.Text(c, locales.Text("正在核对原键读取范围")).TextColor(theme.Colors(c).Muted).SingleLine().Grow(1)
			if ui.Button(c, locales.Text("取消原键读取")).Disabled(d.keyRead.canceled).Clicked() {
				s.cancelKVKeyRead(d)
			}
		})
		c.AnimationFrame()
	}
	if d.keyReadError != "" {
		runes := []rune(d.keyReadError)
		text := string(runes[:min(len(runes), 1024)])
		ui.Text(c, text).FillWidth().Padding(3, 8).SingleLine().Tooltip(text).TextColor(theme.Colors(c).Warning)
	}
}
func (s *State) kvKeyReadMenu(c *ui.Context, d *Document, m *ui.Menu, row int) {
	if s.deps.OpenKVEvents == nil || s.runtimeFor(d).CaptureKVKeyRead == nil {
		return
	}
	ready := s.kvKeyReadReady(d, row)
	m.Separator()
	if m.Item(locales.Text("观察原键事件")).Disabled(!ready).Chosen() {
		s.OpenKeyEvents(c.Services(), d, row, false)
	}
	if m.Item(locales.Text("原键固定历史")).Disabled(!ready).Chosen() {
		s.OpenKeyEvents(c.Services(), d, row, true)
	}
}

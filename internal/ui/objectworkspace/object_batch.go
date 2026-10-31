package objectworkspace

import (
	"context"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

type objectBatch struct {
	sessionGeneration uint64

	profile             int64
	generation          uint64
	items               []mysqlsvc.ObjectBatchItem
	open, busy, blocked bool
	result              <-chan []mysqlsvc.ObjectBatchResult
	outcome             []mysqlsvc.ObjectBatchResult
	cancel              context.CancelFunc
}

func (s *State) BeginObjectBatch(profile int64, refs []mysqlsvc.ObjectIdentity, action string) {
	if !s.capabilities(profile).DesignTables || s.deps.BatchExecutor == nil || !s.sessionFor(profile).Connected || len(refs) == 0 {
		return
	}
	if s.batch != nil && s.batch.busy {
		return
	}
	gen := s.sessionFor(profile).Generation
	if s.deps.RuntimeGeneration != nil {
		gen = s.deps.RuntimeGeneration(profile)
	}
	b := &objectBatch{profile: profile, generation: gen, sessionGeneration: s.sessionFor(profile).Generation, open: true}
	used := map[string]bool{}
	for _, ref := range refs {
		item := mysqlsvc.ObjectBatchItem{Ref: ref, Action: action}
		if action == "copy" {
			base := s.NextObjectName(profile, ref.Name+"_copy")
			item.Destination = base
			for n := 2; used[strings.ToLower(item.Destination)]; n++ {
				item.Destination = fmt.Sprintf("%s_%d", base, n)
			}
			used[strings.ToLower(item.Destination)] = true
		}
		b.items = append(b.items, item)
	}
	s.batch = b
}
func (s *State) consumeBatch(services ui.Services) {
	b := s.batch
	if b == nil || b.result == nil || b.profile != s.session().ProfileID {
		return
	}
	select {
	case result := <-b.result:
		b.result = nil
		b.busy = false
		b.blocked = true
		if b.cancel != nil {
			b.cancel()
			b.cancel = nil
		}
		b.outcome = result
		for _, r := range result {
			if s.deps.ObjectChanged != nil {
				s.deps.ObjectChanged(services, ObjectEvent{ProfileID: b.profile, Generation: b.sessionGeneration, Ref: r.Item.Ref, Action: r.Item.Action, Outcome: r.Outcome})
			}
		}
	default:
	}
}
func (s *State) batchModal(c *ui.Context) {
	b := s.batch
	if b == nil {
		return
	}
	if !b.open {
		if b.busy {
			b.open = true
		} else {
			s.batch = nil
			return
		}
	}
	p := theme.Colors(c)
	ui.Modal(c, &b.open, func() {
		ui.Column(c).Width(620).MaxHeight(600).Padding(16).Gap(8).Background(p.Header).Children(func() {
			ui.Text(c, "确认所选对象操作").Bold()
			ui.Text(c, "按列表顺序执行，遇错停止；已完成和结果不确定的步骤不会自动重放。")
			ui.Scroll(c).Grow(1).Children(func() {
				ui.Column(c).Gap(4).Children(func() {
					for i, item := range b.items {
						label := fmt.Sprintf("%d · %s · %s.%s", i+1, item.Action, item.Ref.Database, item.Ref.Name)
						if item.Destination != "" {
							label += " → " + item.Destination
						}
						if i < len(b.outcome) {
							label += " · " + b.outcome[i].Outcome.State
							if e := b.outcome[i].Outcome.Error; e != nil {
								label += " · " + e.Error()
							}
						}
						ui.Text(c, label)
					}
				})
			})
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "关闭").Disabled(b.busy).Clicked() {
					s.batch = nil
				}
				if ui.Button(c, "取消操作").Disabled(!b.busy || b.cancel == nil).Clicked() {
					b.cancel()
				}
				if ui.PrimaryButton(c, "确认执行所选对象操作").Disabled(b.busy || b.blocked).Clicked() {
					next := func(services ui.Services) {
						if b != s.batch || b.busy || !s.sessionFor(b.profile).Connected || s.sessionFor(b.profile).Generation != b.sessionGeneration {
							return
						}
						b.busy = true
						ctx, cancel := context.WithCancel(context.Background())
						b.cancel = cancel
						ch := make(chan []mysqlsvc.ObjectBatchResult, 1)
						b.result = ch
						profile, generation, items, executor := b.profile, b.generation, append([]mysqlsvc.ObjectBatchItem(nil), b.items...), s.deps.BatchExecutor
						go func() { ch <- executor(ctx, profile, generation, items); services.Invalidate() }()
					}
					var guard func(int, ui.Services)
					guard = func(i int, services ui.Services) {
						if b != s.batch {
							return
						}
						if i >= len(b.items) {
							next(services)
							return
						}
						item := b.items[i]
						resume := func(services ui.Services) { guard(i+1, services) }
						if s.deps.BeforeDDL != nil && s.deps.BeforeDDL(services, b.profile, item.Ref.Database, item.Ref.Name, resume) {
							return
						}
						resume(services)
					}
					guard(0, c.Services())
				}
			})
		})
	})
}

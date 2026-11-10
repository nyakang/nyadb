package shell

import (
	"context"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type workspaceReply struct {
	Items []queries.NamedWorkspace
	Err   error
	Open  *queries.Workspace
}
type workspaceManager struct {
	store             *queries.Store
	open, busy        bool
	name, choice, err string
	items             []queries.NamedWorkspace
	out               <-chan workspaceReply
	cancel            context.CancelFunc
}

func (s *State) workspaceJob(work func(context.Context) workspaceReply) {
	w := &s.workspaces
	if w.busy || w.store == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.busy = true
	out := make(chan workspaceReply, 1)
	w.out = out
	invalidate := s.editServices.Invalidate
	go func() {
		r := work(ctx)
		out <- r
		if invalidate != nil {
			invalidate()
		}
	}()
}
func (s *State) openWorkspaces() {
	if s.workspaces.store == nil {
		return
	}
	s.workspaces.open = true
	s.reloadWorkspaces()
}
func (s *State) reloadWorkspaces() {
	store := s.workspaces.store
	s.workspaceJob(func(ctx context.Context) workspaceReply {
		items, err := store.ListWorkspaces(ctx)
		return workspaceReply{Items: items, Err: err}
	})
}
func (s *State) consumeWorkspaces() {
	w := &s.workspaces
	if w.out == nil {
		return
	}
	select {
	case r := <-w.out:
		w.out = nil
		w.busy = false
		if w.cancel != nil {
			w.cancel()
			w.cancel = nil
		}
		if r.Err != nil {
			w.err = locales.Text("工作区操作失败；原现场保留") + ": " + r.Err.Error()
			return
		}
		w.err = ""
		w.items = r.Items
		if r.Open != nil {
			s.switchWorkspace(*r.Open)
		}
	default:
	}
}
func (s *State) switchWorkspace(target queries.Workspace) {
	if err := validateWorkspace(target); err != nil {
		s.workspaces.err = locales.Text("工作区操作失败；原现场保留") + ": " + err.Error()
		s.status = s.workspaces.err
		return
	}
	target = queries.CloneWorkspace(target)
	// Designer guards are rendered with their document. Release this modal so
	// it cannot cover the leave decision while the scene is still unchanged.
	s.workspaces.open = false
	var finish func(ui.Services)
	finish = func(ui.Services) {
		if s.kvWorkspace.GuardWorkspaceClose(finish) {
			return
		}
		if s.guardTabsLeave(func(t *tab) bool { return t.kind != tabObjects }, finish) {
			return
		}
		current := s.workspaceSnapshot()
		s.sqlWorkspace.FlushWorkspace(current, func(ui.Services) {
			for _, t := range s.tabs[1:] {
				s.disposeTab(t)
			}
			s.tabs = s.tabs[:1]
			s.activeTab = 0
			s.restoreWorkspace(target, false)
			s.workspaces.open = false
			s.observeSQLDocuments()
		})
	}
	finish(s.editServices)
}
func (s *State) workspaceManagerView(c *ui.Context) {
	w := &s.workspaces
	theme.Modal(c, &w.open, func() {
		ui.Column(c).Width(600).MaxWidthPercent(95).Gap(10).Children(func() {
			ui.Text(c, locales.Text("命名工作区")).Bold()
			ui.Text(c, locales.Text("保存页签、布局和 SQL 文本；恢复后需手动连接。"))
			choices := []string{}
			for _, item := range w.items {
				choices = append(choices, item.Name)
			}
			ui.Select(c, &w.choice, choices).Label(locales.Text("选择工作区")).FillWidth().Disabled(w.busy)
			ui.TextInput(c, &w.name).Label(locales.Text("工作区名称")).FillWidth().Disabled(w.busy)
			if w.err != "" {
				ui.Text(c, w.err)
			}
			var selected *queries.NamedWorkspace
			for i := range w.items {
				if w.items[i].Name == w.choice {
					selected = &w.items[i]
					break
				}
			}
			duplicate := false
			for _, item := range w.items {
				duplicate = duplicate || strings.EqualFold(item.Name, strings.TrimSpace(w.name))
			}
			ui.Row(c).Wrap().Gap(6).Children(func() {
				if ui.Button(c, locales.Text("关闭")).Clicked() {
					w.open = false
				}
				if ui.Button(c, locales.Text("保存新工作区")).Disabled(w.busy || strings.TrimSpace(w.name) == "" || duplicate).Clicked() {
					snapshot := queries.CloneWorkspace(s.workspaceSnapshot())
					value := queries.NamedWorkspace{ID: "workspace/" + queries.NewID(), Name: w.name, Revision: 1, Workspace: snapshot}
					store := w.store
					s.workspaceJob(func(ctx context.Context) workspaceReply {
						err := store.SaveNamedWorkspace(ctx, value)
						if err != nil {
							return workspaceReply{Err: err}
						}
						items, err := store.ListWorkspaces(ctx)
						return workspaceReply{Items: items, Err: err}
					})
				}
				if ui.Button(c, locales.Text("替换所选工作区")).Disabled(w.busy || selected == nil).Clicked() {
					value := *selected
					value.Revision++
					value.Workspace = queries.CloneWorkspace(s.workspaceSnapshot())
					store := w.store
					s.workspaceJob(func(ctx context.Context) workspaceReply {
						err := store.SaveNamedWorkspace(ctx, value)
						if err != nil {
							return workspaceReply{Err: err}
						}
						items, err := store.ListWorkspaces(ctx)
						return workspaceReply{Items: items, Err: err}
					})
				}
				if ui.Button(c, locales.Text("打开工作区")).Disabled(w.busy || selected == nil).Clicked() {
					s.switchWorkspace(selected.Workspace)
				}
				if ui.Button(c, locales.Text("重命名工作区")).Disabled(w.busy || selected == nil || strings.TrimSpace(w.name) == "" || duplicate).Clicked() {
					item := *selected
					name := w.name
					store := w.store
					s.workspaceJob(func(ctx context.Context) workspaceReply {
						err := store.RenameWorkspace(ctx, item.ID, item.Revision, name)
						if err != nil {
							return workspaceReply{Err: err}
						}
						items, err := store.ListWorkspaces(ctx)
						return workspaceReply{Items: items, Err: err}
					})
				}
				if ui.Button(c, locales.Text("删除所选工作区")).Disabled(w.busy || selected == nil).Clicked() {
					item := *selected
					store := w.store
					s.workspaceJob(func(ctx context.Context) workspaceReply {
						err := store.DeleteWorkspace(ctx, item.ID, item.Revision)
						if err != nil {
							return workspaceReply{Err: err}
						}
						items, err := store.ListWorkspaces(ctx)
						return workspaceReply{Items: items, Err: err}
					})
				}
			})
		})
	})
}

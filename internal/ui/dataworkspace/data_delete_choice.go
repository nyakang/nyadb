package dataworkspace

import (
	"fmt"
	"reflect"
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// A choice owns the original selection and complete draft/page evidence. It
// performs no read or write and cannot adopt a later selection or generation.
type dataDeleteChoice struct {
	page           *contract.DataPage
	object         contract.ObjectRef
	generation     uint64
	indices        []int
	snapshot       draftSnapshot
	nodes, inserts int
	open           bool
}

func (s *State) beginDataDeleteChoice(d *Document, indices []int) {
	choice := &dataDeleteChoice{page: d.page, object: d.object, generation: s.sessionFor(d).Generation,
		indices: slices.Clone(indices), snapshot: captureDraft(d), open: true}
	choice.snapshot.Row = -1 // Changing selection cannot redirect the captured rows.
	for _, row := range indices {
		if draft := mysqlDataDraftForRow(d, row); draft != nil && draft.Kind == "insert" {
			choice.inserts++
		} else {
			choice.nodes++
		}
	}
	d.deleteChoice = choice
	d.edit.Busy, d.edit.Err = true, ""
}
func (s *State) cancelDataDeleteChoice(d *Document) {
	if d.deleteChoice == nil {
		return
	}
	d.deleteChoice = nil
	d.edit.Busy = false
	s.clearDataMutationContinuation(d)
}
func (s *State) validDataDeleteChoice(d *Document) bool {
	c := d.deleteChoice
	if c == nil || d.closed || s.documents[d.id] != d || d.page != c.page || d.object != c.object || !s.canUseSession(d) || s.sessionFor(d).Generation != c.generation || !d.allowsDataEdit("delete") || !d.allowsDataEdit("detach_delete") {
		return false
	}
	current := captureDraft(d)
	current.Row = -1
	return reflect.DeepEqual(current, c.snapshot)
}
func (s *State) consumeDataDeleteChoice(d *Document) {
	if d.deleteChoice != nil && !s.validDataDeleteChoice(d) {
		s.cancelDataDeleteChoice(d)
		d.edit.Err = locales.Text("原记录或数据库会话已变化，请重新选择删除记录")
	}
}
func (s *State) chooseDataDeleteKind(services ui.Services, d *Document, kind string) {
	if !dataEditDeletesRow(kind) || !s.validDataDeleteChoice(d) {
		s.cancelDataDeleteChoice(d)
		d.edit.Err = locales.Text("原记录或数据库会话已变化，请重新选择删除记录")
		return
	}
	indices := slices.Clone(d.deleteChoice.indices)
	d.deleteChoice = nil
	d.edit.Busy = false
	s.stageDataDeleteIndices(services, d, indices, kind)
}
func (s *State) dataDeleteChoiceModal(c *ui.Context, d *Document) {
	choice := d.deleteChoice
	if choice == nil {
		return
	}
	if !choice.open {
		s.cancelDataDeleteChoice(d)
		return
	}
	if !s.consumeFor(d) {
		return
	}
	theme.Modal(c, &choice.open, func() {
		ui.Column(c).Width(620).MaxWidthPercent(95).Gap(8).Children(func() {
			ui.Text(c, locales.Text("选择节点删除方式")).FontSize(theme.Font(c, theme.FontSize))
			ui.Text(c, d.database+" · "+d.table)
			ui.Text(c, fmt.Sprintf(locales.Text("%d 个节点，%d 个未提交的新节点"), choice.nodes, choice.inserts))
			ui.Text(c, locales.Text("只删除节点会保留关联关系，有关联关系时将阻止提交。删除节点和关联关系会先预览全部受影响的关系，再等待确认。"))
			if choice.inserts > 0 {
				ui.Text(c, locales.Text("未提交的新节点只会从本地草稿移除；取消将保留全部草稿。"))
			}
			ui.Column(c).Gap(6).Children(func() {
				if ui.Button(c, locales.Text("只删除节点")).FillWidth().Clicked() {
					s.chooseDataDeleteKind(c.Services(), d, "delete")
				}
				if ui.Button(c, locales.Text("删除节点和关联关系")).FillWidth().Clicked() {
					s.chooseDataDeleteKind(c.Services(), d, "detach_delete")
				}
				if ui.Button(c, locales.Text("取消")).FillWidth().Clicked() {
					choice.open = false
				}
			})
		})
	})
	if !choice.open && d.deleteChoice == choice {
		s.cancelDataDeleteChoice(d)
	}
}

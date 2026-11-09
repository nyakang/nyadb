package dataworkspace

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type dataGraphEndpointResult struct {
	endpoints      *contract.DataGraphEndpoints
	source, target string
	err            error
}

// The UI owns the input, original page/drafts and the complete endpoint result.
// A reader captures immutable arguments and publishes only to its private channel.
type dataGraphEndpointChoice struct {
	page           *contract.DataPage
	object         contract.ObjectRef
	generation     uint64
	drafts         draftSnapshot
	source, target string
	observation    *contract.DataGraphEndpoints
	results        <-chan dataGraphEndpointResult
	cancel         context.CancelFunc
	open           bool
	err            string
}

func (s *State) beginDataGraphEndpoints(d *Document) {
	if d.closed || d.object.Kind != "relationship" || !s.canUseSession(d) || s.runtimeFor(d).ReadGraphEndpoints == nil {
		return
	}
	drafts := captureDraft(d)
	drafts.Row = -1
	d.graphEndpoints = &dataGraphEndpointChoice{page: d.page, object: d.object, generation: s.sessionFor(d).Generation, drafts: drafts, open: true}
	d.edit.Busy, d.edit.Err = true, ""
}

func (s *State) validDataGraphEndpoints(d *Document) bool {
	m := d.graphEndpoints
	if m == nil || d.closed || s.documents[d.id] != d || d.page != m.page || d.object != m.object || !s.canUseSession(d) || s.sessionFor(d).Generation != m.generation || !d.allowsDataEdit("insert") || s.runtimeFor(d).ReadGraphEndpoints == nil {
		return false
	}
	drafts := captureDraft(d)
	drafts.Row = -1
	return reflect.DeepEqual(drafts, m.drafts)
}

func (s *State) cancelDataGraphEndpoints(d *Document) {
	m := d.graphEndpoints
	if m == nil {
		return
	}
	m.open = false
	m.observation = nil
	if m.cancel != nil {
		m.cancel()
	}
	s.clearDataMutationContinuation(d)
	// A pending read continues to own Busy until its executor acknowledges.
	if m.results == nil {
		d.graphEndpoints = nil
		d.edit.Busy, d.edit.Cancel = false, nil
	}
}

func (s *State) readDataGraphEndpoints(services ui.Services, d *Document) {
	m := d.graphEndpoints
	if m == nil || m.results != nil || !m.open || !s.validDataGraphEndpoints(d) {
		return
	}
	if m.source == "" || m.target == "" || len(m.source) > 512 || len(m.target) > 512 {
		m.err = locales.Text("请输入完整的源和目标 element ID")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan dataGraphEndpointResult, 1)
	m.results, m.cancel, m.observation, m.err = out, cancel, nil, ""
	d.edit.Cancel = cancel
	reader, profile, database, table := s.runtimeFor(d).ReadGraphEndpoints, d.profileID, d.database, d.table
	source, target := m.source, m.target
	go func() {
		e, err := reader(ctx, profile, database, table, source, target)
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		if err != nil {
			e = nil
		}
		out <- dataGraphEndpointResult{endpoints: contract.CloneDataGraphEndpoints(e), source: source, target: target, err: err}
		services.Invalidate()
	}()
}

func (s *State) consumeDataGraphEndpoints(d *Document) {
	m := d.graphEndpoints
	if m == nil {
		return
	}
	valid := s.validDataGraphEndpoints(d)
	if !valid {
		s.cancelDataGraphEndpoints(d)
	}
	if m.results == nil {
		return
	}
	select {
	case r := <-m.results:
		if m.cancel != nil {
			m.cancel()
		}
		m.cancel, m.results, d.edit.Cancel = nil, nil, nil
		if !m.open || !valid {
			d.graphEndpoints, d.edit.Busy = nil, false
			if !valid {
				d.edit.Err = locales.Text("原记录或数据库会话已变化，请重新读取端点")
			}
			return
		}
		err := r.err
		if err == nil {
			e := r.endpoints
			if e == nil || e.Object != m.object || e.Source.ElementID != r.source || e.Target.ElementID != r.target || e.NativeJSON == "" || e.Token == "" || m.source != r.source || m.target != r.target {
				err = contract.ErrDataConflict
			} else if raw, marshalErr := json.Marshal(e); marshalErr != nil || len(raw) > contract.DataEditMaxBytes {
				err = contract.ErrDataEditLimit
			}
		}
		if err != nil {
			m.err = err.Error()
			return
		}
		m.observation = contract.CloneDataGraphEndpoints(r.endpoints)
	default:
	}
}

func (s *State) stageDataGraphRelationship(services ui.Services, d *Document) {
	m := d.graphEndpoints
	if m == nil || m.results != nil || m.observation == nil || !m.open {
		return
	}
	if !s.validDataGraphEndpoints(d) || m.observation.Source.ElementID != m.source || m.observation.Target.ElementID != m.target {
		s.cancelDataGraphEndpoints(d)
		d.edit.Err = locales.Text("原记录或数据库会话已变化，请重新读取端点")
		return
	}
	e := contract.CloneDataGraphEndpoints(m.observation)
	s.cancelDataGraphEndpoints(d)
	s.stageMySQLDataRecord(services, d, e)
}

func (s *State) dataGraphEndpointsModal(c *ui.Context, d *Document) {
	m := d.graphEndpoints
	if m == nil || !s.consumeFor(d) {
		return
	}
	if !m.open {
		s.cancelDataGraphEndpoints(d)
		return
	}
	theme.Modal(c, &m.open, func() {
		ui.Column(c).Width(640).MaxWidthPercent(95).Gap(6).Children(func() {
			ui.Text(c, locales.Text("创建关系：选择端点")).FontSize(theme.Font(c, theme.FontSize))
			ui.Text(c, d.database+" · "+d.table)
			ui.Text(c, locales.Text("输入源和目标节点的 element ID，读取完整端点后添加草稿。保存时会再次预览并确认。"))
			source, target := m.source, m.target
			ui.Text(c, locales.Text("源节点 element ID"))
			sourceInput := ui.TextInput(c, &m.source).Label(locales.Text("输入源节点 element ID")).Placeholder(locales.Text("源节点 element ID")).FillWidth().Disabled(m.results != nil)
			ui.Text(c, locales.Text("目标节点 element ID"))
			targetInput := ui.TextInput(c, &m.target).Label(locales.Text("输入目标节点 element ID")).Placeholder(locales.Text("目标节点 element ID")).FillWidth().Disabled(m.results != nil)
			valid := func() bool { return !d.closed && d.graphEndpoints == m && m.open }
			theme.BindNativeEdit(c, sourceInput, fmt.Sprintf("data/endpoints/source/%d", d.id), s.deps.EditFocus, s.deps.EditTarget, valid, m.results == nil, false)
			theme.BindNativeEdit(c, targetInput, fmt.Sprintf("data/endpoints/target/%d", d.id), s.deps.EditFocus, s.deps.EditTarget, valid, m.results == nil, false)
			if source != m.source || target != m.target {
				m.observation, m.err = nil, ""
			}
			if m.observation != nil {
				ui.Scroll(c).Height(130).FillWidth().Children(func() {
					ui.Text(c, m.source+" → "+m.object.Name+" → "+m.target)
					raw, _ := json.MarshalIndent(struct{ Source, Target contract.GraphNode }{m.observation.Source, m.observation.Target}, "", "  ")
					ui.Text(c, dataMutationPreview(string(raw))).Label(locales.Text("完整端点观察"))
				})
			}
			if m.err != "" {
				ui.Text(c, m.err).TextColor(theme.Colors(c).Error)
			}
			ui.Row(c).Gap(6).Children(func() {
				if ui.Button(c, locales.Text("读取端点")).Disabled(m.results != nil).Clicked() {
					s.readDataGraphEndpoints(c.Services(), d)
				}
				if ui.Button(c, locales.Text("复制完整端点观察")).Disabled(m.observation == nil).Clicked() {
					raw, _ := json.MarshalIndent(m.observation, "", "  ")
					c.WriteClipboard(string(raw))
				}
			})
			if ui.Button(c, locales.Text("添加关系草稿")).FillWidth().Disabled(m.results != nil || m.observation == nil).Clicked() {
				s.stageDataGraphRelationship(c.Services(), d)
			}
			if ui.Button(c, locales.Text("取消")).FillWidth().Clicked() {
				m.open = false
			}
		})
	})
	if !m.open && d.graphEndpoints == m {
		s.cancelDataGraphEndpoints(d)
	}
}

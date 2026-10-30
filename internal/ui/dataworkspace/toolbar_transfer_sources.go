package dataworkspace

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/locales"
)

type mysqlDataWizardSource struct {
	Name  string
	State *mysqlDataTransferState
}

func mysqlDataCopyTransfer(tr mysqlDataTransferState) mysqlDataTransferState {
	tr.Sources = nil
	tr.Fields = slices.Clone(tr.Fields)
	tr.Mapping = slices.Clone(tr.Mapping)
	tr.MatchColumns = slices.Clone(tr.MatchColumns)
	tr.NewColumns = slices.Clone(tr.NewColumns)
	tr.TargetColumns = slices.Clone(tr.TargetColumns)
	tr.SourceTables = slices.Clone(tr.SourceTables)
	tr.Included = maps.Clone(tr.Included)
	return tr
}

func (s *State) rememberWizardSource(t *Document) {
	tr := &t.transfer
	if len(tr.Sources) == 0 {
		return
	}
	copy := mysqlDataCopyTransfer(*tr)
	name := tr.Path
	if name == "" {
		name = fmt.Sprintf("来源 %d", tr.ActiveSource+1)
	}
	tr.Sources[tr.ActiveSource] = mysqlDataWizardSource{Name: name, State: &copy}
}

func (s *State) switchWizardSource(t *Document, index int) {
	tr := &t.transfer
	if tr.Busy || tr.Submitted || index < 0 || index >= len(tr.Sources) || index == tr.ActiveSource {
		return
	}
	s.rememberWizardSource(t)
	sources, owner, request, gen := tr.Sources, tr.Assets, tr.Request+1, tr.Generation
	if sources[index].State == nil {
		return
	}
	next := mysqlDataCopyTransfer(*sources[index].State)
	next.Sources = sources
	next.ActiveSource = index
	next.Assets = owner
	next.Request = request
	next.Generation = gen
	next.Open = true
	t.transfer = next
}

func (s *State) addWizardSource(t *Document) {
	tr := &t.transfer
	if tr.Busy || tr.Submitted || tr.Input == nil || len(tr.Sources) >= 32 {
		return
	}
	s.rememberWizardSource(t)
	sources, owner, request, gen := tr.Sources, tr.Assets, tr.Request+1, tr.Generation
	sources = append(sources, mysqlDataWizardSource{Name: fmt.Sprintf("来源 %d", len(sources)+1)})
	next := mysqlDataTransferState{Wizard: true, Open: true, Kind: "import", Format: "CSV", Options: defaultMySQLDataSourceOptions(), ContinueOnError: true, ExtendedInsert: true, StatementKB: 1024, Target: "existing", NewName: t.table + "_import", Fields: slices.Clone(t.page.Columns), Included: map[string]bool{}, Sources: sources, ActiveSource: len(sources) - 1, Assets: owner, Request: request, Generation: gen}
	for _, field := range next.Fields {
		next.Included[field] = true
	}
	t.transfer = next
}

func (s *State) removeWizardSource(t *Document) {
	tr := &t.transfer
	if tr.Busy || tr.Submitted || len(tr.Sources) < 2 {
		return
	}
	index := tr.ActiveSource
	if tr.RawSpool != nil {
		tr.RawSpool.Close()
	}
	if tr.MappedSpool != nil {
		tr.MappedSpool.Close()
	}
	sources := slices.Delete(tr.Sources, index, index+1)
	index = min(index, len(sources)-1)
	if sources[index].State == nil {
		return
	}
	next := mysqlDataCopyTransfer(*sources[index].State)
	next.Sources, next.ActiveSource, next.Assets = sources, index, tr.Assets
	next.Request, next.Generation, next.Open = tr.Request+1, tr.Generation, true
	t.transfer = next
}

func (s *State) dataSourceSwitcher(c *ui.Context, t *Document) {
	tr := &t.transfer
	if len(tr.Sources) == 0 {
		return
	}
	ui.Row(c).Gap(8).Children(func() {
		choices := []string{}
		for i, source := range tr.Sources {
			choices = append(choices, fmt.Sprintf("%d · %s", i+1, source.Name))
		}
		choice := choices[tr.ActiveSource]
		mysqlDataRuleSelect(c, &choice, choices, func(x string) string { return x }).Width(420).Disabled(tr.Busy || tr.Submitted)
		if indexText, _, ok := strings.Cut(choice, " · "); ok {
			if index, e := strconv.Atoi(indexText); e == nil && index-1 != tr.ActiveSource {
				s.switchWizardSource(t, index-1)
			}
		}
		if ui.Button(c, locales.Text("添加来源")).Disabled(tr.Busy || tr.Submitted || tr.Input == nil || len(tr.Sources) >= 32).Clicked() {
			s.addWizardSource(t)
		}
		if ui.Button(c, locales.Text("移除来源")).Disabled(tr.Busy || tr.Submitted || len(tr.Sources) < 2).Clicked() {
			s.removeWizardSource(t)
		}
	})
}

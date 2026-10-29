package shell

import (
	"fmt"
	"os"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/locales"
)

type mysqlDataExportResult struct {
	path string
	err  error
}
type mysqlDataExportState struct {
	Busy    bool
	Results <-chan mysqlDataExportResult
}

// Snapshot the bounded selection on the UI thread. The dialog and disk write
// run in a worker, and only the UI thread consumes the resulting status.
func (s *State) exportMySQLDataSelection(c *ui.Context, t *tab, format string) {
	if t.dataExport.Busy {
		return
	}
	names, rows, nulls := mysqlDataSelectedPreview(t)
	if len(names) == 0 || len(rows) == 0 {
		return
	}
	var data string
	switch format {
	case "CSV":
		data = mysqlDataDelimitedPreview(names, rows, ',', true, true)
	case "TSV":
		data = mysqlDataDelimitedPreview(names, rows, '\t', true, true)
	case "JSON":
		data = mysqlDataJSONPreview(names, rows, nulls)
	default:
		return
	}
	results := make(chan mysqlDataExportResult, 1)
	t.dataExport = mysqlDataExportState{Busy: true, Results: results}
	services := c.Services()
	title := locales.Text("保存选区数据预览")
	message := locales.Text("仅保存当前页选区；截断字段包含预览标记")
	extension := strings.ToLower(format)
	defaultPath := t.dataTable + "." + extension
	go func() {
		path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Title: title, Message: message, DefaultPath: defaultPath, Filters: []mygo.FileFilter{{Name: format, Extensions: []string{extension}}}})
		if err == nil && path != "" {
			err = os.WriteFile(path, []byte(data+"\n"), 0600)
		}
		results <- mysqlDataExportResult{path: path, err: err}
		services.Invalidate()
	}()
}
func (s *State) consumeMySQLDataExport(t *tab) {
	if t.dataExport.Results == nil {
		return
	}
	select {
	case result := <-t.dataExport.Results:
		t.dataExport = mysqlDataExportState{}
		if result.err != nil {
			t.dataEdit.Err = locales.Text("保存数据文件失败：") + result.err.Error()
		} else if result.path != "" {
			s.status = fmt.Sprintf(locales.Text("已保存选区预览至 %s"), result.path)
		}
	default:
	}
}

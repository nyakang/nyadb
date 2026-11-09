package dataworkspace

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// Translate insert placeholders when displaying them, leaving actual values
// and the original draft/page evidence untouched when the locale changes.
func (s *State) dataCellPreview(t *Document, page *contract.DataPage, row, source int) contract.DataCell {
	cell := page.Rows[row][source]
	if t.page != page || !s.runtimeFor(t).InsertOmittedValues {
		return cell
	}
	draft := mysqlDataDraftForRow(t, row)
	if draft == nil || draft.Kind != "insert" {
		return cell
	}
	if _, explicit := draft.Values[page.Columns[source]]; explicit {
		return cell
	}
	if draft.GraphEndpoints != nil {
		switch page.Columns[source] {
		case "start_element_id", "end_element_id", "type":
			return cell // Explicit endpoint/type observations are already known.
		}
	}
	cell.Text = locales.Text("未设置")
	if source < len(page.ColumnMetadata) {
		column := page.ColumnMetadata[source]
		if column.Generated || contract.DataColumnGenerated(column) {
			cell.Text = locales.Text("提交后生成")
		}
	}
	return cell
}

// Only draft values for this source column mark a changed cell. A moved or
// hidden column never borrows the state of its former visual neighbour.
func mysqlDataCellModified(t *Document, row, source int) bool {
	if t.page == nil || source < 0 || source >= len(t.page.Columns) {
		return false
	}
	draft := mysqlDataDraftForRow(t, row)
	if draft == nil || draft.Kind != "update" {
		return false
	}
	_, changed := draft.Values[t.page.Columns[source]]
	return changed
}

func mysqlDataDraftMarker(c *ui.Context, t *Document, row int) (string, ui.Color) {
	p := theme.Colors(c)
	if draft := mysqlDataDraftForRow(t, row); draft != nil {
		switch draft.Kind {
		case "insert":
			return "+ ", p.GridInsertedMark
		case "delete", "detach_delete":
			return "− ", p.GridDeletedMark
		default:
			return "* ", p.GridModifiedMark
		}
	}
	return "", p.Muted
}

func mysqlDataModifiedBackground(c *ui.Context, t *Document, row, col int, content ui.Element) {
	if !t.grid.CellSelected(row, col) {
		background := theme.Colors(c).GridModified
		paddingX, paddingY := c.Theme().Space(2.5), c.Theme().Space(1.5)+theme.TableRowExtra(c)/2
		// Paint before the text, extending through native padding. An absolute
		// background sibling paints after normal-flow content and hides glyphs.
		// The native cell clips this fill and draws its grid/cursor above it.
		content.Draw(func(p *ui.Painter, r ui.Rect) {
			p.Fill(ui.Rect{X: r.X - paddingX, Y: r.Y - paddingY, W: r.W + 2*paddingX, H: r.H + 2*paddingY}, background, 0)
		})
	}
}

func mysqlDataModifiedMark(c *ui.Context, t *Document, row, col int) {
	ink := theme.CellText(c, &t.grid, row, col, theme.Colors(c).GridModifiedMark)
	ui.Box(c).Key("draft-mark").Absolute().Top(2).Right(3).Size(4, 4).
		Background(ink).Role(ui.RoleNone)
}

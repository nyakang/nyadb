package objectworkspace

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

const schemaReceiptPreviewLimit = 64 << 10

type schemaReceiptTransfer struct {
	cancel  context.CancelFunc
	results <-chan error
}

// Native dialogs and atomic file writers are supplied by the host. A worker
// captures these callbacks and complete encoded evidence before it starts.
func (s *State) SetSchemaReceiptFiles(pick func(string) (string, error), write func(string, func(io.Writer) error) error) {
	s.schemaReceiptPick, s.schemaReceiptWrite = pick, write
}

func (x *schemaDocumentOwner) receiptJSON() (string, error) {
	if x == nil || x.last == nil || x.last.Receipt == nil {
		return "", contract.ErrUnsupported
	}
	if x.receiptText != "" {
		return x.receiptText, nil
	}
	out := x.last.Clone()
	message := ""
	if out.Error != nil {
		message = out.Error.Error()
	}
	raw, err := json.MarshalIndent(struct {
		Version  int
		Kind     string
		State    string
		Verified bool
		Rows     int64
		Error    string
		Actual   *contract.SchemaDesignSnapshot
		Receipt  *contract.SchemaDesignWriteReceipt
	}{1, "schema-original-write-receipt", out.State, out.Verified, out.Rows, message, out.Actual, out.Receipt}, "", "  ")
	if err != nil {
		return "", err
	}
	x.receiptText = string(raw) + "\n"
	end := min(len(x.receiptText), schemaReceiptPreviewLimit)
	for end > 0 && !utf8.ValidString(x.receiptText[:end]) {
		end--
	}
	x.receiptPreview = x.receiptText[:end]
	x.receiptTruncated = end < len(x.receiptText)
	return x.receiptText, nil
}

func (s *State) exportSchemaReceipt(services ui.Services, x *schemaDocumentOwner) {
	if x == nil || x.lease == nil || x.transfer != nil || s.schemaReceiptPick == nil || s.schemaReceiptWrite == nil {
		return
	}
	raw, err := x.receiptJSON()
	if err != nil {
		x.transferError = err.Error()
		return
	}
	pick, write := s.schemaReceiptPick, s.schemaReceiptWrite
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan error, 1)
	x.transfer = &schemaReceiptTransfer{cancel: cancel, results: ch}
	x.transferError = ""
	go func() {
		err := ctx.Err()
		if err == nil {
			var path string
			path, err = pick("schema-original-write-receipt.json")
			if err == nil && path != "" {
				err = ctx.Err() // A canceled/closed dialog cannot admit a write.
				if err == nil {
					err = write(path, func(w io.Writer) error {
						if err := ctx.Err(); err != nil {
							return err
						}
						if _, err := io.WriteString(w, raw); err != nil {
							return err
						}
						return ctx.Err()
					})
				}
			}
		}
		ch <- err
		services.Invalidate()
	}()
}

func (x *schemaDocumentOwner) consumeReceiptTransfer() {
	if x == nil || x.transfer == nil {
		return
	}
	select {
	case err := <-x.transfer.results:
		x.transfer.cancel()
		x.transfer = nil
		if err != nil {
			x.transferError = err.Error()
		}
	default:
	}
}

func (s *State) consumeSchemaReceiptTransfers() {
	w := s.session()
	for _, d := range s.documents {
		if d.closed || d.schemaDesign == nil || d.profileID != w.ProfileID || s.deps.DocumentActive != nil && !s.deps.DocumentActive(d.id) {
			continue
		}
		d.schemaDesign.owner.consumeReceiptTransfer()
	}
	if x := s.schemaDrop; x != nil && x.target.Object.ProfileID == w.ProfileID {
		x.owner.consumeReceiptTransfer()
	}
	if x := s.schemaCopy; x != nil && x.target.Object.ProfileID == w.ProfileID {
		x.owner.consumeReceiptTransfer()
	}
	if x := s.schemaRename; x != nil && x.target.Object.ProfileID == w.ProfileID {
		x.owner.consumeReceiptTransfer()
	}
}

func (s *State) schemaReceiptView(c *ui.Context, x *schemaDocumentOwner) {
	raw, err := x.receiptJSON()
	if err != nil {
		ui.Text(c, locales.Text("结构操作完成后显示原始回执"))
		ui.Box(c).Grow(1)
		return
	}
	ui.Text(c, locales.Text("原始写入回执独立保留；后续读取不会改写它，也不会重放操作。"))
	r := x.last.Receipt
	state := locales.Text("待核验")
	switch r.State {
	case "committed":
		state = locales.Text("已提交")
	case "rolled_back":
		state = locales.Text("已回滚")
	case "not_executed":
		state = locales.Text("尚未执行")
	}
	verified := locales.Text("否")
	if x.last.Verified {
		verified = locales.Text("是")
	}
	ui.Text(c, fmt.Sprintf(locales.Text("原始回执：%s · 当前核验通过：%s"), state, verified))
	o := r.Original.Target.Object
	ui.Text(c, fmt.Sprintf(locales.Text("原始目标：#%d · %s / %s · %s"), o.ProfileID, o.Catalog, o.Schema, o.Name)).SingleLine()
	ui.Row(c).Wrap().Gap(8).Children(func() {
		if ui.Button(c, locales.Text("复制完整回执")).Clicked() {
			c.WriteClipboard(raw)
		}
		if ui.Button(c, locales.Text("导出完整回执")).Disabled(x.transfer != nil || s.schemaReceiptPick == nil || s.schemaReceiptWrite == nil).Clicked() {
			s.exportSchemaReceipt(c.Services(), x)
		}
		if x.transfer != nil {
			if ui.Button(c, locales.Text("取消导出")).Clicked() {
				x.transfer.cancel()
			}
		}
	})
	if x.receiptTruncated {
		ui.Text(c, locales.Text("回执预览已截断；可复制或导出完整内容"))
	}
	if x.transferError != "" {
		ui.Text(c, x.transferError).SingleLine().Tooltip(x.transferError).TextColor(theme.Colors(c).Error)
	}
	preview := x.receiptPreview
	ui.TextArea(c, &preview).Label(locales.Text("原始结构回执")).ReadOnly(true).Grow(1).MinHeight(40)
}

func (s *State) schemaModalReceiptToggle(c *ui.Context, x *schemaDocumentOwner) bool {
	if x == nil || x.last == nil || x.last.Receipt == nil {
		return false
	}
	ui.Checkbox(c, &x.showReceipt, locales.Text("查看原始结构回执"))
	if x.showReceipt {
		s.schemaReceiptView(c, x)
	}
	return x.showReceipt
}

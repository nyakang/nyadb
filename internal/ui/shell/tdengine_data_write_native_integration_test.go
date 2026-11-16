package shell

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/tdengine"
	"github.com/nyakang/nyadb/internal/ui/dataworkspace"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/locales"
)

func tdenginePublicWriteFixture(t *testing.T) (connections.Profile, string, contract.QueryLease, string) {
	t.Helper()
	if os.Getenv("NYADB_TDENGINE_IT") != "1" {
		t.Skip("requires explicitly owned official TDengine")
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_TDENGINE_CONFIG"))
	if err != nil {
		t.Fatal("missing private fixture configuration")
	}
	var cfg struct {
		Host, Username, PasswordFile, Image, Container, Version string
		Port                                                    uint16
	}
	if json.Unmarshal(raw, &cfg) != nil || cfg.Host != "127.0.0.1" || cfg.Port == 0 || cfg.Image != "tdengine/tdengine@sha256:79d32ce8a42f64c7dbfc33946a36b3e176ea23c0cd695a1bc9e8aa889ca03b88" || cfg.Version != "3.3.6.13" || cfg.Container == "" {
		t.Fatal("fixture requires the pinned official product")
	}
	secret, err := os.ReadFile(cfg.PasswordFile)
	if err != nil {
		t.Fatal("missing private fixture credential")
	}
	p, _ := connections.Defaults(connections.TDengine)
	p.ID, p.Name, p.Host, p.Port, p.Username = 841, "Owned TDengine writer", cfg.Host, cfg.Port, cfg.Username
	s, err := tdengine.OpenSession(context.Background(), p, string(secret))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	provider, _ := contract.Require[contract.QueryProvider](s)
	sql, err := provider.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sql.Release)
	db := "public_write_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	tdenginePublicSQL(t, sql, "", "CREATE DATABASE `"+db+"` PRECISION 'ns'")
	t.Cleanup(func() {
		_, err := sql.Execute(context.Background(), "", "DROP DATABASE `"+db+"`")
		if err != nil {
			t.Error("owned database cleanup failed", err)
		}
	})
	t.Logf("official native TDengine version=%s original_container=%s", cfg.Version, cfg.Container)
	return p, string(secret), sql, db
}

func tdenginePublicSQL(t *testing.T, sql contract.QueryLease, db, query string) [][]contract.Cell {
	t.Helper()
	var rows [][]contract.Cell
	_, err := sql.ExecuteStream(context.Background(), db, query, func(_ int, _ []contract.Column, row []contract.Cell) error {
		copy := make([]contract.Cell, len(row))
		for i, c := range row {
			copy[i] = c.Clone()
		}
		rows = append(rows, copy)
		return nil
	})
	if err != nil {
		t.Fatal("independent native observation failed", err)
	}
	return rows
}

func tdenginePublicData(t *testing.T, s *State, tt *ui.Tester, services ui.Services, p connections.Profile, db, kind, name string) *dataworkspace.Document {
	t.Helper()
	s.openMySQLDatabase(services, db, true)
	taskPanelWait(t, tt, func() bool { return s.sessions.Current().Pending == "" && s.sessions.Current().Cache[db] != nil })
	category := "表"
	s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: db, Object: name, Category: category})
	d := s.currentTab().data
	if d == nil || d.Object().Kind != kind {
		t.Fatal("qualified native data document missing", s.currentTab())
	}
	taskPanelWait(t, tt, func() bool { return !d.Busy() })
	if d.Page() == nil || d.Error() != "" {
		t.Fatal("native data page failed", d.Error())
	}
	return d
}

func tdengineWriteClick(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	// State callbacks can add a draft footer or change dialogs. Resolve the
	// target after rendering that state, rather than using its previous box.
	tt.Frame()
	if err := tt.Click(locales.Text(label)); err != nil {
		t.Fatal(err)
	}
}

func tdengineWritePreview(t *testing.T, s *State, tt *ui.Tester, d *dataworkspace.Document) string {
	t.Helper()
	if !d.HasDraft() || !s.dataWorkspace.CanSave(d) {
		t.Fatalf("original native draft cannot be previewed: draft=%v busy=%v error=%q texts=%q", d.HasDraft(), d.Busy(), d.Error(), tt.Texts())
	}
	tdengineWriteClick(t, tt, "保存数据更改")
	// A ready confirmation keeps the original document busy to prohibit edits
	// and leave actions until the user confirms or cancels its immutable plan.
	taskPanelWait(t, tt, func() bool { return tt.HasText(locales.Text("确认 TDengine 数据更改")) || !d.Busy() })
	if !tt.HasText(locales.Text("确认 TDengine 数据更改")) || !d.HasDraft() {
		t.Fatalf("original native confirmation unavailable: draft=%v busy=%v error=%q texts=%q", d.HasDraft(), d.Busy(), d.Error(), tt.Texts())
	}
	tdengineWriteClick(t, tt, "复制完整预览")
	return tt.Clipboard()
}

func tdengineWriteConfirm(t *testing.T, s *State, tt *ui.Tester, d *dataworkspace.Document) {
	t.Helper()
	tdengineWritePreview(t, s, tt, d)
	tdengineWriteClick(t, tt, "确认并提交更改")
	taskPanelWait(t, tt, func() bool { return !d.Busy() })
	if d.HasDraft() || d.Error() != "" {
		t.Fatal("complete original native receipt did not settle drafts/read", d.Error())
	}
}

func tdengineWriteReceipt(t *testing.T, tt *ui.Tester) string {
	t.Helper()
	tdengineWriteClick(t, tt, "原生顺序写入回执")
	tdengineWriteClick(t, tt, "复制完整回执")
	raw := tt.Clipboard()
	tdengineWriteClick(t, tt, "关闭")
	return raw
}

func captureTDengineWrite(t *testing.T, s *State, phase string) {
	t.Helper()
	folder := os.Getenv("NYADB_TDENGINE_SHELL_EVIDENCE")
	if folder == "" {
		return
	}
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			tt := ui.NewTester(s.View, size[0], size[1])
			labels := []string{"保存数据更改"}
			if phase == "preview" {
				labels = []string{"确认 TDengine 数据更改", "复制完整预览", "取消", "确认并提交更改"}
			}
			if phase == "receipt" {
				labels = []string{"复制完整回执", "导出完整回执", "关闭"}
			}
			for _, label := range labels {
				if r, ok := tt.Find(locales.Text(label)); !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0]) || r.Y+r.H > float32(size[1]) {
					t.Fatal("localized native write action outside viewport", phase, language, label, r)
				}
			}
			out := filepath.Join(folder, "writes", language)
			if err := os.MkdirAll(out, 0700); err != nil {
				t.Fatal(err)
			}
			writeDomainCapture(t, out, phase+"-"+strconv.Itoa(size[0])+"x"+strconv.Itoa(size[1]), tt)
		}
	}
}

func TestTDengineNativePublicDataWriteConfirmationTypedValuesAndReadback(t *testing.T) {
	p, password, sql, db := tdenginePublicWriteFixture(t)
	tdenginePublicSQL(t, sql, db, "CREATE TABLE items(ts TIMESTAMP,i BIGINT,dec DECIMAL(38,10),txt VARCHAR(64),bin VARBINARY(64),n INT);INSERT INTO items VALUES(1704067200123456789,9007199254740993,'-12345678901234567890.1234567890','original','\\x00ff',7)")
	s, tt, _, saved, services := keyPanelState(t, p, password)
	t.Cleanup(s.CloseMySQLSessions)
	d := tdenginePublicData(t, s, tt, *services, saved, db, "table", "items")
	session, err := s.runtime.Session(saved.ID)
	if err != nil || d.Page().Editable == nil || !*d.Page().Editable || !session.Capabilities().EditableTables {
		t.Fatal("public native write admission missing")
	}
	boundA, err := s.runtime.Data(saved.ID).ReserveDocument(d.Object())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = boundA.Close() })
	boundB, err := s.runtime.Data(saved.ID).ReserveDocument(d.Object())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = boundB.Close() })
	a, b := boundA.Runtime(), boundB.Runtime()
	owned, err := a.AdvancedLoader(context.Background(), saved.ID, db, "items", contract.DataOptions{Object: d.Object(), Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := a.PreviewEdits(context.Background(), saved.ID, db, "items", []contract.DataEdit{{Kind: "update", Original: owned.RowSnapshots[0], Values: map[string]contract.DataValue{"i": {Kind: contract.Integer, Text: "9007199254740993"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.SubmitEdits(context.Background(), saved.ID, db, "items", plan); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("public plan crossed original document", err)
	}
	if result, err := a.SubmitEdits(context.Background(), saved.ID, db, "items", plan); err != nil || !contract.DataWriteComplete(result, 1) {
		t.Fatal("original public plan lost owning executor", err, result)
	}
	if _, err := a.SubmitEdits(context.Background(), saved.ID, db, "items", plan); err == nil {
		t.Fatal("original public plan replayed")
	}
	s.dataWorkspace.EditCell(*services, d, 0, "ts", contract.Cell{Kind: contract.DateTime, Text: "2024-01-01T00:00:01Z"})
	if d.HasDraft() {
		t.Fatal("existing native timestamp editable")
	}
	s.dataWorkspace.EditCell(*services, d, 0, "i", contract.Cell{Kind: contract.Integer, Text: "9007199254740995"})
	s.dataWorkspace.EditCell(*services, d, 0, "dec", contract.Cell{Kind: contract.Decimal, Text: "-12345678901234567890.1234567891"})
	s.dataWorkspace.EditCell(*services, d, 0, "txt", contract.Cell{Kind: contract.Text, Text: ""})
	s.dataWorkspace.EditCell(*services, d, 0, "bin", contract.Cell{Kind: contract.Binary, Bytes: []byte{0, 255, 1}})
	s.dataWorkspace.EditCell(*services, d, 0, "n", contract.Cell{Kind: contract.Null})
	preview := tdengineWritePreview(t, s, tt, d)
	if !strings.Contains(preview, "9007199254740995") || !strings.Contains(preview, "NULL") || !strings.Contains(preview, db) {
		t.Fatal("complete native preview lost typed request")
	}
	captureTDengineWrite(t, s, "preview")
	if tdenginePublicSQL(t, sql, db, "SELECT i FROM items")[0][0].Text != "9007199254740993" {
		t.Fatal("preview wrote native data")
	}
	tdengineWriteClick(t, tt, "取消")
	if !d.HasDraft() || !s.dataWorkspace.CanSave(d) {
		t.Fatal("canceling preview discarded original draft")
	}
	tdengineWriteConfirm(t, s, tt, d)
	rows := tdenginePublicSQL(t, sql, db, "SELECT i,dec,txt,bin,n FROM items")
	if rows[0][0].Text != "9007199254740995" || rows[0][1].Text != "-12345678901234567890.1234567891" || rows[0][2].Kind != contract.Text || rows[0][2].Text != "" || string(rows[0][3].Bytes) != string([]byte{0, 255, 1}) || rows[0][4].Kind != contract.Null {
		t.Fatal("public native typed write changed values", rows)
	}
	if d.Page().Rows[0][1].Value.Text != "9007199254740995" {
		t.Fatal("acknowledgement did not trigger actual original readback")
	}
	receipt := tdengineWriteReceipt(t, tt)
	var envelope struct {
		Receipt         contract.DataSequentialWriteReceipt
		CommitConfirmed bool
	}
	if err := json.Unmarshal([]byte(receipt), &envelope); err != nil || envelope.CommitConfirmed || envelope.Receipt.Object != d.Object() || len(envelope.Receipt.Statements) != 1 {
		t.Fatal("public receipt lost original native statement identity", err, receipt)
	}
	statement := envelope.Receipt.Statements[0]
	var native struct {
		Code         *int   `json:"code"`
		AffectedRows *int64 `json:"affected_rows"`
		Action       string `json:"action"`
	}
	if err := json.Unmarshal([]byte(statement.NativeJSON), &native); err != nil || statement.State != contract.DataWriteAcknowledged || statement.AffectedRows != 1 || native.Code == nil || *native.Code != 0 || native.AffectedRows == nil || *native.AffectedRows != 1 || native.Action != "binary_query" {
		t.Fatal("public receipt lost original native acknowledgement", err, receipt)
	}
	tdengineWriteClick(t, tt, "原生顺序写入回执")
	captureTDengineWrite(t, s, "receipt")
	tdengineWriteClick(t, tt, "关闭")
	tdengineWriteClick(t, tt, "单元格编辑器")
	tdengineWriteClick(t, tt, "添加记录")
	if !d.HasDraft() || !tt.HasText(locales.Text("值类型")) {
		t.Fatal("native typed insert editor missing")
	}
	jsonLabels := func() int {
		count := 0
		for _, text := range tt.Texts() {
			if text == "JSON" {
				count++
			}
		}
		return count
	}
	// The shared editor also has a read-only JSON presentation tab. Opening
	// native value choices must not add a JSON write kind to that presentation.
	jsonBefore := jsonLabels()
	tdengineWriteClick(t, tt, "值类型")
	if jsonLabels() != jsonBefore || !tt.HasText(locales.Text("日期时间")) || !tt.HasText(locales.Text("精确小数")) {
		t.Fatal("native editor published unsupported or incomplete value kinds")
	}
	tdengineWriteClick(t, tt, "整数")
	tdengineWriteClick(t, tt, "值类型")
	tdengineWriteClick(t, tt, "日期时间")
	taskPanelField(t, tt, "单元格编辑器内容", "2024-01-01T00:00:02.123456789Z")
	s.dataWorkspace.EditCell(*services, d, 1, "txt", contract.Cell{Kind: contract.Text, Text: "added 猫"})
	tdengineWriteConfirm(t, s, tt, d)
	rows = tdenginePublicSQL(t, sql, db, "SELECT ts,txt,i,n FROM items ORDER BY ts")
	if len(rows) != 2 || rows[1][0].Text != "2024-01-01T00:00:02.123456789Z" || rows[1][1].Text != "added 猫" || rows[1][2].Kind != contract.Null || rows[1][3].Kind != contract.Null {
		t.Fatal("insert invented omitted defaults or rounded timestamp", rows)
	}
	tdengineWriteClick(t, tt, "单元格编辑器")
	tdengineWriteClick(t, tt, "added 猫")
	tdengineWriteClick(t, tt, "删除记录")
	tdengineWriteConfirm(t, s, tt, d)
	if len(tdenginePublicSQL(t, sql, db, "SELECT * FROM items")) != 1 {
		t.Fatal("qualified timestamp deletion changed scope")
	}
	captureTDengineWrite(t, s, "saved")
}

func TestTDengineNativePublicStableWritesKeepOriginalChildAndTag(t *testing.T) {
	p, password, sql, db := tdenginePublicWriteFixture(t)
	tdenginePublicSQL(t, sql, db, "CREATE STABLE meters(ts TIMESTAMP,k INT COMPOSITE KEY,v INT) TAGS(site NCHAR(32));CREATE TABLE child_a USING meters TAGS('A');CREATE TABLE child_b USING meters TAGS('B');INSERT INTO child_a VALUES(1704067200123456789,1,11);INSERT INTO child_b VALUES(1704067200123456789,1,22)")
	s, tt, _, saved, services := keyPanelState(t, p, password)
	t.Cleanup(s.CloseMySQLSessions)
	d := tdenginePublicData(t, s, tt, *services, saved, db, "stable", "meters")
	row := 0
	if d.Page().Rows[0][0].Value.Text != "child_a" {
		row = 1
	}
	s.dataWorkspace.EditCell(*services, d, row, "site", contract.Cell{Kind: contract.Text, Text: "foreign"})
	s.dataWorkspace.EditCell(*services, d, row, "tbname", contract.Cell{Kind: contract.Text, Text: "child_b"})
	s.dataWorkspace.EditCell(*services, d, row, "k", contract.Cell{Kind: contract.Integer, Text: "2"})
	if d.HasDraft() {
		t.Fatal("native child/TAG/composite identity editable")
	}
	s.dataWorkspace.EditCell(*services, d, row, "v", contract.Cell{Kind: contract.Integer, Text: "44"})
	tdengineWriteConfirm(t, s, tt, d)
	rows := tdenginePublicSQL(t, sql, db, "SELECT tbname,v,site FROM meters ORDER BY tbname")
	if rows[0][1].Text != "44" || rows[0][2].Text != "A" || rows[1][1].Text != "22" || rows[1][2].Text != "B" {
		t.Fatal("public stable write crossed original child/TAG", rows)
	}
	tdengineWriteClick(t, tt, "添加记录")
	s.dataWorkspace.EditCell(*services, d, 2, "tbname", contract.Cell{Kind: contract.Text, Text: "child_b"})
	s.dataWorkspace.EditCell(*services, d, 2, "ts", contract.Cell{Kind: contract.DateTime, Text: "2024-01-01T00:00:03Z"})
	s.dataWorkspace.EditCell(*services, d, 2, "k", contract.Cell{Kind: contract.Integer, Text: "2"})
	s.dataWorkspace.EditCell(*services, d, 2, "v", contract.Cell{Kind: contract.Integer, Text: "55"})
	tdengineWriteConfirm(t, s, tt, d)
	if len(tdenginePublicSQL(t, sql, db, "SELECT * FROM child_b")) != 2 {
		t.Fatal("public insert ignored approved existing child")
	}
	if reopened := tdenginePublicData(t, s, tt, *services, saved, db, "stable", "meters"); reopened != d {
		t.Fatal("original stable activation duplicated its native document")
	}
}

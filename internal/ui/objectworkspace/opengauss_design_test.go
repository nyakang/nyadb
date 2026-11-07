package objectworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/locales"
)

// Only a test-injected operation makes these forms reachable. Production
// openGauss keeps CanApply/Apply and DesignTables unavailable until the native
// executor's remaining concurrency and workbench verification is complete.
func openGaussFormState(t *testing.T) (*State, *Document, *ui.Tester, ui.Services) {
	t.Helper()
	s, d, _, tt, services := schemaTestState(t)
	comment := "native 中文 comment"
	definition := schemaddl.OpenGaussCopyDefinition{Owner: "owner", Creator: "owner", CanCreate: true,
		Columns: []schemaddl.OpenGaussCopyColumn{
			{Number: 1, Name: "id", Type: "bigint", NotNull: true},
			{Number: 3, Name: "value", Type: "numeric(38,20)", Default: "9007199254740993.12345678901234567890", Comment: &comment},
		},
		Constraints: []schemaddl.OpenGaussCopyObject{{OID: 20, Kind: "c", Name: "positive", SQL: "CHECK (value >= 0)"}},
		Indexes:     []schemaddl.OpenGaussCopyObject{{OID: 21, Kind: "index", Name: "value_idx", SQL: `CREATE INDEX value_idx ON main.t USING btree (value)`}},
		Triggers:    []schemaddl.OpenGaussCopyObject{{OID: 22, Kind: "trigger", Name: "audit", Enabled: "O", SQL: `CREATE TRIGGER audit BEFORE UPDATE ON main.t FOR EACH ROW EXECUTE PROCEDURE main.audit_fn()`}},
	}
	raw, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	p := &schemaddl.OpenGaussCatalog{Database: "main", Namespace: "main", Name: "t", DatabaseOID: 1, NamespaceOID: 2, RelationOID: 3, RoleOID: 4, CanManage: true, CanCreate: true,
		Entries: []schemaddl.OpenGaussCatalogEntry{
			{Kind: "namespace", OID: 2, Data: json.RawMessage(`{"nspname":"main","nspacl":["reader=U/owner"]}`)},
			{Kind: "relation", OID: 3, Data: json.RawMessage(`{"relname":"t","relkind":"r","relpersistence":"p","reloptions":["orientation=row"],"future_property":9007199254740993}`)},
			{Kind: "copy_definition", OID: 3, Data: raw},
		},
	}
	table, ddl, err := p.DesignTable()
	if err != nil {
		t.Fatal(err)
	}
	revision, err := p.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	x := d.schemaDesign
	x.original.Schema = schemaddl.Snapshot{Engine: "opengauss", Exists: true, Table: table, OpenGauss: p, DDL: ddl, Revision: revision}
	x.draft, x.saved = table.Clone(), table.Clone()
	x.baseline = schemaddl.Signature(x.draft)
	tt.Frame()
	return s, d, tt, services
}

func TestOpenGaussDesignerRenameRetainsNativePrecisionAndPhysicalColumnIdentity(t *testing.T) {
	s, d, tt, _ := openGaussFormState(t)
	if err := s.schemaSelectRow(d, 0, 1); err != nil {
		t.Fatal(err)
	}
	x := d.schemaDesign
	before := x.draft.Clone().Clauses[1]
	original := schemaddl.Signature(x.original)
	tt.Frame()
	if err := tt.Click("高级字段属性"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("字段名称"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("ExactValue")
	field, ok := d.SelectedField()
	if !ok || field.Name != "ExactValue" || field.Type != "numeric(38,20)" || field.Default == nil || *field.Default != "9007199254740993.12345678901234567890" || field.Comment != *before.OpenGauss.Comment {
		t.Fatal("native inspector lost pending name, precision or comment", field)
	}
	if err := tt.Click("应用定义修改"); err != nil {
		t.Fatal(err)
	}
	after := x.draft.Clauses[1]
	if after.ID != before.ID || after.OpenGauss.Number != 3 || after.SQL != before.SQL || schemaddl.Signature(after.OpenGauss) != schemaddl.Signature(before.OpenGauss) || after.Postgres != nil || x.draft.Postgres != nil {
		t.Fatal("name edit replaced native properties or a dropped column slot")
	}
	if err := s.PrepareSchemaDesign(d); err != nil || x.plan.Mode != "rename_column" || len(x.plan.SQL) != 1 || x.plan.SQL[0] != `ALTER TABLE "main"."t" RENAME COLUMN "value" TO "ExactValue"` {
		t.Fatal("native rename was not independently planned", x.plan, err)
	}
	if d.DesignBusy() || schemaddl.Signature(x.original) != original {
		t.Fatal("form executed SQL or changed the original catalog")
	}
}

func TestOpenGaussDesignerInlineCommentPreviewAndInvalidNativeFieldRetainDraft(t *testing.T) {
	s, d, tt, _ := openGaussFormState(t)
	x := d.schemaDesign
	id := x.draft.Clauses[1].ID
	key := "field/" + id + "/5"
	if err := tt.Click(key); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("pending 字段")
	field, ok := d.SelectedField()
	if !ok || field.Comment != "pending 字段" {
		t.Fatal("focused native comment did not reach the inspector", field)
	}
	tt.Key(0, ui.KeyEnter)
	if err := s.PrepareSchemaDesign(d); err != nil || len(x.plan.SQL) != 1 || !strings.HasPrefix(x.plan.SQL[0], `COMMENT ON COLUMN "main"."t"."value" IS `) {
		t.Fatal("comment edit rebuilt the column", x.plan, err)
	}
	before := schemaddl.Signature(x.draft)
	if err := s.commitSchemaCell(d, id, 1, "bigint GENERATED ALWAYS AS IDENTITY"); err == nil || schemaddl.Signature(x.draft) != before {
		t.Fatal("PostgreSQL identity entered a native field")
	}
	if err := s.commitSchemaCell(d, id, 1, "integer"); err != nil {
		t.Fatal(err)
	}
	if x.draft.Clauses[1].OpenGauss.Number != 3 || x.draft.Clauses[1].Postgres != nil || *x.draft.Clauses[1].OpenGauss.Comment != "pending 字段" {
		t.Fatal("type edit discarded physical identity or comment")
	}
}

func TestOpenGaussDesignerNullableCommentsAreUnchangedOnViewAndExplicitlyEditable(t *testing.T) {
	s, d, tt, _ := openGaussFormState(t)
	x := d.schemaDesign
	for _, initial := range []*string{nil, new(string)} {
		x.draft.Clauses[0].OpenGauss.Comment = initial
		x.baseline = schemaddl.Signature(x.draft)
		if err := s.schemaSelectRow(d, 0, 0); err != nil {
			t.Fatal(err)
		}
		x.fieldDetails = true
		tt.Frame()
		tt.Frame()
		tt.Scroll(740, 580, 0, 1000)
		tt.Frame()
		if d.DesignDirty() || (x.draft.Clauses[0].OpenGauss.Comment == nil) != (initial == nil) {
			t.Fatal("viewing the form changed SQL NULL or an empty native comment")
		}
		if err := tt.Click("设置注释/字段注释"); err != nil {
			t.Fatal(err)
		}
		if !d.DesignDirty() || (x.draft.Clauses[0].OpenGauss.Comment == nil) == (initial == nil) {
			t.Fatal("explicit presence toggle did not distinguish empty comment from NULL")
		}
	}
}

func TestOpenGaussDesignerConstraintIndexAndTriggerMetadataKeepOriginalOIDs(t *testing.T) {
	for _, page := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(page), func(t *testing.T) {
			s, d, tt, _ := openGaussFormState(t)
			x := d.schemaDesign
			x.page = page
			row, caption := 2, "约束注释"
			if page == 2 {
				row, caption = 0, "索引注释"
			} else if page == 3 {
				row, caption = 1, "触发器注释"
			}
			if err := s.schemaSelectRow(d, page, row); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if page == 3 {
				tt.Scroll(740, 580, 0, 1000)
				tt.Frame()
			}
			if err := tt.Click("设置注释/" + caption); err != nil {
				t.Fatal(err)
			}
			if err := tt.Click(caption); err != nil {
				t.Fatal(err)
			}
			tt.Type("保留 ' 引号\\comment")
			if page == 3 {
				x.draft.Definitions[row].OpenGauss.Enabled = "R"
			}
			if err := s.PrepareSchemaDesign(d); err != nil {
				t.Fatal(err)
			}
			for _, sql := range x.plan.SQL {
				if strings.Contains(sql, "DROP ") || strings.Contains(sql, "CREATE ") {
					t.Fatal("native metadata change recreated an object", sql)
				}
			}
			if page == 1 && x.prepared.Desired.Clauses[row].OpenGauss.OID != 20 || page > 1 && x.prepared.Desired.Definitions[row].OpenGauss.OID != uint64(21+row) {
				t.Fatal("native metadata preview lost object identity")
			}
			if page == 3 && !strings.Contains(strings.Join(x.plan.SQL, "\n"), `ENABLE REPLICA TRIGGER "audit"`) {
				t.Fatal("native trigger state was not planned", x.plan.SQL)
			}
		})
	}
}

func TestOpenGaussDesignerMissingTableInitializesOnlyNativeProperties(t *testing.T) {
	s, d, tt, services := openGaussFormState(t)
	x := d.schemaDesign
	p := x.original.Schema.OpenGauss.Clone()
	p.RelationOID, p.CanManage, p.Entries = 0, false, p.Entries[:1]
	table, _, err := p.DesignTable()
	if err != nil {
		t.Fatal(err)
	}
	revision, err := p.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	binding := s.deps.SchemaDesignFor
	s.deps.SchemaDesignFor = func(id int64) contract.SchemaDesignOperations {
		ops := binding(id)
		ops.Snapshot = func(_ context.Context, target contract.SchemaDesignTarget) (contract.SchemaDesignSnapshot, error) {
			return contract.SchemaDesignSnapshot{Target: target, Schema: schemaddl.Snapshot{Engine: "opengauss", Revision: revision, OpenGauss: p, Table: table}}, nil
		}
		return ops
	}
	x.isNew = true
	s.loadSchemaDesign(d, services)
	waitSchema(t, tt, func() bool { return !x.busy })
	if !x.loaded || x.draft.OpenGauss == nil || x.draft.Postgres != nil || len(x.draft.Clauses) != 2 {
		t.Fatal("missing native table used another engine's template", d.Error())
	}
	for _, clause := range x.draft.Clauses {
		if clause.OpenGauss == nil || clause.Postgres != nil {
			t.Fatal("native template borrowed PostgreSQL catalog properties")
		}
	}
	x.draft.Name = "chosen"
	if err := s.PrepareSchemaDesign(d); err != nil || x.plan.Mode != "create" || !strings.Contains(x.plan.SQL[0], `CREATE TABLE "main"."chosen"`) {
		t.Fatal("native new-table preview ignored the chosen name", x.plan, err)
	}
	x.preview = false
	for _, page := range []int{0, 1, 2, 3} {
		x.page = page
		if err := s.schemaSelectRow(d, page, -1); err != nil {
			t.Fatal(err)
		}
		s.schemaAdd(d)
		if page < 2 {
			if c := x.draft.Clauses[len(x.draft.Clauses)-1]; c.OpenGauss == nil || c.Postgres != nil {
				t.Fatal("new field/constraint lacks native properties", c)
			}
		} else {
			definition := x.draft.Definitions[len(x.draft.Definitions)-1]
			if definition.OpenGauss == nil || definition.Postgres != nil || !strings.Contains(definition.SQL, `ON "main"."chosen"`) || page == 3 && !strings.Contains(definition.SQL, "EXECUTE PROCEDURE") {
				t.Fatal("new native definition used another grammar or target", definition)
			}
		}
	}
}

func TestOpenGaussDesignerStorageOptionsRetainExistingConfigurationAndDoNotBorrowPostgresOptions(t *testing.T) {
	s, d, tt, _ := openGaussFormState(t)
	x := d.schemaDesign
	x.page = 4
	if err := s.schemaSelectRow(d, 4, -1); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, caption := range []string{"添加存储参数", "删除末尾存储参数"} {
		if err := tt.Click(caption); err != nil {
			t.Fatal(err)
		}
	}
	if d.DesignDirty() || len(x.draft.OpenGauss.Parameters) != 1 || x.draft.OpenGauss.Parameters[0] != "orientation=row" {
		t.Fatal("read-only existing storage controls changed native options")
	}
	if tt.HasText("访问方法:") || tt.HasText("SQLite 表选项:") || !tt.HasText("已有表的存储与所有者设置保留；更改需独立操作。") {
		t.Fatal("native options showed another engine's storage form")
	}
	x.original.Schema.Exists = false
	tt.Frame()
	if err := tt.Click("添加存储参数"); err != nil {
		t.Fatal(err)
	}
	if len(x.draft.OpenGauss.Parameters) != 2 || x.draft.OpenGauss.Parameters[1] != "orientation=row" {
		t.Fatal("new table did not expose native storage parameter editing")
	}
}

func TestOpenGaussDesignerAdvancedDefinitionsStayCompleteAndNativeGraphIsQualified(t *testing.T) {
	s, d, tt, _ := openGaussFormState(t)
	x := d.schemaDesign
	x.page = 2
	if err := s.schemaSelectRow(d, 2, 0); err != nil {
		t.Fatal(err)
	}
	if x.structured == nil || x.structured.Form.Schema != "main" || x.structured.Form.Table != "t" || x.structured.Form.Using != "btree" {
		t.Fatal("ordinary native index did not open a qualified graph")
	}
	x.structured.Form.Name = "NativeIndex"
	if err := s.applySchemaForms(d); err != nil {
		t.Fatal(err)
	}
	if x.draft.Definitions[0].OpenGauss.OID != 21 || !strings.Contains(x.draft.Definitions[0].SQL, `ON "main"."t" USING btree`) {
		t.Fatal("graph replaced native identity or qualified definition")
	}
	advanced := `CREATE INDEX "NativeIndex" ON "main"."t" USING btree (value) WITH (fillfactor=80)`
	x.draft.Definitions[0].SQL = advanced
	if err := s.schemaSelectRow(d, 2, 0); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if x.structured != nil || x.raw != advanced || x.draft.Definitions[0].SQL != advanced {
		t.Fatal("native advanced index lost its complete storage definition")
	}
}

func TestOpenGaussDesignerCapturedNativeDraftWaitsForCancellationReceiptAndOriginalProfile(t *testing.T) {
	s, d, tt, services := openGaussFormState(t)
	x := d.schemaDesign
	w := s.session()
	s.deps.Session = func() Session { return w }
	comment := "captured native comment"
	x.draft.OpenGauss.Comment = &comment
	if err := s.PrepareSchemaDesign(d); err != nil {
		t.Fatal(err)
	}
	started := make(chan contract.SchemaDesignRequest, 1)
	release := make(chan struct{}, 1)
	t.Cleanup(func() { close(release) })
	binding := s.deps.SchemaDesignFor
	s.deps.SchemaDesignFor = func(id int64) contract.SchemaDesignOperations {
		ops := binding(id)
		ops.Apply = func(ctx context.Context, r contract.SchemaDesignRequest) contract.SchemaDesignOutcome {
			started <- r
			<-release
			return contract.SchemaDesignOutcome{State: "uncertain", Error: ctx.Err()}
		}
		return ops
	}
	s.executeSchemaDesign(d, services)
	var request contract.SchemaDesignRequest
	select {
	case request = <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("native injected executor did not start")
	}
	*x.draft.OpenGauss.Comment = "later UI draft"
	x.draft.OpenGauss.Parameters[0] = "later UI parameter"
	if *request.Desired.OpenGauss.Comment != "captured native comment" || request.Desired.OpenGauss.Parameters[0] != "orientation=row" || request.Original.Schema.Postgres != nil {
		t.Fatal("native worker request shared mutable UI metadata")
	}
	s.cancelSchemaDesign(d)
	if !x.busy || !x.cancelRequested || x.blocked {
		t.Fatal("native cancellation settled before driver acknowledgement")
	}
	w.ProfileID = 999
	// Closing release is reserved for cleanup; a cancelled executor can now
	// publish its acknowledgement when the test explicitly permits it.
	release <- struct{}{}
	waitSchema(t, tt, func() bool { return len(x.results) == 1 })
	s.Consume(services)
	if !x.busy || x.blocked {
		t.Fatal("parked original-profile completion was consumed elsewhere")
	}
	w.ProfileID = d.profileID
	waitSchema(t, tt, func() bool { return !x.busy })
	if !x.blocked || *x.draft.OpenGauss.Comment != "later UI draft" || *x.original.Schema.Table.Clauses[1].OpenGauss.Comment != "native 中文 comment" {
		t.Fatal("uncertain native receipt discarded drafts or replaced the original snapshot")
	}
	if err := s.PrepareSchemaDesign(d); err == nil {
		t.Fatal("uncertain native design could be replayed")
	}
}

func TestOpenGaussColumnStoreDesignerLocksExistingNullabilityAndShowsLocalizedReason(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		t.Run(language, func(t *testing.T) {
			i18n.SetLocaleFunc(func() string { return language })
			s, d, tt, _ := openGaussFormState(t)
			x := d.schemaDesign
			x.original.Schema.Table.OpenGauss.Parameters = []string{"orientation=column"}
			x.draft.OpenGauss.Parameters = []string{"orientation=column"}
			if err := s.schemaSelectRow(d, 0, 0); err != nil {
				t.Fatal(err)
			}
			x.fieldDetails = true
			tt.Frame()
			reason := locales.Text("openGauss 列存表的已有字段非空属性只读。")
			if !tt.HasText(reason) {
				t.Fatal("native column-store form did not explain its disabled control", language)
			}
			before := x.pgField.NotNull
			if err := tt.Click(locales.Text("非空")); err != nil {
				t.Fatal(err)
			}
			if x.pgField.NotNull != before {
				t.Fatal("disabled existing column-store nullability changed")
			}
			if err := s.commitSchemaCell(d, x.draft.Clauses[0].ID, 2, "false"); !errors.Is(err, schemaddl.ErrOpenGaussNotNull) {
				t.Fatal("inline field bypassed the native nullability rule", err)
			}
			for _, size := range [][2]int{{1280, 800}, {840, 560}, {530, 430}} {
				tt.SetSize(size[0], size[1])
				tt.Frame()
				button, ok := tt.Find(locales.Text("应用定义修改"))
				if !ok || button.Y < 0 || button.Y+button.H > float32(size[1]) || button.X+button.W > float32(size[0]) {
					t.Fatal("native form apply action is clipped", language, size, button)
				}
				if root := os.Getenv("NYADB_OPENGAUSS_FORM_CAPTURE"); root != "" {
					if err := os.MkdirAll(root, 0700); err != nil {
						t.Fatal(err)
					}
					file, err := os.Create(filepath.Join(root, fmt.Sprintf("fields-%s-%dx%d.png", language, size[0], size[1])))
					if err != nil {
						t.Fatal(err)
					}
					encodeErr := png.Encode(file, tt.Image())
					closeErr := file.Close()
					if encodeErr != nil || closeErr != nil {
						t.Fatal(encodeErr, closeErr)
					}
				}
			}
		})
	}
}

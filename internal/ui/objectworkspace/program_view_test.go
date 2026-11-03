package objectworkspace

import (
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func TestProgramViewPreservesReadOnlyMetadataOrderAndServerText(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, lang := range []string{"zh-Hans", "en"} {
		t.Run(lang, func(t *testing.T) {
			i18n.SetLocaleFunc(func() string { return lang })
			for _, kind := range []string{"函数", "存储过程", "事件"} {
				d := newProgram(41, "app", mysqlsvc.MySQLDBObject{Name: "cleanup", Kind: kind, Definer: "原始@server", Parameters: "IN label varchar(80)", ReturnType: "decimal(10,2)", SQLSecurity: "DEFINER", Status: "ENABLED", Schedule: "EVERY 1 DAY", Starts: "2026-10-29", Ends: "2026-11-01", OnCompletion: "PRESERVE", CreatedAt: "2026-10-01", ModifiedAt: "2026-10-28", Comment: "名称不能被翻译", Definition: "BEGIN\n  DELETE FROM logs;\nEND"})
				tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); d.View(c) }, 640, 920)
				previousY := float32(-1)
				for _, label := range []string{"类型", "名称", "定义者", "参数", "返回类型", "SQL SECURITY", "状态", "调度", "开始时间", "结束时间", "ON COMPLETION", "创建时间", "修改时间", "注释", "对象定义（由服务器返回）"} {
					label = locales.Text(label)
					r, ok := tt.Find(label)
					if !ok || r.Y <= previousY {
						t.Fatalf("%s/%s: property order changed at %q: %+v", lang, kind, label, r)
					}
					previousY = r.Y
				}
				for _, value := range []string{"app / cleanup", locales.Text(kind) + " · " + locales.Text("只读"), "原始@server", "名称不能被翻译", "decimal(10,2)", "  DELETE FROM logs;"} {
					if !tt.HasText(value) {
						t.Fatalf("%s/%s: server metadata or read-only header missing: %q", lang, kind, value)
					}
				}
				// The module has no editor or execute affordance, even for mutating bodies.
				for _, action := range []string{"运行", "执行", "确认", "保存", "新建 SQL 页签"} {
					if _, ok := tt.Find(action); ok {
						t.Fatalf("definition exposed an action: %s", action)
					}
				}
				d.Close()
				tt.Frame()
				if tt.HasText("cleanup") || tt.HasText("  DELETE FROM logs;") {
					t.Fatal("closed definition continued rendering")
				}
			}
		})
	}
}

func TestPostgreSQLSequenceViewLocalizesSourceAndPreservesExactValues(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, lang := range []string{"zh-Hans", "en"} {
		t.Run(lang, func(t *testing.T) {
			i18n.SetLocaleFunc(func() string { return lang })
			ref := contract.NamespaceRef{ProfileID: 41, Catalog: "catalog", Schema: "app"}.Object("sequence", "counter")
			d := newProgram(41, "app", contract.DBObject{Ref: ref, Name: ref.Name, Kind: "序列", Definition: "CREATE SEQUENCE app.counter;", Sequence: &contract.SequenceInfo{DataType: "bigint", Start: 9223372036854775805, Increment: -2, Min: -9223372036854775807, Max: 9223372036854775807, Cache: 17, Cycle: true}})
			tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); d.View(c) }, 840, 560)
			for _, label := range []string{"序列类型", "序列起点", "序列步长", "序列最小值", "序列最大值", "序列缓存", "序列循环", "序列定义（根据服务器目录参数重建）"} {
				if !tt.HasText(locales.Text(label)) {
					t.Fatal("missing localized sequence property", lang, label)
				}
			}
			for _, value := range []string{"9223372036854775805", "-9223372036854775807", "9223372036854775807", "-2", "CYCLE", "CREATE SEQUENCE app.counter;"} {
				if !tt.HasText(value) {
					t.Fatal("lost exact server value", value)
				}
			}
			if tt.HasText(locales.Text("对象定义（由服务器返回）")) || tt.HasText(locales.Text("返回类型")) {
				t.Fatal("sequence presented routine attributes or original DDL")
			}
			d.Close()
			d.Close()
			tt.Frame()
			if d.Data().Sequence != nil || tt.HasText("CREATE SEQUENCE app.counter;") {
				t.Fatal("closed sequence retained content")
			}
		})
	}
}

func TestProgramViewOmitsUnavailableAttributesAndExplainsMissingDefinition(t *testing.T) {
	d := newProgram(41, "app", mysqlsvc.MySQLDBObject{Name: "hidden_body", Kind: "函数", Definition: "  \n\t "})
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); d.View(c) }, 340, 400)
	if !tt.HasText("当前账号未获取到定义，可能受到服务器权限限制") {
		t.Fatal("permission-limited metadata lost its explanation")
	}
	for _, label := range []string{"定义者", "参数", "返回类型", "SQL SECURITY", "状态", "调度", "注释"} {
		if _, ok := tt.Find(label); ok {
			t.Fatalf("empty metadata fabricated an attribute: %s", label)
		}
	}
}

func TestProgramDefinitionDisplayBoundKeepsFullMetadata(t *testing.T) {
	for _, count := range []int{1, 499, 500, 501, 10000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			lines := make([]string, count)
			for i := range lines {
				lines[i] = fmt.Sprintf("line %d 中文", i+1)
			}
			body := strings.Join(lines, "\n")
			d := newProgram(41, "app", mysqlsvc.MySQLDBObject{Name: "long_body", Kind: "事件", Definition: body})
			display := definitionLines(d.Data().Definition)
			expected := min(count, 501)
			if len(display) != expected || display[0] != lines[0] || d.Data().Definition != body {
				t.Fatal("bounded display altered retained metadata")
			}
			if count > 500 {
				if display[499] != lines[499] || display[500] != "… (定义过长，仅展示前 500 行)" {
					t.Fatal("long definition bound or marker changed")
				}
			} else if display[len(display)-1] != lines[count-1] {
				t.Fatal("complete definition was truncated at the boundary")
			}
		})
	}
}

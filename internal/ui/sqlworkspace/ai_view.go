package sqlworkspace

import (
	"context"
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) aiModal(c *ui.Context) {
	s.consumeAI()
	a := &s.ai
	if !a.open {
		return
	}
	s.aiNeedsFrame(c)
	ui.Modal(c, &a.open, func() {
		p := theme.Colors(c)
		ui.Column(c).Width(720).MaxWidthPercent(95).Height(510).MaxHeightPercent(95).Padding(14).Gap(9).Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(10).Children(func() {
				ui.Text(c, locales.Text("AI SQL 助手")).Bold().Grow(1)
				if ui.Button(c, locales.Text("AI 服务设置")).Disabled(a.pending != nil).Clicked() {
					a.settings = !a.settings
				}
			})
			ui.Scroll(c).Grow(1).FillWidth().Children(func() {
				ui.Column(c).FillWidth().Gap(9).Children(func() {
					if a.settings {
						s.aiSettingsView(c)
					} else {
						s.aiRequestView(c)
					}
					if a.err != "" {
						ui.Text(c, a.err).TextColor(p.Error).FillWidth()
					}
					if a.status != "" {
						ui.Text(c, a.status).TextColor(p.Muted).FillWidth()
					}
				})
			})
			ui.Row(c).FillWidth().Gap(8).Justify(ui.End).Children(func() {
				if a.pending != nil {
					if ui.Button(c, locales.Text("取消 AI 请求")).Disabled(a.cancelled).Clicked() {
						a.cancelled = true
						if a.cancel != nil {
							a.cancel()
						}
					}
				}
				if ui.Button(c, locales.Text("关闭")).Clicked() {
					s.closeAI()
					return
				}
				if a.settings {
					if ui.PrimaryButton(c, locales.Text("保存 AI 设置")).Disabled(!a.loaded || a.pending != nil).Clicked() {
						s.saveAI()
					}
				} else {
					if ui.PrimaryButton(c, locales.Text("发送给 AI")).Disabled(!a.loaded || !s.aiConfigured() || a.pending != nil || !s.aiTargetValid()).Clicked() {
						s.requestAI()
					}
					if ui.Button(c, locales.Text("插入原 SQL 文档")).Disabled(!a.complete || a.pending != nil || a.mode == "解释 SQL" || !s.aiTargetValid()).Clicked() {
						s.applyAI()
						return
					}
					if ui.Button(c, locales.Text("作为新 SQL 草稿")).Disabled(!a.complete || a.pending != nil || a.mode == "解释 SQL" || s.deps.AIProposal == nil).Clicked() {
						profile, database, text := a.target.profile, a.target.database, aiSQLText(a.output.Buffer.String())
						s.deps.AIProposal(profile, database, text)
						s.closeAI()
					}
				}
			})
		})
	})
	if !a.open {
		s.closeAI()
	}
}
func (s *State) aiSettingsView(c *ui.Context) {
	a := &s.ai
	ui.Text(c, locales.Text("配置 OpenAI 兼容 Chat Completions 服务")).Bold()
	ui.Text(c, locales.Text("HTTPS 服务或本机 HTTP 服务；API 密钥只保存在系统密钥环。")).FontSize(theme.Font(c, 11))
	if !a.loaded {
		ui.Text(c, locales.Text("正在读取 AI 设置…"))
		return
	}
	disabled := a.pending != nil
	for _, f := range []struct {
		label string
		value *string
	}{{"AI API 地址", &a.draft.Endpoint}, {"AI 模型名称", &a.draft.Model}} {
		ui.Text(c, locales.Text(f.label))
		ui.TextInput(c, f.value).Label(locales.Text(f.label)).Disabled(disabled).FillWidth()
	}
	ui.Checkbox(c, &a.draft.Anonymous, locales.Text("本机或自有服务不需要 API 密钥")).Disabled(disabled)
	if !a.draft.Anonymous {
		ui.TextInput(c, &a.key).Password().Label(locales.Text("AI API 密钥")).Placeholder(locales.Text("留空保留已有密钥")).Disabled(disabled).FillWidth()
		if a.snapshot.Config.KeyID != "" {
			ui.Text(c, locales.Text("已关联系统密钥环记录"))
		}
		ui.Checkbox(c, &a.forget, locales.Text("移除已保存的 AI 密钥")).Disabled(disabled)
	}
	ui.Row(c).FillWidth().Gap(10).Children(func() {
		ui.Column(c).Grow(1).Gap(4).Children(func() {
			ui.Text(c, locales.Text("AI 输出 token 上限"))
			ui.TextInput(c, &a.tokens).Label(locales.Text("AI 输出 token 上限")).Disabled(disabled).FillWidth()
		})
		ui.Column(c).Grow(1).Gap(4).Children(func() {
			ui.Text(c, locales.Text("AI 超时（秒）"))
			ui.TextInput(c, &a.timeout).Label(locales.Text("AI 超时（秒）")).Disabled(disabled).FillWidth()
		})
	})
	ui.Text(c, locales.Text("token 限制参数（按服务支持选择）"))
	ui.Select(c, &a.draft.TokenParameter, []string{"max_completion_tokens", "max_tokens"}).Label(locales.Text("AI token 参数")).Disabled(disabled).FillWidth()
	ui.Checkbox(c, &a.draft.Stream, locales.Text("流式显示 AI 建议")).Disabled(disabled)
	if len(a.snapshot.Config.Retired) > 0 {
		if ui.Button(c, locales.Text("重试清理旧 AI 密钥")).Disabled(disabled).Clicked() {
			store, before := a.store, a.snapshot
			s.aiJob("cleanup", func(ctx context.Context) aiReply {
				v, e := store.Cleanup(ctx, before)
				return aiReply{saved: v, err: e}
			})
		}
	}
}
func (s *State) aiRequestView(c *ui.Context) {
	a := &s.ai
	if a.target.doc == nil {
		ui.Text(c, locales.Text("请从 SQL 文档打开 AI 助手"))
		return
	}
	if !a.loaded {
		ui.Text(c, locales.Text("正在读取 AI 设置…"))
		return
	}
	ui.Text(c, a.snapshot.Config.Model+" · "+a.snapshot.Config.Endpoint).FontSize(theme.Font(c, 11))
	ui.Text(c, locales.Text("原连接")+" #"+strconv.FormatInt(a.target.profile, 10)+" · "+a.target.database).FontSize(theme.Font(c, 11))
	mode := locales.Text(a.mode)
	choices := []string{locales.Text("生成 SQL"), locales.Text("解释 SQL"), locales.Text("优化 SQL")}
	if ui.Select(c, &mode, choices).Label(locales.Text("AI 操作")).Disabled(a.pending != nil).FillWidth().Changed() {
		for i, value := range choices {
			if value == mode {
				a.mode = []string{"生成 SQL", "解释 SQL", "优化 SQL"}[i]
			}
		}
		a.complete = false
		a.output.Buffer.Set("")
	}
	ui.TextArea(c, &a.instruction).Label(locales.Text("AI 自然语言要求")).Placeholder(locales.Text("描述需要生成、解释或优化的 SQL")).Height(70).Disabled(a.pending != nil).FillWidth()
	ui.Row(c).Wrap().Gap(8).Children(func() {
		ui.Checkbox(c, &a.includeSQL, locales.Text("包含当前 SQL 或选区")).Disabled(a.pending != nil)
		ui.Checkbox(c, &a.includeSchema, locales.Text("包含已加载的对象和字段名称")).Disabled(a.pending != nil)
	})
	ui.Text(c, locales.Text("只发送下方预览内容；不自动发送表数据或执行建议。")).FontSize(theme.Font(c, 11))
	prompt, e := s.aiPrompt()
	if a.pending != nil || a.complete {
		prompt = a.sent
	}
	if e != nil && a.pending == nil && !a.complete {
		prompt = locales.Text("填写要求；解释和优化需要选择包含 SQL。文档变化后请重新打开助手。")
	}
	ui.TextArea(c, &prompt).ReadOnly(true).Label(locales.Text("AI 本次发送内容")).Height(105).FillWidth()
	if !s.aiTargetValid() {
		ui.Text(c, locales.Text("原文档或会话范围已变化；可另存建议为新草稿，请重新打开后继续编辑。"))
	}
	if a.output != nil {
		a.output.Label = locales.Text("AI 建议预览")
		theme.ApplySQLAppearance(c, a.output)
		editor.View(c, a.output).Height(155).FillWidth()
	}
}

package theme

import "github.com/nyakang/nyadb/locales"

// OutcomeLabel keeps wire values independent from their UI presentation.
func OutcomeLabel(value string) string {
	labels := map[string]string{"executed": "已执行", "not_executed": "未执行", "uncertain": "结果不确定", "partial": "部分完成", "success": "成功", "error": "失败", "cancelled": "已取消", "pending": "事务未提交", "skipped": "未执行"}
	if label, ok := labels[value]; ok {
		return locales.Text(label)
	}
	return locales.Text(value)
}
func TableActionLabel(value string) string {
	labels := map[string]string{"create": "创建表", "rename": "重命名表", "drop": "删除表", "delete_rows": "删除所有记录", "truncate": "清空表", "copy_structure": "复制表结构", "copy_data": "复制表数据"}
	if label, ok := labels[value]; ok {
		return locales.Text(label)
	}
	return locales.Text("未知操作")
}

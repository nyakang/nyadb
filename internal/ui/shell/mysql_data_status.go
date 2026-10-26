package shell

import "fmt"

// mysqlDataPageStatus describes only settled read results. A pending request or
// failed query must never display a fabricated row count or successful page.
func mysqlDataPageStatus(t *tab) string {
	if t == nil {
		return ""
	}
	limit := max(1, t.dataLimit)
	page := t.dataOffset/int64(limit) + 1
	if t.dataLoading {
		return fmt.Sprintf("正在读取第 %d 页…", page)
	}
	if t.dataReadInfo.Canceled {
		return fmt.Sprintf("读取已取消 · 第 %d 页", page)
	}
	if t.dataErr != "" {
		return fmt.Sprintf("读取失败 · 第 %d 页", page)
	}
	if t.dataPage == nil {
		return "尚未读取数据"
	}
	count := len(t.dataPage.Rows)
	if count == 0 {
		return fmt.Sprintf("已读取 0 行 · 第 %d 页", page)
	}
	first := t.dataOffset + 1
	last := first + int64(count) - 1
	return fmt.Sprintf("已读取 %d 行 · %d–%d · 第 %d 页", count, first, last, page)
}

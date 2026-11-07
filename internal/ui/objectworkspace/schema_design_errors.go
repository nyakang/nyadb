package objectworkspace

import (
	"errors"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/locales"
)

func schemaDesignError(err error) string {
	switch {
	case errors.Is(err, schemaddl.ErrOpenGaussNotNull):
		return locales.Text("openGauss 列存表的已有字段非空属性只读。")
	case errors.Is(err, schemaddl.ErrPostgreSQLNotNull):
		return locales.Text("未验证或继承的 PostgreSQL 非空约束只读；独立约束改名不受支持。")
	case errors.Is(err, schemaddl.ErrPostgreSQLCoreTypes):
		return locales.Text("PostgreSQL 表设计仅支持核心类型；用户类型保持只读。")
	case errors.Is(err, schemaddl.ErrPostgreSQLVersion):
		return locales.Text("PostgreSQL 表设计保存仅支持服务器版本 14–18。")
	case errors.Is(err, schemaddl.ErrUnsupported):
		return locales.Text("此结构修改暂不支持安全保存；请检查类型、表达式、分区、继承、字段顺序或身份序列选项。")
	case errors.Is(err, schemaddl.ErrConflict):
		return locales.Text("原表结构或引用对象已改变；保留草稿，请重新读取并核对。")
	case errors.Is(err, contract.ErrRevoked):
		return locales.Text("原表设计会话已失效；保留草稿，请核对执行结果")
	default:
		return err.Error()
	}
}

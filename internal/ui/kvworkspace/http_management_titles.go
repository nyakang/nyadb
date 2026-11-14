package kvworkspace

import (
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

func managementTitle(op contract.KVHTTPManagementOperation) string {
	switch op.Name {
	case "agent-self":
		return locales.Text("Agent 身份")
	case "probe-catalog":
		return locales.Text("Catalog 能力")
	case "probe-health":
		return locales.Text("健康检查能力")
	case "probe-sessions":
		return locales.Text("Session 能力")
	case "probe-acl":
		return locales.Text("ACL 能力")
	case "probe-auth-methods":
		return locales.Text("认证方法能力")
	case "probe-binding-rules":
		return locales.Text("绑定规则能力")
	case "probe-acl-templates":
		return locales.Text("ACL 模板能力")
	case "agent-members":
		return locales.Text("Agent 成员")
	case "agent-metrics":
		return locales.Text("Agent 指标")
	case "agent-service":
		return locales.Text("Agent 服务详情")
	case "leader":
		return locales.Text("集群 Leader")
	case "peers":
		return locales.Text("Raft 成员")
	case "autopilot-config":
		return locales.Text("Autopilot 配置")
	case "autopilot-health":
		return locales.Text("Autopilot 健康状态")
	case "autopilot-state":
		return locales.Text("Autopilot 状态")
	case "autopilot-update":
		return locales.Text("更新 Autopilot")
	case "raft-config":
		return locales.Text("Raft 配置")
	case "raft-transfer":
		return locales.Text("转移 Raft 领导权")
	case "raft-remove":
		return locales.Text("移除 Raft 成员")
	case "keyring-list":
		return locales.Text("Gossip 密钥环")
	case "keyring-install":
		return locales.Text("安装 Gossip 密钥")
	case "keyring-use":
		return locales.Text("使用 Gossip 密钥")
	case "keyring-remove":
		return locales.Text("移除 Gossip 密钥")
	case "usage":
		return locales.Text("资源使用情况")
	case "license-get":
		return locales.Text("Enterprise 许可证")
	case "license-put":
		return locales.Text("更新 Enterprise 许可证")
	case "audit-hash":
		return locales.Text("Enterprise 审计哈希")
	case "query-list":
		return locales.Text("预备查询列表")
	case "query-get":
		return locales.Text("预备查询详情")
	case "query-create":
		return locales.Text("创建预备查询")
	case "query-update":
		return locales.Text("更新预备查询")
	case "query-delete":
		return locales.Text("删除预备查询")
	case "query-execute":
		return locales.Text("执行预备查询")
	case "query-explain":
		return locales.Text("解释预备查询")
	case "event-list":
		return locales.Text("事件列表")
	case "event-fire":
		return locales.Text("发送事件")
	case "coordinate-nodes":
		return locales.Text("网络坐标")
	case "config-list":
		return locales.Text("配置条目列表")
	case "config-get":
		return locales.Text("配置条目详情")
	case "config-apply":
		return locales.Text("应用配置条目")
	case "config-delete":
		return locales.Text("删除配置条目")
	case "intentions-list":
		return locales.Text("意图列表")
	case "intention-get":
		return locales.Text("意图详情")
	case "intention-create":
		return locales.Text("创建意图")
	case "intention-update":
		return locales.Text("更新意图")
	case "intention-delete":
		return locales.Text("删除意图")
	case "intention-exact-get":
		return locales.Text("读取精确意图")
	case "intention-exact-put":
		return locales.Text("应用精确意图")
	case "intention-exact-delete":
		return locales.Text("删除精确意图")
	case "intention-match":
		return locales.Text("匹配意图")
	case "intention-check":
		return locales.Text("检查意图")
	case "discovery-chain":
		return locales.Text("服务发现链")
	case "peering-list":
		return locales.Text("Peering 列表")
	case "peering-get":
		return locales.Text("Peering 详情")
	case "peering-token":
		return locales.Text("生成 Peering Token")
	case "peering-establish":
		return locales.Text("建立 Peering")
	case "peering-delete":
		return locales.Text("删除 Peering")
	case "exported-services":
		return locales.Text("导出服务")
	case "namespace-list":
		return locales.Text("Enterprise Namespace 列表")
	case "namespace-get":
		return locales.Text("Enterprise Namespace 详情")
	case "namespace-create":
		return locales.Text("创建 Enterprise Namespace")
	case "namespace-update":
		return locales.Text("更新 Enterprise Namespace")
	case "namespace-delete":
		return locales.Text("删除 Enterprise Namespace")
	case "partition-list":
		return locales.Text("Enterprise Partition 列表")
	case "partition-get":
		return locales.Text("Enterprise Partition 详情")
	case "partition-create":
		return locales.Text("创建 Enterprise Partition")
	case "partition-update":
		return locales.Text("更新 Enterprise Partition")
	case "partition-delete":
		return locales.Text("删除 Enterprise Partition")
	}
	return op.Title
}

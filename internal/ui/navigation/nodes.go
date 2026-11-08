package navigation

import (
	"encoding/json"
	"strings"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

// Asset navigator geometry is intentionally independent of the rest of the
// workbench. A 19-DIP content row plus Outline's vertical padding gives a
// 21-DIP pitch, and 12.5-DIP text stays readable without a global font bump.
const (
	assetRowHeight float32 = theme.TreeRowHeight
	assetFontSize  float32 = 12.5
)

// Keys are independent of display labels: the same table name in two databases
// must never share selection, expansion, or a context-menu target.
type Node struct {
	Kind      string
	ProfileID int64
	Database  string
	Category  string
	Object    string
	Catalog   string `json:",omitempty"`
	GroupID   string `json:",omitempty"`
}

const navigationPrefix = "\x1fnavigation:"

func (n Node) Key() string {
	b, _ := json.Marshal(n)
	return navigationPrefix + string(b)
}

func ConnectionKey(id int64) string { return (Node{Kind: "connection", ProfileID: id}).Key() }
func DatabaseNode(id int64, database string) Node {
	return Node{Kind: "database", ProfileID: id, Database: database}
}

func Parse(key string) Node {
	var n Node
	if strings.HasPrefix(key, navigationPrefix) {
		_ = json.Unmarshal([]byte(strings.TrimPrefix(key, navigationPrefix)), &n)
	}
	return n
}

// Database programs are server metadata. Named queries also have a local,
// connection-scoped catalog that can be browsed while disconnected.
var mysqlNavigationCategories = []string{"表", "视图", "函数", "存储过程", "事件", "查询", "备份"}
var oracleNavigationCategories = []string{"表", "视图", "函数", "存储过程", "包", "触发器", "序列", "类型", "查询"}
var postgresNavigationCategories = []string{"表", "视图", "函数", "存储过程", "序列", "查询"}

func SupportedCategory(category string) bool {
	switch category {
	case "集合信息", "向量点", "集合别名", "任务", "索引信息", "索引设置", "索引统计", "文档", "数据库信息", "设计文档", "Core", "索引", "别名", "数据流", "键", "集合", "表", "视图", "函数", "存储过程", "事件", "序列", "查询", "包", "触发器", "类型":
		return true
	}
	return false
}

func (s *State) databaseChildren(key string) []string {
	n := Parse(key)
	if s.sessionStatus(n.ProfileID) != "已连接" && n.Kind != "connection" && n.Category != "查询" {
		return nil
	}
	switch n.Kind {
	case "connection":
		if s.sessionStatus(n.ProfileID) != "已连接" {
			return nil
		}
		var local []string
		if s.deps.Queries != nil {
			local = append(local, (Node{Kind: "category", ProfileID: n.ProfileID, Category: "查询"}).Key())
		}
		if s.deps.Catalog != nil {
			if catalog := s.deps.Catalog(n.ProfileID); catalog != "" {
				return append([]string{(Node{Kind: "catalog", ProfileID: n.ProfileID, Catalog: catalog}).Key()}, local...)
			}
		}
		var remote []string
		for _, db := range s.databases(n.ProfileID) {
			remote = append(remote, DatabaseNode(n.ProfileID, db).Key())
		}
		return append(remote, local...)
	case "catalog":
		databases := s.databases(n.ProfileID)
		children := make([]string, 0, len(databases))
		for _, db := range databases {
			children = append(children, DatabaseNode(n.ProfileID, db).Key())
		}
		return children
	case "database":
		if !s.databaseOpened(n.ProfileID, n.Database) {
			return nil
		}
		if _, loaded := s.objects(n.ProfileID, n.Database); !loaded {
			status := n
			status.Kind, status.Object = "status", "waiting"
			pending, err := s.loadState(n.ProfileID, n.Database)
			if pending {
				status.Object = "loading"
			} else if err != "" {
				status.Object = "error"
			}
			return []string{status.Key()}
		}
		children := make([]string, 0, len(mysqlNavigationCategories))
		categories := mysqlNavigationCategories
		if s.deps.Engine != nil {
			switch s.deps.Engine(n.ProfileID) {
			case connections.Qdrant:
				categories = []string{"集合信息", "向量点", "集合别名", "查询"}
			case connections.Meilisearch:
				categories = []string{"索引信息", "文档", "索引设置", "索引统计", "任务", "查询"}
			case connections.CouchDB:
				categories = []string{"文档", "数据库信息", "设计文档", "查询"}
			case connections.Cassandra:
				categories = []string{"表", "视图", "类型", "查询"}
			case connections.Solr:
				categories = []string{"Core", "集合", "别名", "查询"}
			case connections.Elasticsearch, connections.Easysearch:
				categories = []string{"索引", "别名", "数据流", "查询"}
			case connections.Redis:
				categories = []string{"键", "查询"}
			case connections.MongoDB:
				categories = []string{"集合", "视图", "查询"}
			case connections.Oracle, connections.Dameng:
				categories = oracleNavigationCategories
			case connections.PostgreSQL, connections.CockroachDB, connections.OpenGauss, connections.GaussDB, connections.Kingbase, connections.SQLServer, connections.DuckDB:
				categories = postgresNavigationCategories
			case connections.MySQL, connections.MariaDB:
			case connections.TiDB:
				categories = []string{"表", "视图", "序列", "查询"}
			case connections.OceanBase:
				categories = []string{"表", "视图", "序列", "函数", "存储过程", "事件", "查询"}
			default:
				categories = []string{"表", "视图", "查询"}
			}
		}
		for _, kind := range categories {
			child := n
			child.Kind, child.Category = "category", kind
			children = append(children, child.Key())
		}
		return children
	case "category", "query_group":
		if n.Category == "查询" && s.deps.Queries != nil {
			var children []string
			if n.Kind == "category" && s.deps.QueryGroups != nil {
				for _, g := range s.deps.QueryGroups(n.ProfileID) {
					child := n
					child.Kind, child.GroupID = "query_group", g.Kind
					children = append(children, child.Key())
				}
			}
			for _, q := range s.deps.Queries(n.ProfileID, n.Database) {
				if q.Group != n.GroupID {
					continue
				}
				child := n
				child.Kind, child.Object = "object", q.Kind
				children = append(children, child.Key())
			}
			return children
		}
		if !SupportedCategory(n.Category) {
			return nil
		}
		children := []string{}
		for obj := range s.objectList(n.ProfileID, n.Database) {
			if obj.Kind == n.Category {
				child := n
				child.Kind, child.Object = "object", obj.Name
				children = append(children, child.Key())
			}
		}
		return children
	}
	return nil
}

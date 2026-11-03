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
var postgresNavigationCategories = []string{"表", "视图", "函数", "存储过程", "序列", "查询"}

func SupportedCategory(category string) bool {
	switch category {
	case "表", "视图", "函数", "存储过程", "事件", "序列", "查询":
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
		var local []string
		if s.deps.Queries != nil {
			local = append(local, (Node{Kind: "category", ProfileID: n.ProfileID, Category: "查询"}).Key())
		}
		if s.sessionStatus(n.ProfileID) != "已连接" {
			return local
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
			case connections.PostgreSQL:
				categories = postgresNavigationCategories
			case connections.MySQL:
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

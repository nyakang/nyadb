package graphresult

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

const PropertyProjectionLimit = "graph_properties"

const propertyColumnsLimit = 1024
const propertyCellsLimit = 1 << 20
const propertyBytesLimit = 16 << 20
const propertyLayoutLimit = 64 << 10

type nodePropertyColumn struct {
	Source int
	Key    string
}

// This projection uses only retained rows. It cannot discover uncached keys,
// run more Cypher, or change the original native graph receipt and identities.
func nodeProperties(source contract.ResultSet, graph contract.GraphResult) contract.ResultSet {
	bounded := func() contract.ResultSet {
		source.Truncation = append(source.Truncation, PropertyProjectionLimit)
		return source
	}
	types := make([]map[string]map[string]bool, len(source.Columns))
	keepSource := make([]bool, len(source.Columns))
	count := len(source.Columns)
	for _, row := range graph.Rows {
		if len(row) != len(source.Columns) {
			return bounded()
		}
		for index, value := range row {
			if value.Kind == "null" {
				continue
			}
			if value.Kind != "node" || value.Node == nil || len(value.Node.Properties) == 0 {
				keepSource[index] = true
				continue
			}
			if types[index] == nil {
				types[index] = map[string]map[string]bool{}
			}
			seen := map[string]bool{}
			for _, property := range value.Node.Properties {
				if !utf8.ValidString(property.Name) || len(property.Name) > 4096 || seen[property.Name] {
					return bounded()
				}
				seen[property.Name] = true
				observed := types[index][property.Name]
				if observed == nil {
					count++
					if count > propertyColumnsLimit || len(graph.Rows) > propertyCellsLimit/count {
						return bounded()
					}
					observed = map[string]bool{}
					types[index][property.Name] = observed
				}
				if property.Value.Kind != "null" {
					if !utf8.ValidString(property.Value.Kind) || len(property.Value.Kind) > 256 {
						return bounded()
					}
					observed[property.Value.Kind] = true
				}
			}
		}
	}
	if count == len(source.Columns) {
		return source
	}

	result := contract.ResultSet{AffectedRows: source.AffectedRows, Columns: slices.Clone(source.Columns), Truncation: slices.Clone(source.Truncation)}
	for index := range source.Columns {
		result.OriginColumns = append(result.OriginColumns, index)
	}
	used := map[string]bool{}
	for _, column := range source.Columns {
		used[column.Name] = true
	}
	identities := make([]nodePropertyColumn, 0, count-len(source.Columns))
	indices := make([]map[string]int, len(source.Columns))
	for index, properties := range types {
		if len(properties) == 0 {
			continue
		}
		keys := make([]string, 0, len(properties))
		for key := range properties {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		indices[index] = map[string]int{}
		for _, key := range keys {
			base := source.Columns[index].Name + "." + key
			name := base
			for suffix := 2; used[name]; suffix++ {
				name = base + " (" + strconv.Itoa(suffix) + ")"
			}
			used[name] = true
			kinds := make([]string, 0, len(properties[key]))
			for kind := range properties[key] {
				kinds = append(kinds, kind)
			}
			slices.Sort(kinds)
			if len(kinds) == 0 {
				kinds = []string{"null"}
			}
			indices[index][key] = len(result.Columns)
			identities = append(identities, nodePropertyColumn{index, key})
			result.OriginColumns = append(result.OriginColumns, index)
			result.Columns = append(result.Columns, contract.Column{Name: name, DatabaseType: "Neo4j native " + strings.Join(kinds, " | "), Nullable: true})
		}
		if !keepSource[index] {
			result.DefaultHiddenColumns = append(result.DefaultHiddenColumns, index)
		}
	}
	key, _ := json.Marshal(struct {
		Sources    []string
		Properties []nodePropertyColumn
		Hidden     []int
	}{graph.Columns, identities, result.DefaultHiddenColumns})
	if len(key) > propertyLayoutLimit {
		return bounded()
	}
	result.LayoutKey = "neo4j-node-properties:" + string(key)
	budget := propertyBytesLimit - len(result.LayoutKey) - len(result.OriginColumns)*8
	for _, column := range result.Columns {
		budget -= len(column.Name) + len(column.DatabaseType) + 64
	}
	// Account for cell and row metadata before allocating the appended matrix.
	budget -= len(graph.Rows) * (len(result.Columns)*64 + 24)
	if budget < 0 {
		return bounded()
	}
	for rowIndex, row := range graph.Rows {
		cells := make([]contract.Cell, len(result.Columns))
		copy(cells, source.Rows[rowIndex])
		for index, cell := range source.Rows[rowIndex] {
			budget -= len(cell.Text) + len(cell.Bytes)
			cells[index] = cell
		}
		for index := len(source.Columns); index < len(cells); index++ {
			cells[index].Kind = contract.Null
		}
		for index, value := range row {
			if value.Kind != "node" || value.Node == nil {
				continue
			}
			for _, property := range value.Node.Properties {
				cell := propertyCell(property.Value)
				budget -= len(cell.Text) + len(cell.Bytes)
				cells[indices[index][property.Name]] = cell
			}
		}
		if budget < 0 {
			return bounded()
		}
		result.Rows = append(result.Rows, cells)
	}
	return result
}

func propertyCell(value contract.GraphValue) contract.Cell {
	switch value.Kind {
	case "null", "string", "integer", "float", "boolean", "bytes":
		return graphCell(value)
	default:
		// Keep zones, spatial references, duration components and list element
		// kinds in their native typed representation, without numeric narrowing.
		raw, _ := json.Marshal(value)
		return contract.Cell{Kind: contract.JSON, Text: string(raw)}
	}
}

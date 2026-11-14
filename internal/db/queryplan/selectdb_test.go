package queryplan

import "testing"

// Captured from the official SelectDB FE/BE 2.1.6 fixture, not rewritten
// into the distinct Doris 4.1.3 numeric scan serialization.
const selectDBNativePlan = `cost = 6.52252
PhysicalResultSink[252] ( outputExprs=[note#3] )
+--PhysicalProject[249]@4 ( stats=0.5, projects=[note#3] )
   +--PhysicalTopN[246]@3 ( limit=2, offset=0, orderKeys=[id#0 asc null first], phase=MERGE_SORT )
      +--PhysicalDistribute[243]@6 ( stats=0.5, distributionSpec=DistributionSpecGather )
         +--PhysicalTopN[240]@6 ( limit=2, offset=0, orderKeys=[id#0 asc null first], phase=LOCAL_SORT )
            +--PhysicalProject[237]@2 ( stats=0.5, projects=[note#3, id#0] )
               +--PhysicalFilter[234]@1 ( stats=0.5, predicates=(id#0 > 0) )
                  +--PhysicalOlapScan[records_dmv4m8xg9s6n]@0 ( stats=1 )`

func TestSelectDBNativeNamedScanAndCost(t *testing.T) {
	p, err := Parse("selectdb", dorisPlanResult(selectDBNativePlan))
	if err != nil || p.Engine != "selectdb" || p.Nodes[0].Cost == nil || *p.Nodes[0].Cost != 6.52252 {
		t.Fatal("SelectDB original product or optimizer cost lost", err)
	}
	n := p.Nodes[0].Children[0]
	for len(n.Children) > 0 {
		if n.Cost != nil {
			t.Fatal("SelectDB optimizer cost copied onto an operator")
		}
		n = n.Children[0]
	}
	if n.Label != "PhysicalOlapScan[records_dmv4m8xg9s6n]@0" || n.Rows == nil || *n.Rows != 1 {
		t.Fatal("SelectDB scan name or native row estimate lost")
	}
	if _, err = Parse("doris", dorisPlanResult(selectDBNativePlan)); err == nil {
		t.Fatal("SelectDB-specific scan changed Doris's strict native format")
	}
	selfJoin := dorisPlanResult("cost = 1\nPhysicalHashJoin[1] ( stats=2 )\n|--PhysicalOlapScan[records]@0 ( stats=1 )\n+--PhysicalOlapScan[records]@1 ( stats=1 )")
	if p, err := Parse("selectdb", selfJoin); err != nil || len(p.Nodes[0].Children[0].Children) != 2 {
		t.Fatal("SelectDB table names were mistaken for unique operator IDs", err)
	}
	union := dorisPlanResult("cost = 4.099\nPhysicalUnion@ ( qualifier=ALL, outputs=[id#10], regularChildrenOutputs=[[id#0], [id#5]], constantExprsList=[], stats=2 )\n|--PhysicalOlapScan[records]@0 ( stats=1 )\n+--PhysicalOlapScan[records]@2 ( stats=1 )")
	if p, err := Parse("selectdb", union); err != nil || p.Nodes[0].Children[0].Label != "PhysicalUnion@" || *p.Nodes[0].Children[0].Rows != 2 {
		t.Fatal("SelectDB ID-less union or its trailing native estimate lost", err)
	}
	insert := dorisPlanResult("cost = 0.0\nLogicalOlapTableSink[39] ( database=warehouse, targetTable=records, dmlCommandType=INSERT )\n+--PhysicalOneRowRelation[36]@0 ( expressions=[99 AS `id`#0] )")
	if p, err := Parse("selectdb", insert); err != nil || p.Nodes[0].Children[0].Label != "LogicalOlapTableSink[39]" || p.Nodes[0].Children[0].Rows != nil {
		t.Fatal("SelectDB logical insert sink was rewritten or given invented estimates", err)
	}
}

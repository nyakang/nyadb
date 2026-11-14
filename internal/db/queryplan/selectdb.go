package queryplan

import (
	"regexp"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// The native SelectDB 2.1.6 serializer identifies scans by table name. It
// does not provide a numeric operator ID. Self-joins may repeat that name.
var selectDBScan = regexp.MustCompile(`^(PhysicalOlapScan\[[A-Za-z][A-Za-z0-9_-]*\](?:@[0-9]+)?)(?: \( (.*) \))?$`)
var selectDBUnion = regexp.MustCompile(`^(PhysicalUnion@) \( (.*), stats=([^,]*) \)$`)
var selectDBSink = regexp.MustCompile(`^(LogicalOlapTableSink\[([0-9]+)\])(?: \( (.*) \))?$`)

func selectDBNode(text string) (contract.PlanNode, string, error) {
	// Native SelectDB INSERT plans retain a logical table sink over physical
	// children. Preserve its exact label instead of claiming it is physical.
	if match := selectDBSink.FindStringSubmatch(text); len(match) == 4 {
		return contract.PlanNode{Label: match[1], Detail: match[3]}, match[2], nil
	}
	if match := selectDBUnion.FindStringSubmatch(text); len(match) == 4 {
		node := contract.PlanNode{Label: match[1], Detail: match[2] + ", stats=" + match[3]}
		if match[3] == "null" {
			return node, "", nil
		}
		var err error
		node.Rows, err = dorisEstimate(match[3])
		return node, "", err
	}
	if match := selectDBScan.FindStringSubmatch(text); len(match) == 3 {
		node := contract.PlanNode{Label: match[1], Detail: match[2]}
		var err error
		node.Rows, err = dorisRows(match[1], match[2])
		return node, "", err
	}
	return dorisNode(text)
}

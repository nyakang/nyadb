package objectworkspace

import "github.com/nyakang/nyadb/internal/db/schemaddl"

// Only the overlapping field/constraint grammar is shared. Each product keeps
// its own catalog, planner, properties and execution capabilities.
func schemaNativeFieldGrammar(engine string) bool {
	return engine == "postgresql" || engine == "opengauss"
}

func parseSchemaNativeField(engine, raw string) (schemaddl.PostgreSQLField, error) {
	if engine == "opengauss" {
		return schemaddl.ParseOpenGaussField(raw)
	}
	return schemaddl.ParsePostgreSQLField(raw)
}

func planSchemaDesign(original schemaddl.Snapshot, desired schemaddl.Table, schema string) (schemaddl.Plan, error) {
	switch original.Engine {
	case "postgresql":
		return schemaddl.PlanPostgreSQL(original, desired, schema, true)
	case "opengauss":
		return schemaddl.PlanOpenGauss(original, desired, schema, true)
	case "sqlite":
		return schemaddl.PlanSQLite(original, desired, schema, true)
	default:
		return schemaddl.Plan{}, schemaddl.ErrUnsupported
	}
}

func schemaNotNullEditable(original schemaddl.Snapshot, id string) error {
	if !original.Exists {
		return nil
	}
	if original.Engine == "opengauss" {
		return schemaddl.OpenGaussNotNullEditable(original, id)
	}
	return schemaddl.PostgreSQLNotNullEditable(original.Postgres, id)
}

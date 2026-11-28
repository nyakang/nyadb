package manticore

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"strings"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func metadataShape(set contract.ResultSet, names ...string) bool {
	if len(set.Truncation) != 0 || len(set.Columns) != len(names) {
		return false
	}
	for i, name := range names {
		if set.Columns[i].Name != name {
			return false
		}
	}
	for _, row := range set.Rows {
		if len(row) != len(names) {
			return false
		}
		for _, cell := range row {
			if cell.Truncated || cell.Kind == contract.Null || cell.Kind == contract.Binary {
				return false
			}
		}
	}
	return true
}

func pluginObjects(n contract.NamespaceRef, set contract.ResultSet) ([]contract.DBObject, error) {
	if !metadataShape(set, "Type", "Name", "Library", "Users", "Extra") {
		return nil, contract.ErrUnsupported
	}
	var objects []contract.DBObject
	seen := map[string]bool{}
	for _, row := range set.Rows {
		if !strings.EqualFold(row[0].Text, "udf") {
			continue
		}
		name := row[1].Text
		if !validName(name) || seen[name] {
			return nil, contract.ErrUnsupported
		}
		seen[name] = true
		original := set
		original.Rows = [][]contract.Cell{row}
		raw, err := json.Marshal(contract.NativeMetadataObservation{Command: "SHOW PLUGINS", Result: original})
		if err != nil {
			return nil, err
		}
		objects = append(objects, contract.DBObject{Ref: n.Object("function", name), Name: name, Kind: "函数", Engine: "Manticore Search", ReturnType: row[4].Text, Comment: "SONAME " + row[2].Text + ", RETURNS " + row[4].Text, Definition: string(raw)})
	}
	return objects, nil
}

func (s *Session) indexObservation(ctx context.Context, c driver.Conn, o contract.ObjectRef) (contract.Definition, error) {
	command := "SHOW TABLE " + quote(o.Name) + " INDEXES"
	result, err := read(ctx, c, command, 0, nil, nil)
	observation := contract.NativeMetadataObservation{Command: command, Result: result}
	if err != nil {
		var server *mysqldriver.MySQLError
		if !errors.As(err, &server) || ctx.Err() != nil {
			return contract.Definition{}, err
		}
		observation.ErrorCode = server.Number
		observation.SQLState = string(server.SQLState[:])
		observation.Error = server.Message
		if secret := s.profileSecret(); secret != "" {
			observation.Error = strings.ReplaceAll(observation.Error, secret, "[redacted]")
		}
	} else if !metadataShape(result, "Name", "Type", "Enabled", "Percent") {
		return contract.Definition{}, contract.ErrUnsupported
	}
	raw, err := json.Marshal(observation)
	return contract.Definition{Name: o.Name, Kind: "manticore_indexes", SQL: string(raw)}, err
}

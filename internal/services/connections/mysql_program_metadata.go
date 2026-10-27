package connections

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ObjectsWithPrograms extends the read-only table/view browser with routines
// and scheduled events. All three INFORMATION_SCHEMA queries bind the schema;
// definitions are shown only to callers whose server grants permit reading them.
func (m *MySQLSessionManager) ObjectsWithPrograms(ctx context.Context, id int64, database string) ([]MySQLDBObject, error) {
	objects, err := m.Objects(ctx, id, database)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	session := m.sessions[id]
	m.mu.Unlock()
	if session == nil {
		return nil, ErrMySQLNotConnected
	}
	queryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	stop := context.AfterFunc(session.ctx, cancel)
	defer func() { stop(); cancel() }()

	const routinesQuery = `SELECT ROUTINE_NAME, ROUTINE_TYPE, COALESCE(DEFINER, ''),
 COALESCE(SECURITY_TYPE, ''), COALESCE(DTD_IDENTIFIER, ''),
 CAST(CREATED AS CHAR), CAST(LAST_ALTERED AS CHAR),
 COALESCE(ROUTINE_DEFINITION, ''), COALESCE(ROUTINE_COMMENT, '')
 FROM information_schema.ROUTINES
 WHERE ROUTINE_SCHEMA = ? AND ROUTINE_TYPE IN ('FUNCTION', 'PROCEDURE')
 ORDER BY ROUTINE_TYPE, ROUTINE_NAME`
	rows, err := session.db.QueryContext(queryCtx, routinesQuery, database)
	if err != nil {
		return nil, fmt.Errorf("read MySQL routines: %w", err)
	}
	for rows.Next() {
		var name, typ, definer, security, returns, definition, comment string
		var created, altered sql.NullString
		if err := rows.Scan(&name, &typ, &definer, &security, &returns, &created, &altered, &definition, &comment); err != nil {
			rows.Close()
			return nil, err
		}
		kind := "函数"
		if typ == "PROCEDURE" {
			kind = "存储过程"
		}
		obj := MySQLDBObject{Name: name, Kind: kind, Definer: definer, SQLSecurity: security,
			ReturnType: returns, Definition: definition, Comment: comment}
		if created.Valid {
			obj.CreatedAt = created.String
		}
		if altered.Valid {
			obj.ModifiedAt = altered.String
		}
		if len(objects) >= maxMySQLMetadataObjects {
			rows.Close()
			return nil, ErrMySQLMetadataLimit
		}
		objects = append(objects, obj)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	// A routine's SPECIFIC_NAME is a separate identity from its display name.
	// MySQL does not overload routine names within a schema, but keep the
	// type-qualified lookup so functions and procedures never share parameters.
	const parametersQuery = `SELECT SPECIFIC_NAME, COALESCE(ROUTINE_TYPE, ''),
 COALESCE(PARAMETER_MODE, ''), COALESCE(PARAMETER_NAME, ''),
 COALESCE(DTD_IDENTIFIER, '')
 FROM information_schema.PARAMETERS
 WHERE SPECIFIC_SCHEMA = ? AND ORDINAL_POSITION > 0
 ORDER BY SPECIFIC_NAME, ORDINAL_POSITION`
	parameters, err := session.db.QueryContext(queryCtx, parametersQuery, database)
	if err != nil {
		return nil, fmt.Errorf("read MySQL routine parameters: %w", err)
	}
	byRoutine := make(map[string][]string)
	for parameters.Next() {
		var name, typ, mode, argument, datatype string
		if err := parameters.Scan(&name, &typ, &mode, &argument, &datatype); err != nil {
			parameters.Close()
			return nil, err
		}
		param := strings.TrimSpace(strings.Join([]string{mode, argument, datatype}, " "))
		key := typ + "\x00" + name
		byRoutine[key] = append(byRoutine[key], param)
	}
	if err := parameters.Err(); err != nil {
		parameters.Close()
		return nil, err
	}
	if err := parameters.Close(); err != nil {
		return nil, err
	}
	for i := range objects {
		obj := &objects[i]
		if obj.Kind != "函数" && obj.Kind != "存储过程" {
			continue
		}
		typ := "FUNCTION"
		if obj.Kind == "存储过程" {
			typ = "PROCEDURE"
		}
		obj.Parameters = strings.Join(byRoutine[typ+"\x00"+obj.Name], ", ")
	}

	const eventsQuery = `SELECT EVENT_NAME, COALESCE(DEFINER, ''), COALESCE(STATUS, ''),
 COALESCE(EVENT_TYPE, ''), COALESCE(CAST(EXECUTE_AT AS CHAR), ''),
 COALESCE(CAST(STARTS AS CHAR), ''), COALESCE(CAST(ENDS AS CHAR), ''),
 COALESCE(CAST(INTERVAL_VALUE AS CHAR), ''), COALESCE(INTERVAL_FIELD, ''),
 COALESCE(ON_COMPLETION, ''), COALESCE(CAST(CREATED AS CHAR), ''),
 COALESCE(CAST(LAST_ALTERED AS CHAR), ''), COALESCE(EVENT_DEFINITION, ''),
 COALESCE(EVENT_COMMENT, '')
 FROM information_schema.EVENTS WHERE EVENT_SCHEMA = ? ORDER BY EVENT_NAME`
	events, err := session.db.QueryContext(queryCtx, eventsQuery, database)
	if err != nil {
		return nil, fmt.Errorf("read MySQL events: %w", err)
	}
	for events.Next() {
		var obj MySQLDBObject
		var typ, executeAt, interval, field string
		if err := events.Scan(&obj.Name, &obj.Definer, &obj.Status, &typ, &executeAt,
			&obj.Starts, &obj.Ends, &interval, &field, &obj.OnCompletion,
			&obj.CreatedAt, &obj.ModifiedAt, &obj.Definition, &obj.Comment); err != nil {
			events.Close()
			return nil, err
		}
		obj.Kind = "事件"
		if typ == "ONE TIME" {
			obj.Schedule = "AT " + executeAt
		} else {
			obj.Schedule = strings.TrimSpace("EVERY " + interval + " " + field)
		}
		if len(objects) >= maxMySQLMetadataObjects {
			events.Close()
			return nil, ErrMySQLMetadataLimit
		}
		objects = append(objects, obj)
	}
	if err := events.Err(); err != nil {
		events.Close()
		return nil, err
	}
	if err := events.Close(); err != nil {
		return nil, err
	}
	if err := queryCtx.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(objects, func(i, j int) bool {
		if objects[i].Kind != objects[j].Kind {
			return objects[i].Kind < objects[j].Kind
		}
		return objects[i].Name < objects[j].Name
	})
	return objects, nil
}

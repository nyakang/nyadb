package schemaddl

import "errors"

var ErrPostgreSQLVersion = errors.New("PostgreSQL table design requires server version 14–18")
var ErrPostgreSQLCoreTypes = errors.New("PostgreSQL table design requires supported core types; user types are read-only")

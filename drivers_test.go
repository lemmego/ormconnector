package ormconnector

// The driver this package's tests open, imported here rather than by the
// package itself.
//
// This module used to blank-import sqlite, MySQL and Postgres from
// ormconnector.go, so every application linked all three and paid for the two
// it had never configured — the pure-Go sqlite driver alone is twenty-nine
// packages of translated libc. Registering a driver is the application's
// choice, which is the arrangement database/sql was designed around.
//
// A test file keeps it out of consumers' builds while leaving the suite able
// to open a database.
// Postgres and MySQL are here for the same reason, and arrived later: until
// migration v0.6.0 this module got all three registered transitively, because
// migration.go blank-imported them. v0.6.0 registers none, so the integration
// suites went from passing to "unknown driver" — which is the forgotten import
// the message names, in the test binary rather than in a consumer's.
import (
	_ "github.com/glebarez/go-sqlite"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
)

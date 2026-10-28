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
import _ "github.com/glebarez/go-sqlite"

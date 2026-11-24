package ormconnector

import (
	"testing"

	"github.com/lemmego/api/db"
	"github.com/lemmego/migration"
	"github.com/lemmego/orm"
)

// The dialect must be chosen from the driver as CONFIGURED, not after
// normalisation — and this test exists because the two differ for exactly one
// backend, in a way that is silent.
//
// driverName collapses every SQLite-family spelling onto "sqlite" so the ORM
// and `lemmego migrate` open the same database under the same name. libSQL has
// to take part in that, because its DDL *is* SQLite's. But its driver refuses
// a read-only transaction, and orm.LibSQL() is the SQLite dialect carrying
// exactly that one difference. So a dialect resolved after normalisation can
// only ever be orm.SQLite(), which silently restores an option
// lemmego/tenancy passes on every read path and libSQL rejects.
func TestTheDialectComesFromTheConfiguredDriverNotTheNormalisedOne(t *testing.T) {
	normalised, err := driverName(DriverLibSQL)
	if err != nil {
		t.Fatalf("driverName(%q): %v", DriverLibSQL, err)
	}
	if normalised != migration.DriverSQLite {
		t.Fatalf("driverName(%q) = %q, want %q — libSQL must target sqlite's DDL",
			DriverLibSQL, normalised, migration.DriverSQLite)
	}

	// The bug this guards: resolving from the normalised name.
	viaNormalised, err := DialectFor(normalised)
	if err != nil {
		t.Fatal(err)
	}
	if orm.SupportsReadOnlyTransactions(viaNormalised) != true {
		t.Error("the normalised name should resolve to plain SQLite; the test's premise is wrong")
	}

	// The fix: resolving from what was configured.
	viaConfigured, err := DialectFor(DriverLibSQL)
	if err != nil {
		t.Fatal(err)
	}
	if viaConfigured.Name() != DriverLibSQL {
		t.Errorf("DialectFor(%q).Name() = %q, want %q", DriverLibSQL, viaConfigured.Name(), DriverLibSQL)
	}
	if orm.SupportsReadOnlyTransactions(viaConfigured) {
		t.Error("DialectFor(\"libsql\") kept read-only transactions, so the capability was lost")
	}
}

func TestDialectForAcceptsTheAliasesParseDialectKnows(t *testing.T) {
	for _, c := range []struct{ configured, want string }{
		{"sqlite", "sqlite"},
		{"sqlite3", "sqlite"},
		{"libsql", "libsql"},
		{"LibSQL", "libsql"},
		{"postgres", "postgres"},
		{"postgresql", "postgres"},
		{"pgsql", "postgres"},
		{"pgx", "postgres"},
		{"mysql", "mysql"},
		{"mariadb", "mysql"},
	} {
		dialect, err := DialectFor(c.configured)
		if err != nil {
			t.Errorf("DialectFor(%q): %v", c.configured, err)
			continue
		}
		if dialect.Name() != c.want {
			t.Errorf("DialectFor(%q).Name() = %q, want %q", c.configured, dialect.Name(), c.want)
		}
	}
	if _, err := DialectFor("nonsense"); err == nil {
		t.Error("an unknown driver was accepted")
	}
}

// database/sql must be asked to open "libsql", not "sqlite". Opening "sqlite"
// on a project that also imports a SQLite driver would succeed against a local
// FILE and look like it worked, which is the worst available outcome.
func TestLibSQLOpensItsOwnRegisteredDriverName(t *testing.T) {
	if got := registeredDriverFor(DriverLibSQL, migration.DriverSQLite); got != DriverLibSQL {
		t.Errorf("registeredDriverFor(libsql, sqlite) = %q, want %q", got, DriverLibSQL)
	}
	if got := registeredDriverFor("sqlite3", migration.DriverSQLite); got != migration.DriverSQLite {
		t.Errorf("registeredDriverFor(sqlite3, sqlite) = %q, want %q", got, migration.DriverSQLite)
	}
	if got := registeredDriverFor("pgx", migration.DriverPostgres); got != migration.DriverPostgres {
		t.Errorf("registeredDriverFor(pgx, postgres) = %q, want %q", got, migration.DriverPostgres)
	}
}

// libsql has to survive the framework's one normaliser, or driverName rejects
// it before a dialect is ever chosen.
func TestParseDialectKnowsLibSQL(t *testing.T) {
	dialect, known := db.ParseDialect(DriverLibSQL)
	if !known {
		t.Fatalf("db.ParseDialect(%q) is unknown", DriverLibSQL)
	}
	if dialect != db.SQLite {
		t.Errorf("db.ParseDialect(%q) = %q, want %q — libSQL's SQL is SQLite's",
			DriverLibSQL, dialect, db.SQLite)
	}
}

// migration duplicates db.ParseDialect's alias table, because api requires
// migration and importing api/db there would be a cycle. This module imports
// both, so it is where the duplication is kept honest.
func TestMigrationAndParseDialectAgreeOnEverySpelling(t *testing.T) {
	for _, spelling := range []string{
		"sqlite", "sqlite3", "libsql",
		"mysql", "mariadb",
		"postgres", "postgresql", "pgsql", "pgx",
	} {
		fromMigration, okMigration := migration.NormaliseDialect(spelling)
		fromAPI, okAPI := db.ParseDialect(spelling)
		if okMigration != okAPI {
			t.Errorf("%q: migration knows it=%v, db.ParseDialect knows it=%v",
				spelling, okMigration, okAPI)
			continue
		}
		if fromMigration != string(fromAPI) {
			t.Errorf("%q: migration says %q, db.ParseDialect says %q",
				spelling, fromMigration, fromAPI)
		}
	}

	// And neither invents a dialect the other refuses.
	for _, spelling := range []string{"nonsense", "mongo"} {
		_, okMigration := migration.NormaliseDialect(spelling)
		_, okAPI := db.ParseDialect(spelling)
		if okMigration || okAPI {
			t.Errorf("%q accepted: migration=%v api=%v", spelling, okMigration, okAPI)
		}
	}
}

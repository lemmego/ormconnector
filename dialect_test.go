package ormconnector

import (
	"testing"

	"github.com/lemmego/api/db"
	"github.com/lemmego/migration"
)

// The framework now has one driver normaliser, db.ParseDialect, but two
// vocabularies read its result: the seam's db.Dialect and the migration
// module's driver names. They agree today and nothing in either package
// would notice if one drifted, so pin them here — this is the one module
// that already depends on both.
func TestDialectNamesMatchMigrationDrivers(t *testing.T) {
	for _, tc := range []struct {
		dialect db.Dialect
		driver  string
	}{
		{db.SQLite, migration.DriverSQLite},
		{db.MySQL, migration.DriverMySQL},
		{db.Postgres, migration.DriverPostgres},
	} {
		if string(tc.dialect) != tc.driver {
			t.Errorf("db dialect %q != migration driver %q", tc.dialect, tc.driver)
		}
	}
}

// db.ParseDialect recognises SQL Server, because gormconnector can open one.
// The migration module cannot target it, so this connector must still refuse
// it rather than pass a dialect name migrate would not understand.
func TestDriverNameRejectsSQLServer(t *testing.T) {
	for _, driver := range []string{"sqlserver", "mssql"} {
		if _, known := db.ParseDialect(driver); !known {
			t.Fatalf("test premise changed: db.ParseDialect no longer knows %q", driver)
		}
		if got, err := driverName(driver); err == nil {
			t.Errorf("driverName(%q) = %q, want an error", driver, got)
		}
	}
}

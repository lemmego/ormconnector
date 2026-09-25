package ormconnector

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lemmego/gpa"
	"github.com/lemmego/gpaorm"
	"github.com/lemmego/migration"
	"github.com/lemmego/orm"
)

type Category struct {
	ID   int `orm:"primaryKey;autoIncrement"`
	Name string
}

type Product struct {
	ID         int `orm:"primaryKey;autoIncrement"`
	CategoryID int
	Title      string
}

func TestDriverNameNormalisation(t *testing.T) {
	cases := map[string]string{
		"sqlite": migration.DriverSQLite, "sqlite3": migration.DriverSQLite, "SQLite": migration.DriverSQLite,
		"mysql": migration.DriverMySQL, "mariadb": migration.DriverMySQL,
		"postgres": migration.DriverPostgres, "postgresql": migration.DriverPostgres, "pgsql": migration.DriverPostgres,
	}
	for input, want := range cases {
		got, err := driverName(input)
		if err != nil {
			t.Fatalf("%s: %v", input, err)
		}
		if got != want {
			t.Fatalf("%s resolved to %q, want %q", input, got, want)
		}
	}

	if _, err := driverName("oracle"); err == nil {
		t.Fatal("expected an unsupported driver to be rejected")
	}
	if _, err := driverName(""); err == nil {
		t.Fatal("expected an empty driver to be rejected")
	}
}

// The dialect the ORM uses must be derived from the same driver name the
// migration module uses, or the two would disagree about placeholders and
// quoting.
func TestDialectMatchesMigrationDriverNames(t *testing.T) {
	for _, driver := range SupportedDrivers() {
		dialect, err := DialectFor(driver)
		if err != nil {
			t.Fatalf("%s: %v", driver, err)
		}
		if dialect.Name() != driver {
			t.Fatalf("driver %q produced dialect %q; the names must match", driver, dialect.Name())
		}
	}
	if _, err := DialectFor("mssql"); err == nil {
		t.Fatal("expected an unsupported driver to be rejected")
	}
}

func TestConnectSQLite(t *testing.T) {
	db, err := Connect(gpa.Config{Driver: "sqlite3", Database: ":memory:", MaxOpenConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if db.Dialect().Name() != migration.DriverSQLite {
		t.Fatalf("unexpected dialect: %s", db.Dialect().Name())
	}
	if err := db.SQLDB().Ping(); err != nil {
		t.Fatal(err)
	}
}

func TestConnectRejectsBadDriver(t *testing.T) {
	if _, err := Connect(gpa.Config{Driver: "oracle", Database: "x"}); err == nil {
		t.Fatal("expected an unsupported driver to be rejected")
	}
	if _, err := Connect(gpa.Config{Driver: "postgres"}); err == nil {
		t.Fatal("expected a missing database name to be rejected")
	}
}

// The whole stack in one test: the connector opens the database, the migration
// module builds the schema, the ORM reads and writes it, and the GPA adapter
// serves the same data through gpa.Repository.
func TestConnectorMigrationORMAndGPATogether(t *testing.T) {
	ctx := context.Background()

	db, err := Connect(gpa.Config{Driver: "sqlite", Database: ":memory:", MaxOpenConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	// Schema comes from the migration module, as it would in a real app.
	for _, schema := range []string{
		migration.Create("categories", func(table *migration.Table) {
			table.Increments("id")
			table.String("name", 255)
		}).Build(),
		migration.Create("products", func(table *migration.Table) {
			table.Increments("id")
			table.Int("category_id")
			table.String("title", 255)
		}).Build(),
	} {
		if _, err := db.Exec(ctx, schema); err != nil {
			t.Fatalf("%v\nSQL: %s", err, schema)
		}
	}

	// The migrator takes the very same connection.
	if _, err := migration.Init(db.SQLDB(), db.Dialect().Name()); err != nil {
		t.Fatal(err)
	}

	// The ORM resolves the table the migration created. Under the old naive
	// pluraliser this would have looked for "categorys".
	info, err := orm.ModelInfoFor[Category]()
	if err != nil {
		t.Fatal(err)
	}
	if info.Table != "categories" {
		t.Fatalf("ORM resolved table %q, but the migration created \"categories\"", info.Table)
	}

	category := &Category{Name: "hardware"}
	if _, err := db.Model[Category]().Create(ctx, category); err != nil {
		t.Fatal(err)
	}
	if category.ID == 0 {
		t.Fatal("expected the generated key to be written back")
	}

	// The same connection, served through the GPA contracts.
	provider := gpaorm.New(db)
	repo := gpaorm.GetRepository[Product](provider)

	if err := repo.Create(ctx, &Product{CategoryID: category.ID, Title: "keyboard"}); err != nil {
		t.Fatal(err)
	}
	products, err := repo.Query(ctx, gpa.Where("category_id", gpa.OpEqual, category.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(products) != 1 || products[0].Title != "keyboard" {
		t.Fatalf("unexpected products: %#v", products)
	}

	// Writes through GPA are visible through the ORM, since it is one handle.
	count, err := db.Model[Product]().Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 product through the ORM, got %d", count)
	}
}

func TestProviderShutdownIsSafeWhenUnused(t *testing.T) {
	provider := &Provider{}
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown before Provide must be a no-op: %v", err)
	}
}

func TestProviderRegistersGPAProvider(t *testing.T) {
	db, err := Connect(gpa.Config{Driver: "sqlite", Database: ":memory:", MaxOpenConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	provider := &Provider{UseGPA: true, db: db, sqlDB: db.SQLDB()}

	gpa.RegisterDefault(gpaorm.New(db))
	resolved, err := gpa.Get[*gpaorm.Provider]()
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ProviderInfo().Name != gpaorm.ProviderName {
		t.Fatalf("unexpected provider name: %s", resolved.ProviderInfo().Name)
	}

	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Shutdown clears the registry when it owns it.
	if _, err := gpa.Get[*gpaorm.Provider](); err == nil {
		t.Fatal("expected the registry to be cleared on shutdown")
	}
}

func TestAddCommandsExposesFieldGenerator(t *testing.T) {
	commands := (&Provider{}).AddCommands()
	if len(commands) != 1 {
		t.Fatalf("expected one command, got %d", len(commands))
	}
	command := commands[0](nil)
	if !strings.HasPrefix(command.Use, "orm:fields") {
		t.Fatalf("unexpected command: %s", command.Use)
	}
	for _, flag := range []string{"out", "type", "all"} {
		if command.Flags().Lookup(flag) == nil {
			t.Fatalf("expected a --%s flag", flag)
		}
	}
}

func TestFieldGeneratorReportsMissingDirectory(t *testing.T) {
	command := genFieldsCmd(nil)
	command.SetArgs([]string{t.TempDir()})
	command.SetOut(&strings.Builder{})
	command.SetErr(&strings.Builder{})

	err := command.Execute()
	if err == nil {
		t.Fatal("expected an error for a directory with no models")
	}
	if errors.Is(err, context.Canceled) {
		t.Fatal("unexpected error kind")
	}
}

// The MySQL driver returns DATETIME columns as []byte unless parseTime is set,
// which makes every model with a time.Time field fail to scan. The connector
// must supply it rather than leaving users to discover it from a confusing
// error.
func TestMySQLParamsAreSupplied(t *testing.T) {
	cases := []struct{ name, dsn string }{
		{"no query", "root:@tcp(127.0.0.1:3306)/app"},
		{"trailing question mark", "root:@tcp(127.0.0.1:3306)/app?"},
		{"existing params", "root:@tcp(127.0.0.1:3306)/app?charset=utf8mb4"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := ensureMySQLParams(testCase.dsn)
			if !strings.Contains(got, "parseTime=true") {
				t.Fatalf("parseTime is missing from %q", got)
			}
			if !strings.Contains(got, "loc=UTC") {
				t.Fatalf("loc is missing from %q", got)
			}
		})
	}

	// An explicit setting must be respected, whatever its value.
	explicit := ensureMySQLParams("root:@tcp(127.0.0.1:3306)/app?parseTime=false&loc=Local")
	if strings.Contains(explicit, "parseTime=true") {
		t.Fatalf("an explicit parseTime must not be overridden: %s", explicit)
	}
	if strings.Contains(explicit, "loc=UTC") {
		t.Fatalf("an explicit loc must not be overridden: %s", explicit)
	}
	if strings.Count(explicit, "parseTime=") != 1 {
		t.Fatalf("parseTime was duplicated: %s", explicit)
	}
}

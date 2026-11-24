// Package ormconnector wires the Lemmego ORM into the application lifecycle.
//
// It opens the connection, publishes *orm.DB to the service container, and
// optionally registers a GPA provider so code written against
// gpa.Repository[T] keeps working.
package ormconnector

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/lemmego/api/app"
	"github.com/lemmego/api/config"
	"github.com/lemmego/api/db"
	"github.com/lemmego/gpa"
	"github.com/lemmego/gpaorm"
	"github.com/lemmego/migration"
	"github.com/lemmego/orm"
)

// Provider connects the ORM during application bootstrap.
type Provider struct {
	// UseGPA additionally registers a gpaorm provider, so repositories
	// resolved through gpa.MustGet keep working while code migrates over.
	UseGPA bool

	// Connection selects a named connection from the sql config tree.
	// Empty means the configured default.
	Connection string

	// Config overrides the configuration read from the app when set.
	Config gpa.Config

	// Options are passed to orm.Open, so an application can configure the
	// handle this provider builds.
	//
	// The one that matters today is orm.WithTenantEnter: a multi-tenant
	// application needs the database told which tenant a transaction is for
	// at the moment it opens, and the hook that does it has to be installed
	// on the handle. Without a way to pass it here, such an application
	// cannot use this provider at all — it has to open its own connection
	// and give up everything else the provider does.
	Options []orm.Option

	// Driver is the registered database/sql driver name to open, when it is
	// not the one the dialect is named after.
	//
	// For an instrumented driver — see ConnectWith. The dialect still comes
	// from configuration, so `lemmego migrate` and the ORM stay in step.
	Driver string

	db       *orm.DB
	sqlDB    *sql.DB
	connName string
	dialect  db.Dialect
}

// Provide opens the connection and registers it with the application.
func (p *Provider) Provide(a app.App) error {
	settings := p.Config
	connName := p.Connection
	if settings.Driver == "" {
		resolved, selected, err := sqlConfig(p.Connection)
		if err != nil {
			return err
		}
		settings, connName = resolved, selected
	}

	ormDB, err := ConnectWith(settings, p.Driver, p.Options...)
	if err != nil {
		return err
	}
	p.db = ormDB
	p.sqlDB = ormDB.SQLDB()
	p.connName = connName
	p.dialect, _ = db.ParseDialect(settings.Driver)

	a.AddService(ormDB)

	if p.UseGPA {
		provider := gpaorm.New(ormDB)
		if err := provider.Configure(settings); err != nil {
			return err
		}
		gpa.RegisterDefault(provider)
		a.AddService(provider)
	}

	// Publish the connection under the shared seam so framework packages
	// that need a table can find it without naming *orm.DB. This is outside
	// the UseGPA branch on purpose: the seam is about the pool, not about
	// which abstraction the application writes its own queries through.
	db.Register(a, p)
	return nil
}

// AddCommands contributes the descriptor generator as a CLI command, so the
// framework gains `orm:fields` without the CLI module needing to depend on the
// ORM.
func (p *Provider) AddCommands() []app.Command {
	return []app.Command{genFieldsCmd}
}

// Shutdown closes the connection.
func (p *Provider) Shutdown(ctx context.Context) error {
	if p.UseGPA {
		gpa.Registry().RemoveAll()
	}
	if p.sqlDB != nil {
		return p.sqlDB.Close()
	}
	return nil
}

// DB returns the ORM handle, once Provide has run.
func (p *Provider) DB() *orm.DB { return p.db }

// SQLDB returns the underlying connection, which is what the migration module
// takes. It is also half of db.Connection.
func (p *Provider) SQLDB() *sql.DB { return p.sqlDB }

// Dialect reports the SQL flavour of the open connection, completing
// db.Connection along with Name.
func (p *Provider) Dialect() db.Dialect { return p.dialect }

// Name reports which sql.connections key this connection was built from.
func (p *Provider) Name() string { return p.connName }

// Connect opens a connection and wraps it in an ORM handle.
//
// The DSN is built by the migration module so that the ORM and `lemmego
// migrate` always resolve the same database from the same settings.
func Connect(settings gpa.Config, options ...orm.Option) (*orm.DB, error) {
	return ConnectWith(settings, "", options...)
}

// ConnectWith opens the connection through a named database/sql driver,
// rather than the one the dialect is named after.
//
// For an instrumented driver. A wrapper is registered under a name of its own
// — sql.Register panics on a duplicate, so it cannot take "postgres" — and
// Connect opens the canonical name, so there was no way to reach one. The
// dialect still comes from the configured driver, which is what keeps
// `lemmego migrate` and the ORM generating the same SQL.
//
//	sql.Register("postgres-observed", observe.Wrap(&pq.Driver{}))
//	&ormconnector.Provider{Driver: "postgres-observed"}
//
// An empty name means the canonical one, so this is Connect.
func ConnectWith(settings gpa.Config, open string, options ...orm.Option) (*orm.DB, error) {
	driver, err := driverName(settings.Driver)
	if err != nil {
		return nil, err
	}
	// From settings.Driver, not driver: see DialectFor on why normalising
	// first would lose the libSQL capability.
	dialect, err := DialectFor(settings.Driver)
	if err != nil {
		return nil, err
	}

	// The dialect is already decided, so the name below is only what
	// database/sql is asked to open.
	open = strings.TrimSpace(open)
	if open == "" {
		open = registeredDriverFor(settings.Driver, driver)
	}

	dsn := settings.ConnectionURL
	if dsn == "" {
		source := &migration.DataSource{
			Driver:   driver,
			Host:     settings.Host,
			Username: settings.Username,
			Password: settings.Password,
			Name:     settings.Database,
		}
		if settings.Port > 0 {
			source.Port = strconv.Itoa(settings.Port)
		}
		source.Params = optionsToParams(settings.Options)

		built, dsnErr := source.String()
		if dsnErr != nil {
			return nil, fmt.Errorf("ormconnector: %w", dsnErr)
		}
		dsn = built
	}

	if driver == migration.DriverMySQL {
		dsn = ensureMySQLParams(dsn)
	}

	sqlDB, err := sql.Open(open, dsn)
	if err != nil {
		return nil, fmt.Errorf("ormconnector: cannot open %s: %w", open, err)
	}

	if settings.MaxOpenConns > 0 {
		sqlDB.SetMaxOpenConns(settings.MaxOpenConns)
	}
	if settings.MaxIdleConns > 0 {
		sqlDB.SetMaxIdleConns(settings.MaxIdleConns)
	}
	if settings.ConnMaxLifetime > 0 {
		sqlDB.SetConnMaxLifetime(settings.ConnMaxLifetime)
	}
	if settings.ConnMaxIdleTime > 0 {
		sqlDB.SetConnMaxIdleTime(settings.ConnMaxIdleTime)
	}

	return orm.Open(sqlDB, dialect, options...), nil
}

// DialectFor maps a driver name onto an ORM dialect.
//
// It accepts both the canonical names the migration module uses and the
// aliases db.ParseDialect knows, because it is called with the driver as
// CONFIGURED rather than after normalisation. That distinction is
// load-bearing: driverName collapses every SQLite-family spelling onto
// "sqlite" so the ORM and `lemmego migrate` open the same database under the
// same name, and collapsing happens before a dialect is chosen — so a dialect
// picked from the normalised name can only ever be orm.SQLite().
//
// libSQL is the case that makes it matter. Its SQL is SQLite's, so it must
// normalise to "sqlite" for migration's DDL; but its driver refuses a
// read-only transaction, and orm.LibSQL() is sqliteDialect carrying exactly
// that one difference. Resolving it to orm.SQLite() would silently restore an
// option lemmego/tenancy passes on every read path and libSQL answers "read
// only transactions are not supported" to.
func DialectFor(driver string) (orm.Dialect, error) {
	switch strings.ToLower(strings.TrimSpace(driver)) {
	case DriverLibSQL:
		return orm.LibSQL(), nil
	case migration.DriverSQLite, "sqlite3":
		return orm.SQLite(), nil
	case migration.DriverMySQL, "mariadb":
		return orm.MySQL(), nil
	case migration.DriverPostgres, "postgresql", "pgsql", "pgx":
		return orm.Postgres(), nil
	}
	return nil, fmt.Errorf("ormconnector: unsupported driver %q, want one of %s",
		driver, strings.Join(SupportedDrivers(), ", "))
}

// DriverLibSQL is the configured driver name for a libSQL server, and also the
// name github.com/tursodatabase/libsql-client-go registers with database/sql.
const DriverLibSQL = "libsql"

// driverName normalises the aliases people write in configuration.
// driverName normalises a configured driver onto the name the migration
// module uses, so the ORM and `lemmego migrate` always open the same database
// under the same driver name.
//
// The spellings live in db.ParseDialect, which is the one normaliser the
// framework shares. This function adds only what is specific to the ORM:
// rejecting a dialect that migration cannot target. TestDialectNamesMatch
// pins the two vocabularies together.
func driverName(driver string) (string, error) {
	if strings.TrimSpace(driver) == "" {
		return "", fmt.Errorf("ormconnector: no database driver configured")
	}
	switch dialect, known := db.ParseDialect(driver); {
	case !known, dialect == db.SQLServer:
		return "", fmt.Errorf("ormconnector: unsupported driver %q, want one of %s",
			driver, strings.Join(SupportedDrivers(), ", "))
	default:
		return string(dialect), nil
	}
}

// registeredDriverFor answers which name to hand database/sql.
//
// Normally the canonical dialect name, which is also what the driver packages
// register. libSQL is the exception: it normalises to "sqlite" for the
// dialect and for migration, but the driver registered with database/sql is
// "libsql", so opening "sqlite" would either fail or — worse, on a project
// that also imports a SQLite driver — open a local file and look like it
// worked.
func registeredDriverFor(configured, canonical string) string {
	if strings.EqualFold(strings.TrimSpace(configured), DriverLibSQL) {
		return DriverLibSQL
	}
	return canonical
}

// SupportedDrivers lists the drivers the connector can open. It is deliberately
// the same set the migration module supports, plus libSQL, which migration
// targets as sqlite; SQL Server is absent from both.
func SupportedDrivers() []string {
	return []string{migration.DriverSQLite, migration.DriverMySQL, migration.DriverPostgres, DriverLibSQL}
}

// ensureMySQLParams adds the connection settings the MySQL driver needs to
// hand back usable timestamps.
//
// Without parseTime the driver returns DATETIME columns as []byte, and any
// model with a time.Time field fails to scan with "unsupported Scan, storing
// driver.Value type []uint8 into type *time.Time" — a confusing error a long
// way from its cause. loc=UTC matches the UTC the ORM stamps automatic
// timestamps in, so values round-trip unchanged.
//
// A setting the caller specified explicitly is always left alone.
func ensureMySQLParams(dsn string) string {
	base, query, hasQuery := strings.Cut(dsn, "?")
	if !hasQuery {
		base = dsn
	}

	existing := map[string]bool{}
	parts := []string{}
	for _, part := range strings.Split(query, "&") {
		if part == "" {
			continue
		}
		key, _, _ := strings.Cut(part, "=")
		existing[strings.ToLower(key)] = true
		parts = append(parts, part)
	}

	if !existing["parsetime"] {
		parts = append(parts, "parseTime=true")
	}
	if !existing["loc"] {
		parts = append(parts, "loc=UTC")
	}
	return base + "?" + strings.Join(parts, "&")
}

func optionsToParams(options map[string]any) string {
	if len(options) == 0 {
		return ""
	}
	parts := make([]string, 0, len(options))
	for key, value := range options {
		parts = append(parts, fmt.Sprintf("%s=%v", key, value))
	}
	return strings.Join(parts, "&")
}

// sqlConfig reads the same configuration tree gormconnector reads, so swapping
// connectors needs no config changes.
// It also returns the connection name it resolved, which the seam reports so
// a package reading connection-scoped configuration knows which block to read.
func sqlConfig(connName string) (gpa.Config, string, error) {
	name := connName
	if name == "" {
		name = "default"
	}

	selected, ok := config.Get(fmt.Sprintf("sql.%s", name)).(string)
	if !ok || selected == "" {
		return gpa.Config{}, "", fmt.Errorf("ormconnector: sql.%s is not configured", name)
	}
	connection, ok := config.Get(fmt.Sprintf("sql.connections.%s", selected)).(config.M)
	if !ok {
		return gpa.Config{}, "", fmt.Errorf("ormconnector: sql.connections.%s is not configured", selected)
	}

	settings := gpa.Config{
		Driver:   connection.String("driver"),
		Database: connection.String("database"),
		// A whole DSN, for a backend whose address is a URL rather than a
		// host and a port — a libSQL server is "http://host:8080", which
		// migration.DataSource cannot express: it builds a sqlite file path,
		// a MySQL user:pass@tcp(...), or Postgres keywords and nothing else.
		// queue/config.go has read this key all along; this is the standard
		// connection block catching up.
		ConnectionURL: connection.String("url"),
	}
	if settings.Driver == "" {
		return gpa.Config{}, "", fmt.Errorf("ormconnector: sql.connections.%s needs a driver", selected)
	}
	// A url is an alternative to a database, not an addition to it.
	if settings.Database == "" && settings.ConnectionURL == "" {
		return gpa.Config{}, "", fmt.Errorf("ormconnector: sql.connections.%s needs a database or a url", selected)
	}

	if normalised, err := driverName(settings.Driver); err == nil && normalised != migration.DriverSQLite {
		settings.Host, _ = config.Get(fmt.Sprintf("sql.connections.%s.host", selected)).(string)
		settings.Port, _ = config.Get(fmt.Sprintf("sql.connections.%s.port", selected)).(int)
		settings.Username, _ = config.Get(fmt.Sprintf("sql.connections.%s.user", selected)).(string)
		settings.Password, _ = config.Get(fmt.Sprintf("sql.connections.%s.password", selected)).(string)
		if options, ok := config.Get(fmt.Sprintf("sql.connections.%s.options", selected)).(config.M); ok {
			settings.Options = options
		}
	}
	return settings, selected, nil
}

// Get returns the ORM handle from the service container.
func Get(a app.App) *orm.DB { return app.Get[*orm.DB](a) }

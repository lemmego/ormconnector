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
	"github.com/lemmego/gpa"
	"github.com/lemmego/gpaorm"
	"github.com/lemmego/migration"
	"github.com/lemmego/orm"

	// The same drivers the migration module registers, so the ORM and the
	// migrator open the same databases under the same driver names.
	_ "github.com/glebarez/go-sqlite"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
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

	db    *orm.DB
	sqlDB *sql.DB
}

// Provide opens the connection and registers it with the application.
func (p *Provider) Provide(a app.App) error {
	settings := p.Config
	if settings.Driver == "" {
		resolved, err := sqlConfig(p.Connection)
		if err != nil {
			return err
		}
		settings = resolved
	}

	db, err := Connect(settings)
	if err != nil {
		return err
	}
	p.db = db
	p.sqlDB = db.SQLDB()

	a.AddService(db)

	if p.UseGPA {
		provider := gpaorm.New(db)
		if err := provider.Configure(settings); err != nil {
			return err
		}
		gpa.RegisterDefault(provider)
		a.AddService(provider)
	}
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
// takes.
func (p *Provider) SQLDB() *sql.DB { return p.sqlDB }

// Connect opens a connection and wraps it in an ORM handle.
//
// The DSN is built by the migration module so that the ORM and `lemmego
// migrate` always resolve the same database from the same settings.
func Connect(settings gpa.Config) (*orm.DB, error) {
	driver, err := driverName(settings.Driver)
	if err != nil {
		return nil, err
	}
	dialect, err := DialectFor(driver)
	if err != nil {
		return nil, err
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

	sqlDB, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("ormconnector: cannot open %s: %w", driver, err)
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

	return orm.Open(sqlDB, dialect), nil
}

// DialectFor maps a driver name onto an ORM dialect. The names match those the
// migration module uses, which is what keeps the two in step.
func DialectFor(driver string) (orm.Dialect, error) {
	switch driver {
	case migration.DriverSQLite:
		return orm.SQLite(), nil
	case migration.DriverMySQL:
		return orm.MySQL(), nil
	case migration.DriverPostgres:
		return orm.Postgres(), nil
	}
	return nil, fmt.Errorf("ormconnector: unsupported driver %q, want one of %s",
		driver, strings.Join(SupportedDrivers(), ", "))
}

// driverName normalises the aliases people write in configuration.
func driverName(driver string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(driver)) {
	case "sqlite", "sqlite3":
		return migration.DriverSQLite, nil
	case "mysql", "mariadb":
		return migration.DriverMySQL, nil
	case "postgres", "postgresql", "pgsql":
		return migration.DriverPostgres, nil
	case "":
		return "", fmt.Errorf("ormconnector: no database driver configured")
	}
	return "", fmt.Errorf("ormconnector: unsupported driver %q, want one of %s",
		driver, strings.Join(SupportedDrivers(), ", "))
}

// SupportedDrivers lists the drivers the connector can open. It is deliberately
// the same set the migration module supports; SQL Server is absent from both.
func SupportedDrivers() []string {
	return []string{migration.DriverSQLite, migration.DriverMySQL, migration.DriverPostgres}
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
func sqlConfig(connName string) (gpa.Config, error) {
	name := connName
	if name == "" {
		name = "default"
	}

	selected, ok := config.Get(fmt.Sprintf("sql.%s", name)).(string)
	if !ok || selected == "" {
		return gpa.Config{}, fmt.Errorf("ormconnector: sql.%s is not configured", name)
	}
	connection, ok := config.Get(fmt.Sprintf("sql.connections.%s", selected)).(config.M)
	if !ok {
		return gpa.Config{}, fmt.Errorf("ormconnector: sql.connections.%s is not configured", selected)
	}

	settings := gpa.Config{
		Driver:   connection.String("driver"),
		Database: connection.String("database"),
	}
	if settings.Driver == "" || settings.Database == "" {
		return gpa.Config{}, fmt.Errorf("ormconnector: sql.connections.%s needs a driver and a database", selected)
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
	return settings, nil
}

// Get returns the ORM handle from the service container.
func Get(a app.App) *orm.DB { return app.Get[*orm.DB](a) }

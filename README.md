# ormconnector

Wires the [Lemmego ORM](../orm) into the application lifecycle.

## Usage

Register it like any other provider, in `bootstrap/providers.go`:

```go
func LoadProviders() []app.Provider {
    return []app.Provider{
        &fs.Provider{},
        &session.Provider{},
        &ormconnector.Provider{},
        // ...
    }
}
```

Then reach the connection from a handler or service:

```go
db := app.Get[*orm.DB](a)          // or ormconnector.Get(a)
users, err := db.Model[User]().Where(orm.Eq("active", true)).All(ctx)
```

It reads the same `sql` configuration tree `gormconnector` reads, so swapping
connectors needs no configuration changes.

### Running alongside GPA

Set `UseGPA` to also register a [`gpaorm`](../gpaorm) provider, so existing code
written against `gpa.Repository[T]` keeps working while it migrates:

```go
&ormconnector.Provider{UseGPA: true}
```

### Options

| Field | Meaning |
|---|---|
| `UseGPA` | also register a `gpaorm` provider as the GPA default |
| `Connection` | pick a named connection instead of the configured default |
| `Config` | supply a `gpa.Config` directly, bypassing the config tree |

## Driver and DSN parity with migrations

The DSN is built by the **migration module's** `DataSource`, and the driver
names are the migration module's constants (`sqlite`, `mysql`, `postgres`). That
is deliberate: the ORM and `lemmego migrate` must resolve the same database from
the same settings, and the ORM dialect is derived from the same driver name, so
placeholders and quoting cannot drift apart.

`DialectFor` is asserted in tests to return a dialect whose `Name()` equals the
driver it was given.

The supported set is therefore the same as the migration module's — SQLite,
MySQL and PostgreSQL. SQL Server is absent from both. Aliases (`sqlite3`,
`postgresql`, `pgsql`, `mariadb`) are normalised.

The connector blank-imports the same drivers the migration module registers, so
`*sql.DB` from `SQLDB()` can be handed straight to `migration.Init`.

## The `orm:fields` command

The provider contributes the descriptor generator as a CLI command:

```bash
go run ./cmd/app orm:fields ./internal/models
go run ./cmd/app orm:fields --type User,Post ./internal/models
```

It lives here rather than in the CLI module because generation must use the
ORM's own naming rules, and `/cli` does not depend on the ORM. The framework's
`CommandProvider` hook is exactly the seam for that.

## MySQL connection parameters

The connector supplies `parseTime=true` and `loc=UTC` unless you set them
yourself. Without `parseTime` the driver returns `DATETIME` columns as `[]byte`
and every model with a `time.Time` field fails to scan with *"unsupported Scan,
storing driver.Value type []uint8 into type \*time.Time"* — a confusing error a
long way from its cause. `loc=UTC` matches the UTC the ORM stamps automatic
timestamps in. An explicitly configured value is always respected.

## Verification

```bash
cd ormconnector
go build ./... && go vet ./... && go test ./... && go test -race ./...
```

The test suite includes an end-to-end check that opens a database through the
connector, builds the schema with the migration module, writes through the ORM,
and reads the same rows back through the GPA adapter.

### Integration tests against real servers

SQLite cannot exercise `$N` placeholders, `RETURNING`, or the driver error text
the dialects parse. Those paths are covered by integration tests that run only
when a DSN is configured, against an isolated database:

```bash
createdb lemmego_orm_test
mysql -u root -e "CREATE DATABASE lemmego_orm_test"

export ORM_POSTGRES_DSN="postgres://$USER@127.0.0.1:5432/lemmego_orm_test?sslmode=disable"
export ORM_MYSQL_DSN="root@tcp(127.0.0.1:3306)/lemmego_orm_test"

go test -run TestIntegration ./...
```

They are skipped when the variables are unset, so the default suite stays
hermetic. The same assertions run against every configured backend, so a
dialect that diverges shows up as a failure rather than a surprise in
production.

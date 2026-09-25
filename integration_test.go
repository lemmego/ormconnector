package ormconnector

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lemmego/gpa"
	"github.com/lemmego/gpaorm"
	"github.com/lemmego/migration"
	"github.com/lemmego/orm"
)

// Integration tests run against real database servers. They are skipped
// unless the corresponding DSN is configured, per the repository's policy that
// tests needing an external service use explicit configuration and an isolated
// database.
//
//	ORM_POSTGRES_DSN='postgres://user@127.0.0.1:5432/lemmego_orm_test?sslmode=disable'
//	ORM_MYSQL_DSN='root@tcp(127.0.0.1:3306)/lemmego_orm_test?parseTime=true'

type Member struct {
	ID        int `orm:"primaryKey;autoIncrement"`
	Email     string
	Age       *int
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time
	Posts     []MemberPost `orm:"hasMany:MemberID"`
}

type MemberPost struct {
	ID       int `orm:"primaryKey;autoIncrement"`
	MemberID int
	Title    string
}

type backend struct {
	name   string
	envVar string
	// ddl deliberately avoids the migration schema builder so that an ORM
	// failure is not confounded by a migration bug; the builder gets its own
	// test below.
	ddl []string
}

var backends = []backend{
	{
		name:   "postgres",
		envVar: "ORM_POSTGRES_DSN",
		ddl: []string{
			`DROP TABLE IF EXISTS member_posts`,
			`DROP TABLE IF EXISTS members`,
			`CREATE TABLE members (
				id SERIAL PRIMARY KEY,
				email TEXT NOT NULL UNIQUE,
				age INTEGER,
				created_at TIMESTAMP,
				updated_at TIMESTAMP,
				deleted_at TIMESTAMP,
				legacy_note TEXT
			)`,
			`CREATE TABLE member_posts (
				id SERIAL PRIMARY KEY,
				member_id INTEGER NOT NULL REFERENCES members(id),
				title TEXT
			)`,
		},
	},
	{
		name:   "mysql",
		envVar: "ORM_MYSQL_DSN",
		ddl: []string{
			`DROP TABLE IF EXISTS member_posts`,
			`DROP TABLE IF EXISTS members`,
			`CREATE TABLE members (
				id INT AUTO_INCREMENT PRIMARY KEY,
				email VARCHAR(255) NOT NULL UNIQUE,
				age INT,
				created_at DATETIME,
				updated_at DATETIME,
				deleted_at DATETIME,
				legacy_note TEXT
			) ENGINE=InnoDB`,
			`CREATE TABLE member_posts (
				id INT AUTO_INCREMENT PRIMARY KEY,
				member_id INT NOT NULL,
				title VARCHAR(255),
				FOREIGN KEY (member_id) REFERENCES members(id)
			) ENGINE=InnoDB`,
		},
	},
}

func openBackend(t *testing.T, b backend) *orm.DB {
	t.Helper()
	dsn := os.Getenv(b.envVar)
	if dsn == "" {
		t.Skipf("set %s to run the %s integration tests", b.envVar, b.name)
	}

	db, err := Connect(gpa.Config{Driver: b.name, ConnectionURL: dsn, MaxOpenConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	ctx := context.Background()
	for _, statement := range b.ddl {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatalf("%s: %v\nSQL: %s", b.name, err, statement)
		}
	}
	return db
}

func TestIntegration(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			db := openBackend(t, b)
			ctx := context.Background()

			t.Run("GeneratedKeyAndRoundTrip", func(t *testing.T) {
				member := &Member{Email: "key@example.com"}
				if _, err := db.Model[Member]().Create(ctx, member); err != nil {
					t.Fatal(err)
				}
				if member.ID == 0 {
					t.Fatal("the generated key was not written back")
				}
				found, err := db.Model[Member]().Find(ctx, member.ID)
				if err != nil {
					t.Fatal(err)
				}
				if found.Email != "key@example.com" {
					t.Fatalf("unexpected row: %#v", found)
				}
			})

			// The table has a legacy_note column the struct does not declare.
			t.Run("UnmappedColumnIgnored", func(t *testing.T) {
				if _, err := db.Exec(ctx,
					`INSERT INTO members (email, legacy_note) VALUES (`+ph(db, 1)+`, `+ph(db, 2)+`)`,
					"legacy@example.com", "note"); err != nil {
					t.Fatal(err)
				}
				members, err := db.Model[Member]().Where(orm.Eq("email", "legacy@example.com")).All(ctx)
				if err != nil {
					t.Fatalf("an unmapped column must not break reads: %v", err)
				}
				if len(members) != 1 {
					t.Fatalf("expected 1 row, got %d", len(members))
				}
			})

			// Timestamps must survive the driver round trip.
			t.Run("TimestampsRoundTrip", func(t *testing.T) {
				member := &Member{Email: "stamps@example.com"}
				if _, err := db.Model[Member]().Create(ctx, member); err != nil {
					t.Fatal(err)
				}
				if member.CreatedAt.IsZero() {
					t.Fatal("CreatedAt was not stamped")
				}
				stored, err := db.Model[Member]().Find(ctx, member.ID)
				if err != nil {
					t.Fatal(err)
				}
				if stored.CreatedAt.IsZero() {
					t.Fatalf("the timestamp did not survive the round trip: %#v", stored.CreatedAt)
				}
				if delta := stored.CreatedAt.Sub(member.CreatedAt); delta > time.Second || delta < -time.Second {
					t.Fatalf("timestamp drifted by %s", delta)
				}
			})

			t.Run("NullHandling", func(t *testing.T) {
				member := &Member{Email: "nulls@example.com"}
				if _, err := db.Model[Member]().Create(ctx, member); err != nil {
					t.Fatal(err)
				}
				found, err := db.Model[Member]().Find(ctx, member.ID)
				if err != nil {
					t.Fatal(err)
				}
				if found.Age != nil {
					t.Fatalf("expected a nil pointer for a NULL column, got %v", *found.Age)
				}
				if found.DeletedAt != nil {
					t.Fatal("expected DeletedAt to be nil")
				}
			})

			// The single most driver-specific part of the ORM: turning a
			// driver's error text into a typed constraint error.
			t.Run("UniqueViolation", func(t *testing.T) {
				first := &Member{Email: "dup@example.com"}
				if _, err := db.Model[Member]().Create(ctx, first); err != nil {
					t.Fatal(err)
				}
				_, err := db.Model[Member]().Create(ctx, &Member{Email: "dup@example.com"})

				var constraint *orm.ConstraintError
				if !errors.As(err, &constraint) {
					t.Fatalf("expected a *ConstraintError, got %#v", err)
				}
				if constraint.Kind != orm.ConstraintUnique {
					t.Fatalf("expected a unique violation, got %s (%v)", constraint.Kind, err)
				}
				if !orm.IsUniqueViolation(err) {
					t.Fatal("IsUniqueViolation should report true")
				}
			})

			t.Run("ForeignKeyViolation", func(t *testing.T) {
				_, err := db.Model[MemberPost]().Create(ctx, &MemberPost{MemberID: 987654, Title: "orphan"})

				var constraint *orm.ConstraintError
				if !errors.As(err, &constraint) {
					t.Fatalf("expected a *ConstraintError, got %#v", err)
				}
				if constraint.Kind != orm.ConstraintForeignKey {
					t.Fatalf("expected a foreign-key violation, got %s (%v)", constraint.Kind, err)
				}
			})

			t.Run("NotNullViolation", func(t *testing.T) {
				_, err := db.Exec(ctx, `INSERT INTO members (email) VALUES (NULL)`)
				classified := db.Dialect().ClassifyError(err)

				var constraint *orm.ConstraintError
				if !errors.As(classified, &constraint) {
					t.Fatalf("expected a *ConstraintError, got %#v", classified)
				}
				if constraint.Kind != orm.ConstraintNotNull {
					t.Fatalf("expected a not-null violation, got %s (%v)", constraint.Kind, err)
				}
			})

			// Placeholder numbering has to stay consistent across every clause,
			// which only $N dialects can really prove.
			t.Run("PlaceholderNumbering", func(t *testing.T) {
				for i := range 6 {
					age := 20 + i
					if _, err := db.Model[Member]().Create(ctx, &Member{
						Email: fmt.Sprintf("ph%d@example.com", i),
						Age:   &age,
					}); err != nil {
						t.Fatal(err)
					}
				}
				members, err := db.Model[Member]().
					Where(orm.Like("email", "ph%")).
					Where(orm.Between("age", 21, 24)).
					Where(orm.In("age", 21, 22, 23, 24, 25)).
					OrderBy(orm.Desc("age")).
					Limit(2).
					Offset(1).
					All(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if len(members) != 2 || *members[0].Age != 23 {
					t.Fatalf("unexpected rows: %#v", members)
				}
			})

			t.Run("EagerLoading", func(t *testing.T) {
				member := &Member{Email: "eager@example.com"}
				if _, err := db.Model[Member]().Create(ctx, member); err != nil {
					t.Fatal(err)
				}
				for i := range 3 {
					if _, err := db.Model[MemberPost]().Create(ctx, &MemberPost{
						MemberID: member.ID, Title: fmt.Sprintf("post %d", i),
					}); err != nil {
						t.Fatal(err)
					}
				}
				loaded, err := db.Model[Member]().Where(orm.Eq("id", member.ID)).With("Posts").All(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if len(loaded) != 1 || len(loaded[0].Posts) != 3 {
					t.Fatalf("expected 3 eager-loaded posts, got %#v", loaded)
				}
			})

			t.Run("SoftDelete", func(t *testing.T) {
				member := &Member{Email: "soft@example.com"}
				if _, err := db.Model[Member]().Create(ctx, member); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Model[Member]().Delete(ctx, member); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Model[Member]().Find(ctx, member.ID); !errors.Is(err, orm.ErrNotFound) {
					t.Fatalf("a soft-deleted row must be hidden, got %v", err)
				}
				trashed, err := db.Model[Member]().WithTrashed().Where(orm.Eq("id", member.ID)).Count(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if trashed != 1 {
					t.Fatalf("the row should still exist, found %d", trashed)
				}
			})

			t.Run("TransactionRollback", func(t *testing.T) {
				before, err := db.Model[Member]().Count(ctx)
				if err != nil {
					t.Fatal(err)
				}
				wanted := errors.New("abort")
				err = db.Transaction(ctx, func(tx *orm.Tx) error {
					if _, err := tx.Model[Member]().Create(ctx, &Member{Email: "tx@example.com"}); err != nil {
						return err
					}
					return wanted
				})
				if !errors.Is(err, wanted) {
					t.Fatalf("expected the error to propagate, got %v", err)
				}
				after, err := db.Model[Member]().Count(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if after != before {
					t.Fatalf("expected a rollback: %d rows before, %d after", before, after)
				}
			})

			t.Run("AggregatesAndPluck", func(t *testing.T) {
				emails, err := db.Model[Member]().Where(orm.Like("email", "ph%")).Pluck[string](ctx, "email")
				if err != nil {
					t.Fatal(err)
				}
				if len(emails) != 6 {
					t.Fatalf("expected 6 emails, got %d", len(emails))
				}
				maximum, err := db.Model[Member]().Where(orm.Like("email", "ph%")).Max[int64](ctx, "age")
				if err != nil {
					t.Fatal(err)
				}
				if maximum != 25 {
					t.Fatalf("unexpected max age: %d", maximum)
				}
				// An aggregate over no rows is NULL and must read back as zero.
				empty, err := db.Model[Member]().Where(orm.Eq("email", "nobody")).Sum[int64](ctx, "age")
				if err != nil {
					t.Fatal(err)
				}
				if empty != 0 {
					t.Fatalf("expected 0 from an empty aggregate, got %d", empty)
				}
			})

			t.Run("Pagination", func(t *testing.T) {
				page, err := db.Model[Member]().Where(orm.Like("email", "ph%")).OrderBy(orm.Asc("id")).Paginate(ctx, 2, 4)
				if err != nil {
					t.Fatal(err)
				}
				if page.Total != 6 || page.TotalPages != 2 || len(page.Items) != 2 {
					t.Fatalf("unexpected page: %#v", page)
				}
			})

			// Upsert is spelled differently by every dialect: ON CONFLICT DO
			// UPDATE on Postgres and SQLite, ON DUPLICATE KEY UPDATE on
			// MySQL. Only a real server proves both.
			t.Run("Upsert", func(t *testing.T) {
				age := 30
				first := &Member{Email: "upsert@example.com", Age: &age}
				if _, err := db.Model[Member]().Upsert(ctx, first, []string{"email"}, "age"); err != nil {
					t.Fatal(err)
				}

				older := 31
				second := &Member{Email: "upsert@example.com", Age: &older}
				if _, err := db.Model[Member]().Upsert(ctx, second, []string{"email"}, "age"); err != nil {
					t.Fatal(err)
				}

				rows, err := db.Model[Member]().Where(orm.Eq("email", "upsert@example.com")).All(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if len(rows) != 1 {
					t.Fatalf("an upsert must not duplicate the row, found %d", len(rows))
				}
				if rows[0].Age == nil || *rows[0].Age != 31 {
					t.Fatalf("expected the row to be updated: %#v", rows[0].Age)
				}

				// InsertIgnore must leave it as it is.
				untouched := 99
				if _, err := db.Model[Member]().InsertIgnore(ctx,
					&Member{Email: "upsert@example.com", Age: &untouched}, "email"); err != nil {
					t.Fatal(err)
				}
				after, err := db.Model[Member]().Where(orm.Eq("email", "upsert@example.com")).First(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if after.Age == nil || *after.Age != 31 {
					t.Fatalf("InsertIgnore must not modify the row: %#v", after.Age)
				}
			})

			t.Run("Savepoints", func(t *testing.T) {
				before, err := db.Model[Member]().Count(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if err := db.Transaction(ctx, func(tx *orm.Tx) error {
					if _, err := tx.Model[Member]().Create(ctx, &Member{Email: "sp-keep@example.com"}); err != nil {
						return err
					}
					if err := tx.Savepoint(ctx, "sp1"); err != nil {
						return err
					}
					if _, err := tx.Model[Member]().Create(ctx, &Member{Email: "sp-undo@example.com"}); err != nil {
						return err
					}
					return tx.RollbackTo(ctx, "sp1")
				}); err != nil {
					t.Fatal(err)
				}
				after, err := db.Model[Member]().Count(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if after != before+1 {
					t.Fatalf("expected exactly one row to survive the savepoint rollback: %d -> %d", before, after)
				}
			})

			// Every statement the ORM runs must reach an installed logger.
			t.Run("QueryLogging", func(t *testing.T) {
				var logged int
				logged = 0
				instrumented := orm.Open(db.SQLDB(), db.Dialect(),
					orm.WithLogger(func(_ context.Context, event orm.QueryEvent) {
						if event.Err == nil {
							logged++
						}
					}))
				if _, err := instrumented.Model[Member]().Limit(1).All(ctx); err != nil {
					t.Fatal(err)
				}
				if logged == 0 {
					t.Fatal("expected the statement to reach the logger")
				}
			})

			// The GPA adapter over a real server, including its error mapping.
			t.Run("GPAAdapter", func(t *testing.T) {
				repo := gpaorm.GetRepository[Member](gpaorm.New(db))
				if err := repo.Create(ctx, &Member{Email: "gpa@example.com"}); err != nil {
					t.Fatal(err)
				}
				err := repo.Create(ctx, &Member{Email: "gpa@example.com"})
				if !gpa.IsDuplicate(err) {
					t.Fatalf("expected a GPA duplicate error, got %v", err)
				}
				found, err := repo.QueryOne(ctx, gpa.Where("email", gpa.OpEqual, "gpa@example.com"))
				if err != nil {
					t.Fatal(err)
				}
				if found.Email != "gpa@example.com" {
					t.Fatalf("unexpected row: %#v", found)
				}
			})
		})
	}
}

// The migration module's schema builder must produce DDL these servers accept,
// and the ORM must resolve the tables it creates.
func TestIntegrationMigrationSchemaBuilder(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			dsn := os.Getenv(b.envVar)
			if dsn == "" {
				t.Skipf("set %s to run the %s integration tests", b.envVar, b.name)
			}
			db, err := Connect(gpa.Config{Driver: b.name, ConnectionURL: dsn, MaxOpenConns: 2})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			ctx := context.Background()

			// The schema builder picks its dialect from DB_DRIVER.
			t.Setenv("DB_DRIVER", b.name)

			if _, err := db.Exec(ctx, `DROP TABLE IF EXISTS categories`); err != nil {
				t.Fatal(err)
			}
			schema := migration.Create("categories", func(table *migration.Table) {
				table.Increments("id")
				table.String("name", 255)
			}).Build()

			if _, err := db.Exec(ctx, schema); err != nil {
				t.Fatalf("the migration builder produced DDL %s rejected: %v\nSQL: %s", b.name, err, schema)
			}

			info, err := orm.ModelInfoFor[Category]()
			if err != nil {
				t.Fatal(err)
			}
			if info.Table != "categories" {
				t.Fatalf("the ORM resolved %q but the migration created \"categories\"", info.Table)
			}

			category := &Category{Name: "hardware"}
			if _, err := db.Model[Category]().Create(ctx, category); err != nil {
				t.Fatal(err)
			}
			if category.ID == 0 {
				t.Fatal("the generated key was not written back")
			}
		})
	}
}

// ph renders a positional placeholder for the dialect under test.
func ph(db *orm.DB, n int) string { return db.Dialect().Placeholder(n) }

var _ = strings.TrimSpace

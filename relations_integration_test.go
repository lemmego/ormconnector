package ormconnector

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/lemmego/gpa"
	"github.com/lemmego/migration"
	"github.com/lemmego/orm"
)

// The association matrix had only ever run on SQLite. hasOne, belongsTo,
// many-to-many, nested eager loading and the association writes are all
// dialect-sensitive — placeholders, quoting and the pivot IN clause — so they
// are exercised against every backend here.

type RelAuthor struct {
	ID        uint64 `orm:"column:id;primaryKey;autoIncrement"`
	Name      string `orm:"column:name"`
	CreatedAt time.Time
	UpdatedAt time.Time

	Profile  *RelProfile  `orm:"hasOne:AuthorID"`
	Articles []RelArticle `orm:"hasMany:AuthorID"`
}

type RelProfile struct {
	ID       uint64 `orm:"column:id;primaryKey;autoIncrement"`
	AuthorID uint64 `orm:"column:author_id"`
	Bio      string `orm:"column:bio"`
}

type RelArticle struct {
	ID        uint64 `orm:"column:id;primaryKey;autoIncrement"`
	AuthorID  uint64 `orm:"column:author_id"`
	Title     string `orm:"column:title"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time

	Author   *RelAuthor   `orm:"belongsTo:AuthorID"`
	Comments []RelComment `orm:"hasMany:ArticleID"`
	Labels   []RelLabel   `orm:"many2many:rel_article_labels"`
}

type RelComment struct {
	ID        uint64 `orm:"column:id;primaryKey;autoIncrement"`
	ArticleID uint64 `orm:"column:article_id"`
	Body      string `orm:"column:body"`
}

type RelLabel struct {
	ID   uint64 `orm:"column:id;primaryKey;autoIncrement"`
	Name string `orm:"column:name"`

	Articles []RelArticle `orm:"many2many:rel_article_labels"`
}

func relationSchema(t *testing.T, db *orm.DB, driver string) {
	t.Helper()
	ctx := context.Background()
	t.Setenv("DB_DRIVER", driver)

	for _, table := range []string{"rel_article_labels", "rel_comments", "rel_articles", "rel_profiles", "rel_labels", "rel_authors"} {
		if _, err := db.Exec(ctx, "DROP TABLE IF EXISTS "+table); err != nil {
			t.Fatal(err)
		}
	}

	// Built with the migration module, as a scaffolded project would.
	schemas := []string{
		migration.Create("rel_authors", func(tb *migration.Table) {
			tb.BigIncrements("id").Primary()
			tb.String("name", 255)
			tb.DateTime("created_at", 6).Nullable()
			tb.DateTime("updated_at", 6).Nullable()
		}).Build(),
		migration.Create("rel_profiles", func(tb *migration.Table) {
			tb.BigIncrements("id").Primary()
			tb.UnsignedBigInt("author_id")
			tb.String("bio", 255).Nullable()
		}).Build(),
		migration.Create("rel_labels", func(tb *migration.Table) {
			tb.BigIncrements("id").Primary()
			tb.String("name", 255)
		}).Build(),
		migration.Create("rel_articles", func(tb *migration.Table) {
			tb.BigIncrements("id").Primary()
			tb.UnsignedBigInt("author_id")
			tb.String("title", 255)
			tb.DateTime("created_at", 6).Nullable()
			tb.DateTime("updated_at", 6).Nullable()
			tb.DateTime("deleted_at", 6).Nullable()
			tb.Index("author_id")
		}).Build(),
		migration.Create("rel_comments", func(tb *migration.Table) {
			tb.BigIncrements("id").Primary()
			tb.UnsignedBigInt("article_id")
			tb.String("body", 255).Nullable()
		}).Build(),
		migration.Create("rel_article_labels", func(tb *migration.Table) {
			tb.UnsignedBigInt("rel_article_id")
			tb.UnsignedBigInt("rel_label_id")
			tb.UniqueKey("rel_article_id", "rel_label_id")
		}).Build(),
	}
	for _, schema := range schemas {
		if _, err := db.Exec(ctx, schema); err != nil {
			t.Fatalf("%v\nSQL: %s", err, schema)
		}
	}
}

func TestIntegrationAssociations(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			dsn := os.Getenv(b.envVar)
			if dsn == "" {
				t.Skipf("set %s", b.envVar)
			}
			db, err := Connect(gpa.Config{Driver: b.name, ConnectionURL: dsn, MaxOpenConns: 4})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			relationSchema(t, db, b.name)
			ctx := context.Background()

			// --- seed ---------------------------------------------------
			author := &RelAuthor{Name: "Ada"}
			if _, err := db.Model[RelAuthor]().Create(ctx, author); err != nil {
				t.Fatalf("author: %v", err)
			}
			if _, err := db.Model[RelProfile]().Create(ctx, &RelProfile{AuthorID: author.ID, Bio: "mathematician"}); err != nil {
				t.Fatalf("profile: %v", err)
			}

			labels := map[string]*RelLabel{}
			for _, name := range []string{"go", "sql", "orm"} {
				label := &RelLabel{Name: name}
				if _, err := db.Model[RelLabel]().Create(ctx, label); err != nil {
					t.Fatalf("label: %v", err)
				}
				labels[name] = label
			}

			var articles []*RelArticle
			for i := range 3 {
				article := &RelArticle{AuthorID: author.ID, Title: "Article " + string(rune('A'+i))}
				if _, err := db.Model[RelArticle]().Create(ctx, article); err != nil {
					t.Fatalf("article: %v", err)
				}
				for j := range 2 {
					if _, err := db.Model[RelComment]().Create(ctx, &RelComment{ArticleID: article.ID, Body: "c"}); err != nil {
						t.Fatalf("comment: %v", err)
					}
					_ = j
				}
				articles = append(articles, article)
			}

			// --- many-to-many writes ------------------------------------
			t.Run("Attach", func(t *testing.T) {
				rel := db.Relation[RelArticle, RelLabel](articles[0], "Labels")
				if err := rel.Attach(ctx, labels["go"], labels["sql"]); err != nil {
					t.Fatal(err)
				}
				// Attaching again must not duplicate.
				if err := rel.Attach(ctx, labels["go"]); err != nil {
					t.Fatal(err)
				}
				count, err := db.Relation[RelArticle, RelLabel](articles[0], "Labels").Count(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if count != 2 {
					t.Fatalf("expected 2 labels, got %d", count)
				}
			})

			t.Run("SyncAndToggle", func(t *testing.T) {
				rel := func() *orm.RelationHandle[RelArticle, RelLabel] {
					return db.Relation[RelArticle, RelLabel](articles[0], "Labels")
				}
				if err := rel().Sync(ctx, labels["orm"]); err != nil {
					t.Fatal(err)
				}
				names, err := rel().Query().OrderBy(orm.Asc("name")).Pluck[string](ctx, "name")
				if err != nil {
					t.Fatal(err)
				}
				if len(names) != 1 || names[0] != "orm" {
					t.Fatalf("Sync should leave exactly one label: %#v", names)
				}
				if err := rel().Toggle(ctx, labels["orm"], labels["go"]); err != nil {
					t.Fatal(err)
				}
				names, _ = rel().Query().OrderBy(orm.Asc("name")).Pluck[string](ctx, "name")
				if len(names) != 1 || names[0] != "go" {
					t.Fatalf("Toggle should swap membership: %#v", names)
				}
			})

			// --- eager loading ------------------------------------------
			t.Run("BelongsToAndHasMany", func(t *testing.T) {
				loaded, err := db.Model[RelArticle]().With("Author", "Comments").OrderBy(orm.Asc("id")).All(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if len(loaded) != 3 {
					t.Fatalf("expected 3 articles, got %d", len(loaded))
				}
				for _, article := range loaded {
					if article.Author == nil || article.Author.Name != "Ada" {
						t.Fatalf("belongsTo not loaded: %#v", article.Author)
					}
					if len(article.Comments) != 2 {
						t.Fatalf("hasMany not loaded: %d comments", len(article.Comments))
					}
				}
			})

			t.Run("HasOne", func(t *testing.T) {
				loaded, err := db.Model[RelAuthor]().With("Profile").All(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if len(loaded) != 1 || loaded[0].Profile == nil || loaded[0].Profile.Bio != "mathematician" {
					t.Fatalf("hasOne not loaded: %#v", loaded)
				}
			})

			t.Run("ManyToManyEagerLoad", func(t *testing.T) {
				loaded, err := db.Model[RelArticle]().With("Labels").Where(orm.Eq("id", articles[0].ID)).All(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if len(loaded) != 1 || len(loaded[0].Labels) != 1 || loaded[0].Labels[0].Name != "go" {
					t.Fatalf("many2many not loaded: %#v", loaded)
				}
				// And from the inverse side.
				fromLabel, err := db.Model[RelLabel]().With("Articles").Where(orm.Eq("name", "go")).All(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if len(fromLabel) != 1 || len(fromLabel[0].Articles) != 1 {
					t.Fatalf("inverse many2many not loaded: %#v", fromLabel)
				}
			})

			t.Run("NestedEagerLoad", func(t *testing.T) {
				loaded, err := db.Model[RelAuthor]().With("Articles.Comments", "Profile").All(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if len(loaded) != 1 {
					t.Fatalf("expected 1 author, got %d", len(loaded))
				}
				if len(loaded[0].Articles) != 3 {
					t.Fatalf("expected 3 articles, got %d", len(loaded[0].Articles))
				}
				for _, article := range loaded[0].Articles {
					if len(article.Comments) != 2 {
						t.Fatalf("nested level did not load: article %d has %d comments", article.ID, len(article.Comments))
					}
				}
			})

			// --- soft delete interacting with relations ------------------
			t.Run("SoftDeletedParentHidesFromRelations", func(t *testing.T) {
				if _, err := db.Model[RelArticle]().Delete(ctx, articles[2]); err != nil {
					t.Fatal(err)
				}
				loaded, err := db.Model[RelAuthor]().With("Articles").All(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if len(loaded[0].Articles) != 2 {
					t.Fatalf("a soft-deleted article must not be eager-loaded, got %d", len(loaded[0].Articles))
				}
			})
		})
	}
}

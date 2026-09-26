package ormconnector

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lemmego/api/app"
	"github.com/lemmego/api/config"
	"github.com/lemmego/api/db"
	"github.com/lemmego/api/db/dbtest"
)

// Provide must publish the connection under the seam in both modes. Whether
// the application writes its queries through GPA or through the ORM directly
// says nothing about whether a framework package can find the pool.
//
// Asserted twice because it is easy to get wrong in exactly one mode: the
// gorm and bun connectors register mutually exclusive products in an if/else,
// so a db.Register placed inside either arm would leave half of all projects
// with no resolvable connection and a passing test suite.
func TestProvideRegistersConnection(t *testing.T) {
	for _, useGPA := range []bool{false, true} {
		name := "without GPA"
		if useGPA {
			name = "with GPA"
		}
		t.Run(name, func(t *testing.T) {
			config.Set("sql", config.M{
				"default": "sqlite",
				"connections": config.M{
					"sqlite": config.M{
						"driver":   "sqlite",
						"database": filepath.Join(t.TempDir(), "seam.sqlite"),
					},
				},
			})

			a := app.Configure()
			p := &Provider{UseGPA: useGPA}
			if err := p.Provide(a); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

			conn, ok := db.Resolve(a)
			if !ok {
				t.Fatal("Provide registered no db.Connection")
			}
			dbtest.AssertConnection(t, conn)

			if conn.Dialect() != db.SQLite {
				t.Errorf("Dialect = %q, want %q", conn.Dialect(), db.SQLite)
			}
			if conn.Name() != "sqlite" {
				t.Errorf("Name = %q, want %q", conn.Name(), "sqlite")
			}
			if conn.SQLDB() != p.SQLDB() {
				t.Error("the registered connection is not the connector's own pool")
			}
		})
	}
}

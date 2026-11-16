package ormconnector

import (
	"context"
	"testing"

	"github.com/lemmego/gpa"
	"github.com/lemmego/orm"
)

// The provider must pass an application's orm options to the handle it
// builds, or a multi-tenant application cannot use this provider at all:
// orm.WithTenantEnter is how the database is told which tenant a transaction
// is for, and it can only be installed at Open.
func TestConnectForwardsOrmOptions(t *testing.T) {
	called := false
	settings := gpa.Config{Driver: "sqlite", Database: ":memory:"}

	db, err := Connect(settings, orm.WithTenantEnter(
		func(ctx context.Context, tx *orm.Tx, value any) error {
			called = true
			return nil
		}))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { db.SQLDB().Close() })

	// Opening a tenant transaction is what runs the hook, so that is the
	// observation — asserting on a private field would pass against a
	// provider that stored the option and never called it.
	if err := db.TenantTx(context.Background(), "acme", func(tx *orm.Tx) error {
		return nil
	}); err != nil {
		t.Fatalf("tenant transaction: %v", err)
	}
	if !called {
		t.Fatal("the TenantEnter hook was not installed, so the option was dropped")
	}
}

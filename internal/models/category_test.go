package models_test

import (
	"testing"

	"aura-saku/internal/config"
	"aura-saku/internal/db"
	"aura-saku/internal/models"
)

// TestCategory_NameMustBeLowercase is an integration test against a real
// Postgres instance (see docker-compose.yml), skipped automatically if
// unreachable. It verifies the chk_categories_name_lowercase CHECK
// constraint (internal/models/category.go) at the DB level — inserting
// directly via GORM, bypassing transaction.resolveCategory's own
// lowercasing entirely, so this covers any future insert path (a raw-SQL
// seed, a different service) that might forget to lowercase, not just the
// one code path that currently remembers to.
func TestCategory_NameMustBeLowercase(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Skipf("skipping integration test, config load failed: %v", err)
	}
	gormDB, err := db.NewPostgres(cfg)
	if err != nil {
		t.Skipf("skipping integration test, postgres not reachable: %v", err)
	}
	if err := db.RunMigrations(gormDB); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	err = gormDB.Create(&models.Category{Name: "NotLowercase", Bucket: models.BucketNone}).Error
	if err == nil {
		t.Fatal("expected the CHECK constraint to reject a non-lowercase category name, got no error")
	}

	// Cleanup in case the constraint didn't fire and the insert actually
	// landed (so the test failure above doesn't also leak dirty data).
	gormDB.Unscoped().Where("name = ?", "NotLowercase").Delete(&models.Category{})
}

# Migrations

Schema is currently managed via GORM `AutoMigrate` (see `internal/db/migrate.go`), run automatically on server boot and available standalone via `make migrate` (`cmd/migrate`).

If the schema outgrows what `AutoMigrate` can safely express (e.g. destructive column changes, data backfills), switch to versioned raw SQL migration files in this directory using a tool like [golang-migrate](https://github.com/golang-migrate/migrate).

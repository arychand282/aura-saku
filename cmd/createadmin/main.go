// Command createadmin bootstraps or updates a dashboard admin account.
// There's no signup UI for admins (only Telegram bot users self-register),
// so this is how the first — and every subsequent — admin gets created.
// Running it again for an existing email updates their name/password/role,
// so it doubles as a password-reset tool.
package main

import (
	"flag"
	"fmt"
	"log"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"aura-saku/internal/config"
	"aura-saku/internal/db"
	"aura-saku/internal/models"
)

func main() {
	email := flag.String("email", "", "admin email (required)")
	name := flag.String("name", "", "admin display name (required)")
	password := flag.String("password", "", "admin password (required)")
	role := flag.String("role", "admin", "admin role: admin or superadmin")
	flag.Parse()

	if *email == "" || *name == "" || *password == "" {
		log.Fatal("usage: createadmin -email=<email> -name=<name> -password=<password> [-role=admin|superadmin]")
	}

	adminRole := models.AdminRole(strings.ToLower(*role))
	if adminRole != models.AdminRoleAdmin && adminRole != models.AdminRoleSuperadmin {
		log.Fatalf("invalid role %q: must be %q or %q", *role, models.AdminRoleAdmin, models.AdminRoleSuperadmin)
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config load: %v", err)
	}

	gormDB, err := db.NewPostgres(cfg)
	if err != nil {
		log.Fatalf("postgres connect: %v", err)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(*password), bcrypt.DefaultCost)
	if err != nil {
		log.Fatalf("hash password: %v", err)
	}

	normalizedEmail := strings.ToLower(strings.TrimSpace(*email))
	var admin models.Admin
	result := gormDB.Where(models.Admin{Email: normalizedEmail}).
		Assign(models.Admin{
			Name:         *name,
			PasswordHash: string(hash),
			Role:         adminRole,
			IsActive:     true,
		}).
		FirstOrCreate(&admin)
	if result.Error != nil {
		log.Fatalf("create/update admin: %v", result.Error)
	}

	fmt.Printf("admin %q (%s) ready with role %q\n", admin.Email, admin.Name, admin.Role)
}

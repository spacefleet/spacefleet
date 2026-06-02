package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/spacefleet/spacefleet/db/migrations"
	"github.com/spacefleet/spacefleet/lib/config"
	"github.com/spacefleet/spacefleet/lib/db"
	"github.com/spacefleet/spacefleet/lib/migrate"
)

// runMigrate is the entrypoint for `spacefleet migrate <subcommand>`.
// Subcommands: up, status.
func runMigrate(args []string) {
	if len(args) == 0 {
		migrateUsage()
		os.Exit(2)
	}

	// migrate only needs the database; it deliberately does not load the full
	// serve-time config (EXTERNAL_URL, OIDC, …), matching the DB-only env the
	// Helm chart gives the migrate Job.
	dbURL, err := config.LoadDatabaseURL()
	if err != nil {
		log.Fatalf("migrate: %v", err)
	}
	sqlDB, _, err := db.Open(dbURL)
	if err != nil {
		log.Fatalf("migrate: %v", err)
	}
	defer sqlDB.Close()

	m := migrate.New(sqlDB, migrations.FS)
	ctx := context.Background()

	switch args[0] {
	case "up":
		applied, err := m.Up(ctx)
		if err != nil {
			log.Fatalf("migrate up: %v", err)
		}
		if len(applied) == 0 {
			fmt.Println("migrate: already up to date")
			return
		}
		for _, v := range applied {
			fmt.Printf("migrate: applied %s\n", v)
		}
	case "status":
		applied, pending, err := m.Status(ctx)
		if err != nil {
			log.Fatalf("migrate status: %v", err)
		}
		fmt.Printf("applied (%d):\n", len(applied))
		for _, v := range applied {
			fmt.Printf("  %s\n", v)
		}
		fmt.Printf("pending (%d):\n", len(pending))
		for _, v := range pending {
			fmt.Printf("  %s\n", v)
		}
	default:
		migrateUsage()
		os.Exit(2)
	}
}

func migrateUsage() {
	fmt.Fprintln(os.Stderr, "usage: spacefleet migrate <up|status>")
}

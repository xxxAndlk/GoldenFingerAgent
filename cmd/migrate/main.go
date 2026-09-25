// Command migrate applies pending SQL migrations (thin wrapper over store.Migrate).
package main

import (
	"context"
	"fmt"
	"os"

	"goldenfinger/agent/internal/config"
	"goldenfinger/agent/internal/store"
)

func main() {
	cfg, err := config.Load("")
	if err != nil {
		fatal(err)
	}
	ctx := context.Background()
	db, err := store.Connect(ctx, cfg.Database.URL)
	if err != nil {
		fatal(err)
	}
	defer db.Close()

	applied, err := db.Migrate(ctx)
	if err != nil {
		fatal(err)
	}
	if len(applied) == 0 {
		fmt.Println("migrate: database is up to date")
		return
	}
	for _, name := range applied {
		fmt.Println("migrate: applied", name)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "migrate:", err)
	os.Exit(1)
}

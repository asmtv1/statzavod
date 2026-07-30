package main

import (
	"context"
	"log"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func main() {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		log.Fatal("DATABASE_URL is required")
	}
	if err := goose.SetDialect("postgres"); err != nil {
		log.Fatal(err)
	}
	db, err := goose.OpenDBWithDriver("pgx", url)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	if err := goose.UpContext(context.Background(), db, "db/migrations"); err != nil {
		log.Fatal(err)
	}
}

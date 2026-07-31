package main

import (
	"context"
	"github.com/statzavod/statzavod/internal/config"
	"github.com/statzavod/statzavod/internal/database"
	httpserver "github.com/statzavod/statzavod/internal/transport/http"
	"log"
	"time"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	pool, err := database.Open(context.Background(), cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	server := httpserver.New(pool, cfg)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		// Instagram imports enrich every publication with a separate Insights
		// request. Accounts with dozens of publications can legitimately take
		// longer than one minute, especially when collaborative media is included.
		runCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		processed, syncErr := server.RunPlatformSync(runCtx, 10)
		cancel()
		if syncErr != nil {
			log.Printf("platform sync (%d processed): %v", processed, syncErr)
		}
		<-ticker.C
	}
}

package main

import (
	"context"
	"fmt"
	"github.com/statzavod/statzavod/internal/config"
	"github.com/statzavod/statzavod/internal/database"
	httpserver "github.com/statzavod/statzavod/internal/transport/http"
	"log"
	"os"
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
	workerID := makeWorkerID()
	for {
		mediaCtx, cancelMedia := context.WithTimeout(context.Background(), 10*time.Minute)
		validated, mediaErr := server.RunMediaValidation(mediaCtx, workerID, 1)
		cancelMedia()
		if mediaErr != nil {
			log.Printf("media validation (%d processed): %v", validated, mediaErr)
		}
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 2*time.Minute)
		cleaned, cleanupErr := server.RunMediaCleanup(cleanupCtx, workerID, 20)
		cancelCleanup()
		if cleanupErr != nil {
			log.Printf("media cleanup (%d processed): %v", cleaned, cleanupErr)
		}

		publishCtx, cancelPublish := context.WithTimeout(context.Background(), 5*time.Minute)
		published, publishErr := server.RunContentPublish(publishCtx, workerID, 10)
		cancelPublish()
		if publishErr != nil {
			log.Printf("content publish (%d processed): %v", published, publishErr)
		}
		if _, err := server.ExpireContentReauth(context.Background()); err != nil {
			log.Printf("content reauth expiry: %v", err)
		}

		lifecycleCtx, cancelLifecycle := context.WithTimeout(context.Background(), 2*time.Minute)
		purged, lifecycleErr := server.RunLifecycle(lifecycleCtx, workerID, 20)
		cancelLifecycle()
		if lifecycleErr != nil {
			log.Printf("lifecycle purge (%d processed): %v", purged, lifecycleErr)
		}

		refreshCtx, cancelRefresh := context.WithTimeout(context.Background(), 2*time.Minute)
		refreshed, refreshErr := server.RunOAuthTokenRefresh(refreshCtx, 50)
		cancelRefresh()
		if refreshErr != nil {
			log.Printf("OAuth token refresh (%d processed): %v", refreshed, refreshErr)
		}

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

func makeWorkerID() string {
	return fmt.Sprintf("worker-%d-%d", os.Getpid(), time.Now().UnixNano())
}

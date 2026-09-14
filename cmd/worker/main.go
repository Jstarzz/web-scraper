package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/Jstarzz/web-scraper/internal/browser"
	"github.com/Jstarzz/web-scraper/internal/config"
	"github.com/Jstarzz/web-scraper/internal/store"
	"github.com/jackc/pgx/v5"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		log.Error("config", "error", err)
		os.Exit(1)
	}
	root, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	st, err := store.Open(root, cfg.DatabaseURL)
	if err != nil {
		log.Error("database", "error", err)
		os.Exit(1)
	}
	defer st.Close()
	if err := st.Migrate(root); err != nil {
		log.Error("migration", "error", err)
		os.Exit(1)
	}

	client := browser.New(cfg.BrowserWorkerURL, cfg.BrowserRequestTimeout)
	capabilities := []string{"amazon", "aliexpress", "ebay", "browser"}
	go maintenance(root, st, cfg.RetentionDays, cfg.WorkerID, capabilities, log)

	var wg sync.WaitGroup
	for i := 0; i < cfg.WorkerConcurrency; i++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			workerLoop(root, st, client, cfg, fmt.Sprintf("%s/%d", cfg.WorkerID, slot+1), log)
		}(i)
	}
	log.Info("worker pool started", "worker", cfg.WorkerID, "concurrency", cfg.WorkerConcurrency, "max_attempts", cfg.WorkerMaxAttempts)
	<-root.Done()
	wg.Wait()
}

func workerLoop(root context.Context, st *store.Store, client *browser.Client, cfg config.Config, claimID string, log *slog.Logger) {
	for root.Err() == nil {
		job, err := st.ClaimJob(root, claimID)
		if errors.Is(err, pgx.ErrNoRows) {
			sleep(root, cfg.WorkerPollInterval)
			continue
		}
		if err != nil {
			log.Error("claim job", "worker", claimID, "error", err)
			sleep(root, time.Second)
			continue
		}

		log.Info("scrape start", "worker", claimID, "job", job.ID, "marketplace", job.Marketplace, "query", job.Query)
		var listingsErr error
		for attempt := 1; attempt <= cfg.WorkerMaxAttempts; attempt++ {
			ctx, cancel := context.WithTimeout(root, cfg.BrowserRequestTimeout)
			listings, listErr := client.Scrape(ctx, job)
			cancel()

			if listErr == nil {
				listingsErr = st.CompleteJob(root, job, listings)
				if listingsErr == nil {
					log.Info("scrape complete", "worker", claimID, "job", job.ID, "listings", len(listings), "attempt", attempt)
				}
				break
			}

			listingsErr = listErr
			if attempt < cfg.WorkerMaxAttempts {
				backoff := retryDelay(cfg.WorkerRetryBase, attempt)
				log.Warn("scrape attempt failed; retrying", "worker", claimID, "job", job.ID, "marketplace", job.Marketplace, "attempt", attempt, "retry_in", backoff, "error", listErr)
				sleep(root, backoff)
			}
		}
		if listingsErr != nil {
			log.Warn("scrape failed", "worker", claimID, "job", job.ID, "error", listingsErr)
			_ = st.FailJob(root, job.ID, listingsErr.Error())
		}
	}
}

func retryDelay(base time.Duration, attempt int) time.Duration {
	shift := attempt - 1
	if shift > 4 {
		shift = 4
	}
	backoff := base * time.Duration(1<<shift)
	if backoff > 15*time.Second {
		backoff = 15 * time.Second
	}
	return backoff + time.Duration(rand.Intn(500))*time.Millisecond
}

func maintenance(ctx context.Context, st *store.Store, retention int, workerID string, capabilities []string, log *slog.Logger) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	_ = st.TouchWorker(ctx, workerID, capabilities)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = st.TouchWorker(ctx, workerID, capabilities)
			if n, err := st.RequeueStaleJobs(ctx, 2*time.Minute); err == nil && n > 0 {
				log.Warn("requeued stale jobs", "count", n)
			}
			if n, err := st.PruneHistory(ctx, retention); err == nil && n > 0 {
				log.Info("pruned observations", "count", n)
			}
		}
	}
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

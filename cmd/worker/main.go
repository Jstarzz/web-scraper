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
	go maintenance(root, st, cfg, capabilities, log)

	var wg sync.WaitGroup
	for i := 0; i < cfg.WorkerConcurrency; i++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			workerLoop(root, st, client, cfg, fmt.Sprintf("%s/%d", cfg.WorkerID, slot+1), log)
		}(i)
	}
	log.Info(
		"worker pool started",
		"worker", cfg.WorkerID,
		"concurrency", cfg.WorkerConcurrency,
		"max_attempts", cfg.WorkerMaxAttempts,
		"heartbeat_interval", cfg.WorkerHeartbeatInterval,
		"stale_after", cfg.WorkerStaleAfter,
	)
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

		jobCtx, cancelJob := context.WithCancel(root)
		claimLost := make(chan error, 1)
		go maintainJobClaim(jobCtx, cancelJob, st, job.ID, claimID, cfg.WorkerHeartbeatInterval, claimLost, log)

		var listingsErr error
	attemptLoop:
		for attempt := 1; attempt <= cfg.WorkerMaxAttempts; attempt++ {
			ctx, cancel := context.WithTimeout(jobCtx, cfg.BrowserRequestTimeout)
			listings, listErr := client.Scrape(ctx, job)
			cancel()

			select {
			case claimErr := <-claimLost:
				listingsErr = claimErr
				break attemptLoop
			default:
			}

			if listErr == nil {
				listingsErr = st.CompleteJob(jobCtx, job, listings)
				if listingsErr == nil {
					log.Info("scrape complete", "worker", claimID, "job", job.ID, "listings", len(listings), "attempt", attempt)
				}
				break
			}

			listingsErr = listErr
			if root.Err() != nil {
				break
			}
			if attempt < cfg.WorkerMaxAttempts {
				backoff := retryDelay(cfg.WorkerRetryBase, attempt)
				log.Warn(
					"scrape attempt failed; retrying",
					"worker", claimID,
					"job", job.ID,
					"marketplace", job.Marketplace,
					"attempt", attempt,
					"retry_in", backoff,
					"error", listErr,
				)
				sleep(jobCtx, backoff)
			}
		}
		cancelJob()

		if listingsErr == nil {
			continue
		}
		if errors.Is(listingsErr, store.ErrJobClaimLost) {
			log.Warn("job claim lost; discarding stale result", "worker", claimID, "job", job.ID)
			continue
		}
		if root.Err() != nil {
			return
		}

		log.Warn("scrape failed", "worker", claimID, "job", job.ID, "error", listingsErr)
		if err := st.FailJob(root, job.ID, claimID, listingsErr.Error()); err != nil && !errors.Is(err, store.ErrJobClaimLost) {
			log.Error("mark job failed", "worker", claimID, "job", job.ID, "error", err)
		}
	}
}

func maintainJobClaim(
	ctx context.Context,
	cancelJob context.CancelFunc,
	st *store.Store,
	jobID string,
	workerID string,
	interval time.Duration,
	claimLost chan<- error,
	log *slog.Logger,
) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := st.RefreshJobClaim(ctx, jobID, workerID)
			if errors.Is(err, store.ErrJobClaimLost) {
				select {
				case claimLost <- err:
				default:
				}
				cancelJob()
				return
			}
			if err != nil && ctx.Err() == nil {
				log.Warn("refresh job claim", "worker", workerID, "job", jobID, "error", err)
			}
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

func maintenance(ctx context.Context, st *store.Store, cfg config.Config, capabilities []string, log *slog.Logger) {
	heartbeat := time.NewTicker(cfg.WorkerHeartbeatInterval)
	prune := time.NewTicker(6 * time.Hour)
	defer heartbeat.Stop()
	defer prune.Stop()

	if err := st.TouchWorker(ctx, cfg.WorkerID, capabilities); err != nil {
		log.Warn("worker heartbeat", "worker", cfg.WorkerID, "error", err)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			if err := st.TouchWorker(ctx, cfg.WorkerID, capabilities); err != nil {
				log.Warn("worker heartbeat", "worker", cfg.WorkerID, "error", err)
			}
			if n, err := st.RequeueStaleJobs(ctx, cfg.WorkerStaleAfter); err != nil {
				log.Warn("requeue stale jobs", "error", err)
			} else if n > 0 {
				log.Warn("requeued stale jobs", "count", n, "stale_after", cfg.WorkerStaleAfter)
			}
		case <-prune.C:
			if n, err := st.PruneHistory(ctx, cfg.RetentionDays); err != nil {
				log.Warn("prune observations", "error", err)
			} else if n > 0 {
				log.Info("pruned observations", "count", n)
			}
		}
	}
}

func sleep(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

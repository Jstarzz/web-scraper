package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Jstarzz/web-scraper/internal/ziz"
)

type runSummary struct {
	StartedAt  string                    `json:"started_at"`
	FinishedAt string                    `json:"finished_at"`
	BaseURL    string                    `json:"base_url"`
	Articles   ziz.ExportStats           `json:"articles"`
	Validation ziz.ValidateStats         `json:"validation"`
	Media      *ziz.MediaStats           `json:"media,omitempty"`
}

func main() {
	var (
		baseURL      = flag.String("base", "https://zizonline.com", "ZIZ base URL")
		outputDir    = flag.String("out", "ziz-export", "migration output directory")
		workers      = flag.Int("workers", 16, "parallel WordPress REST page workers")
		mediaWorkers = flag.Int("media-workers", 24, "parallel media download workers")
		timeout      = flag.Duration("timeout", 25*time.Second, "per-request timeout")
		resume       = flag.Bool("resume", true, "skip completed chunks and media")
		withMedia    = flag.Bool("media", true, "download public ZIZ wp-content media")
		maxMedia     = flag.Int64("max-media-bytes", 250<<20, "maximum bytes per media object")
		userAgent    = flag.String("user-agent", "Jstarzz-web-scraper-ziz-migration/1.0", "HTTP User-Agent")
	)
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	started := time.Now()
	if err := os.MkdirAll(*outputDir, 0o755); err != nil {
		fatal(log, "create output directory", err)
	}

	maxConns := *workers
	if *mediaWorkers > maxConns {
		maxConns = *mediaWorkers
	}
	client, err := ziz.NewClient(*baseURL, *timeout, maxConns, *userAgent)
	if err != nil {
		fatal(log, "create client", err)
	}

	log.Info("ziz migration start",
		"base", *baseURL,
		"out", *outputDir,
		"workers", *workers,
		"media_workers", *mediaWorkers,
		"resume", *resume,
		"media", *withMedia,
	)

	articleStats, err := ziz.ExportArticles(ctx, client, ziz.ExportConfig{
		OutputDir: *outputDir,
		Workers:   *workers,
		PerPage:   100,
		Resume:    *resume,
	})
	if err != nil {
		fatal(log, "export articles", err)
	}
	log.Info("article export complete",
		"expected_articles", articleStats.ExpectedArticles,
		"expected_pages", articleStats.ExpectedPages,
		"articles_written", articleStats.ArticlesWritten,
		"pages_written", articleStats.PagesWritten,
		"pages_skipped", articleStats.PagesSkipped,
		"seconds", articleStats.DurationSeconds,
	)

	validation, err := ziz.ValidateArticles(*outputDir)
	if err != nil {
		fatal(log, "validate articles", err)
	}
	log.Info("article validation complete",
		"unique_articles", validation.UniqueArticles,
		"duplicates", validation.Duplicates,
		"empty_bodies", validation.EmptyBodies,
		"empty_titles", validation.EmptyTitles,
		"media_urls", validation.MediaURLs,
	)

	var mediaStats *ziz.MediaStats
	if *withMedia {
		stats, err := ziz.DownloadMedia(ctx, client, ziz.MediaConfig{
			OutputDir: *outputDir,
			Workers:   *mediaWorkers,
			MaxBytes:  *maxMedia,
			Resume:    *resume,
		})
		if err != nil {
			fatal(log, "download media", err)
		}
		mediaStats = &stats
		log.Info("media download complete",
			"discovered", stats.Discovered,
			"downloaded", stats.Downloaded,
			"skipped", stats.Skipped,
			"failed", stats.Failed,
			"bytes", stats.Bytes,
			"seconds", stats.DurationSeconds,
		)
	}

	summary := runSummary{
		StartedAt:  started.UTC().Format(time.RFC3339),
		FinishedAt: time.Now().UTC().Format(time.RFC3339),
		BaseURL:    *baseURL,
		Articles:   articleStats,
		Validation: validation,
		Media:      mediaStats,
	}
	if err := writeJSON(filepath.Join(*outputDir, "summary.json"), summary); err != nil {
		fatal(log, "write summary", err)
	}

	if articleStats.ExpectedArticles > 0 && validation.UniqueArticles < articleStats.ExpectedArticles {
		log.Warn("export count is below WordPress total; rerun with -resume=true to fill missing chunks",
			"wordpress_total", articleStats.ExpectedArticles,
			"unique_articles", validation.UniqueArticles,
		)
	}

	log.Info("ziz migration finished",
		"elapsed", time.Since(started).Round(time.Millisecond).String(),
		"articles", validation.UniqueArticles,
		"output", *outputDir,
	)
}

func writeJSON(path string, value any) error {
	tmp := path + ".tmp"
	file, err := os.Create(tmp)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		file.Close()
		os.Remove(tmp)
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		os.Remove(tmp)
		return err
	}
	if err := file.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func fatal(log *slog.Logger, message string, err error) {
	log.Error(message, "error", err)
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

package ziz

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type ExportConfig struct {
	OutputDir string
	Workers   int
	PerPage   int
	Resume    bool
}

type ExportStats struct {
	ExpectedArticles int           `json:"expected_articles"`
	ExpectedPages    int           `json:"expected_pages"`
	ArticlesWritten  int64         `json:"articles_written"`
	PagesWritten     int64         `json:"pages_written"`
	PagesSkipped     int64         `json:"pages_skipped"`
	Duration         time.Duration `json:"-"`
	DurationSeconds  float64       `json:"duration_seconds"`
}

func ExportArticles(ctx context.Context, client *Client, cfg ExportConfig) (ExportStats, error) {
	started := time.Now()
	if cfg.OutputDir == "" {
		cfg.OutputDir = "ziz-export"
	}
	if cfg.Workers < 1 {
		cfg.Workers = 16
	}
	if cfg.PerPage < 1 || cfg.PerPage > 100 {
		cfg.PerPage = 100
	}

	articlesDir := filepath.Join(cfg.OutputDir, "articles")
	if err := os.MkdirAll(articlesDir, 0o755); err != nil {
		return ExportStats{}, err
	}

	firstPosts, total, totalPages, err := client.FetchPostsPage(ctx, 1, cfg.PerPage)
	if err != nil {
		return ExportStats{}, fmt.Errorf("WordPress REST probe failed: %w", err)
	}
	if totalPages < 1 {
		totalPages = 1
	}

	stats := ExportStats{
		ExpectedArticles: total,
		ExpectedPages:    totalPages,
	}

	firstPath := articlePartPath(articlesDir, 1)
	if cfg.Resume {
		if _, err := os.Stat(firstPath); err == nil {
			stats.PagesSkipped++
		} else if err := writeArticlePart(firstPath, client.BaseURL(), firstPosts); err != nil {
			return stats, err
		} else {
			stats.PagesWritten++
			stats.ArticlesWritten += int64(len(firstPosts))
		}
	} else if err := writeArticlePart(firstPath, client.BaseURL(), firstPosts); err != nil {
		return stats, err
	} else {
		stats.PagesWritten++
		stats.ArticlesWritten += int64(len(firstPosts))
	}

	if totalPages == 1 {
		stats.Duration = time.Since(started)
		stats.DurationSeconds = stats.Duration.Seconds()
		return stats, nil
	}

	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	pages := make(chan int)
	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	var articlesWritten atomic.Int64
	var pagesWritten atomic.Int64
	var pagesSkipped atomic.Int64

	worker := func() {
		defer wg.Done()
		for page := range pages {
			if workCtx.Err() != nil {
				return
			}

			path := articlePartPath(articlesDir, page)
			if cfg.Resume {
				if _, err := os.Stat(path); err == nil {
					pagesSkipped.Add(1)
					continue
				}
			}

			posts, _, _, err := client.FetchPostsPage(workCtx, page, cfg.PerPage)
			if err != nil {
				select {
				case errCh <- fmt.Errorf("fetch page %d: %w", page, err):
					cancel()
				default:
				}
				return
			}
			if err := writeArticlePart(path, client.BaseURL(), posts); err != nil {
				select {
				case errCh <- fmt.Errorf("write page %d: %w", page, err):
					cancel()
				default:
				}
				return
			}
			pagesWritten.Add(1)
			articlesWritten.Add(int64(len(posts)))
		}
	}

	wg.Add(cfg.Workers)
	for i := 0; i < cfg.Workers; i++ {
		go worker()
	}

	go func() {
		defer close(pages)
		for page := 2; page <= totalPages; page++ {
			select {
			case <-workCtx.Done():
				return
			case pages <- page:
			}
		}
	}()

	wg.Wait()
	select {
	case err := <-errCh:
		return stats, err
	default:
	}

	stats.ArticlesWritten += articlesWritten.Load()
	stats.PagesWritten += pagesWritten.Load()
	stats.PagesSkipped += pagesSkipped.Load()
	stats.Duration = time.Since(started)
	stats.DurationSeconds = stats.Duration.Seconds()
	return stats, nil
}

func articlePartPath(dir string, page int) string {
	return filepath.Join(dir, fmt.Sprintf("part-%05d.jsonl.gz", page))
}

func writeArticlePart(path string, baseURL interface{ String() string }, posts []wpPost) error {
	tmp := path + ".tmp"
	file, err := os.Create(tmp)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		file.Close()
		if !ok {
			os.Remove(tmp)
		}
	}()

	gz, err := gzip.NewWriterLevel(file, gzip.BestSpeed)
	if err != nil {
		return err
	}
	buffer := bufio.NewWriterSize(gz, 256<<10)
	encoder := json.NewEncoder(buffer)
	scrapedAt := time.Now()

	base, err := parseBaseURL(baseURL.String())
	if err != nil {
		return err
	}
	for _, post := range posts {
		article := articleFromWP(base, post, scrapedAt)
		if err := encoder.Encode(article); err != nil {
			return err
		}
	}
	if err := buffer.Flush(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}

func parseBaseURL(raw string) (*url.URL, error) {
	return url.Parse(raw)
}

func ReadArticles(outputDir string, fn func(Article) error) error {
	files, err := filepath.Glob(filepath.Join(outputDir, "articles", "part-*.jsonl.gz"))
	if err != nil {
		return err
	}
	sort.Strings(files)
	if len(files) == 0 {
		return fmt.Errorf("no article parts found in %s", filepath.Join(outputDir, "articles"))
	}

	for _, path := range files {
		if err := readArticlePart(path, fn); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	return nil
}

func readArticlePart(path string, fn func(Article) error) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()

	decoder := json.NewDecoder(bufio.NewReaderSize(gz, 256<<10))
	for {
		var article Article
		if err := decoder.Decode(&article); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if err := fn(article); err != nil {
			return err
		}
	}
}

type ValidateStats struct {
	UniqueArticles int      `json:"unique_articles"`
	Duplicates     int      `json:"duplicate_articles"`
	EmptyBodies    int      `json:"empty_bodies"`
	EmptyTitles    int      `json:"empty_titles"`
	MediaURLs      int      `json:"unique_media_urls"`
	DuplicateIDs   []int    `json:"duplicate_ids,omitempty"`
}

func ValidateArticles(outputDir string) (ValidateStats, error) {
	stats := ValidateStats{}
	ids := make(map[int]struct{})
	media := make(map[string]struct{})

	err := ReadArticles(outputDir, func(article Article) error {
		if _, exists := ids[article.SourceID]; exists {
			stats.Duplicates++
			if len(stats.DuplicateIDs) < 100 {
				stats.DuplicateIDs = append(stats.DuplicateIDs, article.SourceID)
			}
		} else {
			ids[article.SourceID] = struct{}{}
		}
		if strings.TrimSpace(article.Title) == "" {
			stats.EmptyTitles++
		}
		if strings.TrimSpace(article.BodyHTML) == "" {
			stats.EmptyBodies++
		}
		for _, rawURL := range article.MediaURLs {
			media[rawURL] = struct{}{}
		}
		return nil
	})
	if err != nil {
		return stats, err
	}
	stats.UniqueArticles = len(ids)
	stats.MediaURLs = len(media)
	sort.Ints(stats.DuplicateIDs)
	return stats, nil
}

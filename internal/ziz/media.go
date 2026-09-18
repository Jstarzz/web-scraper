package ziz

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type MediaConfig struct {
	OutputDir     string
	Workers       int
	MaxBytes      int64
	Resume        bool
	AllowedHosts  []string
}

type MediaRecord struct {
	URL         string `json:"url"`
	LocalPath   string `json:"local_path,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
	Size        int64  `json:"size,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
}

type MediaStats struct {
	Discovered       int           `json:"discovered"`
	Downloaded       int64         `json:"downloaded"`
	Skipped          int64         `json:"skipped"`
	Failed           int64         `json:"failed"`
	Bytes            int64         `json:"bytes"`
	Duration         time.Duration `json:"-"`
	DurationSeconds  float64       `json:"duration_seconds"`
}

func DownloadMedia(ctx context.Context, client *Client, cfg MediaConfig) (MediaStats, error) {
	started := time.Now()
	if cfg.OutputDir == "" {
		cfg.OutputDir = "ziz-export"
	}
	if cfg.Workers < 1 {
		cfg.Workers = 24
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = 250 << 20
	}
	if len(cfg.AllowedHosts) == 0 {
		cfg.AllowedHosts = []string{"zizonline.com", "www.zizonline.com"}
	}

	mediaDir := filepath.Join(cfg.OutputDir, "media")
	filesDir := filepath.Join(mediaDir, "files")
	if err := os.MkdirAll(filesDir, 0o755); err != nil {
		return MediaStats{}, err
	}

	seenManifest, err := loadMediaManifest(filepath.Join(mediaDir, "manifest.jsonl"))
	if err != nil {
		return MediaStats{}, err
	}

	urlSet := make(map[string]struct{})
	if err := ReadArticles(cfg.OutputDir, func(article Article) error {
		for _, rawURL := range article.MediaURLs {
			if mediaURLAllowed(rawURL, cfg.AllowedHosts) {
				urlSet[rawURL] = struct{}{}
			}
		}
		return nil
	}); err != nil {
		return MediaStats{}, err
	}

	stats := MediaStats{Discovered: len(urlSet)}
	manifest, err := os.OpenFile(filepath.Join(mediaDir, "manifest.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return stats, err
	}
	defer manifest.Close()

	writer := bufio.NewWriterSize(manifest, 256<<10)
	defer writer.Flush()

	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobs := make(chan string)
	results := make(chan MediaRecord, cfg.Workers*2)
	errCh := make(chan error, 1)

	var wg sync.WaitGroup
	var downloaded atomic.Int64
	var skipped atomic.Int64
	var failed atomic.Int64
	var bytesWritten atomic.Int64

	for i := 0; i < cfg.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for rawURL := range jobs {
				if workCtx.Err() != nil {
					return
				}
				if cfg.Resume {
					if _, ok := seenManifest[rawURL]; ok {
						skipped.Add(1)
						continue
					}
				}
				record := downloadOneMedia(workCtx, client, filesDir, rawURL, cfg.MaxBytes)
				switch record.Status {
				case "downloaded":
					downloaded.Add(1)
					bytesWritten.Add(record.Size)
				case "skipped":
					skipped.Add(1)
				default:
					failed.Add(1)
				}
				select {
				case <-workCtx.Done():
					return
				case results <- record:
				}
			}
		}()
	}

	go func() {
		defer close(jobs)
		for rawURL := range urlSet {
			select {
			case <-workCtx.Done():
				return
			case jobs <- rawURL:
			}
		}
	}()

	go func() {
		wg.Wait()
		close(results)
	}()

	encoder := json.NewEncoder(writer)
	pending := 0
	for record := range results {
		if err := encoder.Encode(record); err != nil {
			select {
			case errCh <- err:
			default:
			}
			cancel()
			break
		}
		pending++
		if pending >= 100 {
			if err := writer.Flush(); err != nil {
				select {
				case errCh <- err:
				default:
				}
				cancel()
				break
			}
			pending = 0
		}
	}
	if err := writer.Flush(); err != nil {
		return stats, err
	}
	if err := manifest.Sync(); err != nil {
		return stats, err
	}

	select {
	case err := <-errCh:
		return stats, err
	default:
	}

	stats.Downloaded = downloaded.Load()
	stats.Skipped = skipped.Load()
	stats.Failed = failed.Load()
	stats.Bytes = bytesWritten.Load()
	stats.Duration = time.Since(started)
	stats.DurationSeconds = stats.Duration.Seconds()
	return stats, nil
}

func downloadOneMedia(ctx context.Context, client *Client, filesDir, rawURL string, maxBytes int64) MediaRecord {
	record := MediaRecord{URL: rawURL}
	resp, err := client.Download(ctx, rawURL)
	if err != nil {
		record.Status = "failed"
		record.Error = err.Error()
		return record
	}
	defer resp.Body.Close()

	if resp.ContentLength > maxBytes && resp.ContentLength > 0 {
		record.Status = "failed"
		record.Error = fmt.Sprintf("content length %d exceeds limit %d", resp.ContentLength, maxBytes)
		return record
	}

	tmp, err := os.CreateTemp(filesDir, ".download-*")
	if err != nil {
		record.Status = "failed"
		record.Error = err.Error()
		return record
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	hasher := sha256.New()
	limited := io.LimitReader(resp.Body, maxBytes+1)
	n, err := io.Copy(io.MultiWriter(tmp, hasher), limited)
	if err != nil {
		tmp.Close()
		record.Status = "failed"
		record.Error = err.Error()
		return record
	}
	if n > maxBytes {
		tmp.Close()
		record.Status = "failed"
		record.Error = fmt.Sprintf("download exceeded limit %d", maxBytes)
		return record
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		record.Status = "failed"
		record.Error = err.Error()
		return record
	}
	if err := tmp.Close(); err != nil {
		record.Status = "failed"
		record.Error = err.Error()
		return record
	}

	sum := hex.EncodeToString(hasher.Sum(nil))
	ext := mediaExtension(rawURL, resp.Header.Get("Content-Type"))
	dir := filepath.Join(filesDir, sum[:2])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		record.Status = "failed"
		record.Error = err.Error()
		return record
	}
	finalPath := filepath.Join(dir, sum+ext)
	if _, err := os.Stat(finalPath); err == nil {
		record.Status = "skipped"
	} else if err := os.Rename(tmpPath, finalPath); err != nil {
		record.Status = "failed"
		record.Error = err.Error()
		return record
	} else {
		record.Status = "downloaded"
	}

	rel, _ := filepath.Rel(filepath.Dir(filesDir), finalPath)
	record.LocalPath = filepath.ToSlash(filepath.Join("media", rel))
	record.SHA256 = sum
	record.Size = n
	record.ContentType = strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	return record
}

func mediaExtension(rawURL, contentType string) string {
	if u, err := url.Parse(rawURL); err == nil {
		ext := strings.ToLower(filepath.Ext(u.Path))
		if len(ext) >= 2 && len(ext) <= 8 {
			return ext
		}
	}
	contentType = strings.TrimSpace(strings.Split(contentType, ";")[0])
	if exts, err := mime.ExtensionsByType(contentType); err == nil && len(exts) > 0 {
		return exts[0]
	}
	return ".bin"
}

func mediaURLAllowed(rawURL string, allowedHosts []string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Hostname() == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, allowed := range allowedHosts {
		if host == strings.ToLower(allowed) {
			return strings.Contains(u.Path, "/wp-content/uploads/")
		}
	}
	return false
}

func loadMediaManifest(path string) (map[string]MediaRecord, error) {
	out := make(map[string]MediaRecord)
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var record MediaRecord
		if err := json.Unmarshal(line, &record); err != nil {
			continue
		}
		if record.URL != "" && (record.Status == "downloaded" || record.Status == "skipped") {
			out[record.URL] = record
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func mediaStatusOK(status int) bool {
	return status >= http.StatusOK && status < http.StatusMultipleChoices
}

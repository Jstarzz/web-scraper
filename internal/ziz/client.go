package ziz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	base      *url.URL
	http      *http.Client
	userAgent string
	retries   int
}

func NewClient(baseURL string, timeout time.Duration, maxConns int, userAgent string) (*Client, error) {
	base, err := url.Parse(strings.TrimRight(baseURL, "/") + "/")
	if err != nil {
		return nil, fmt.Errorf("parse base url: %w", err)
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return nil, errors.New("base URL must use http or https")
	}
	if maxConns < 1 {
		maxConns = 16
	}
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	if userAgent == "" {
		userAgent = "Jstarzz-web-scraper-ziz-migration/1.0"
	}

	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          maxConns * 4,
		MaxIdleConnsPerHost:   maxConns * 2,
		MaxConnsPerHost:       maxConns,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	}

	return &Client{
		base: base,
		http: &http.Client{
			Transport: transport,
			Timeout:   timeout,
		},
		userAgent: userAgent,
		retries:   3,
	}, nil
}

func (c *Client) BaseURL() *url.URL {
	copy := *c.base
	return &copy
}

func (c *Client) resolve(path string) string {
	u, err := url.Parse(path)
	if err != nil {
		return c.base.String()
	}
	return c.base.ResolveReference(u).String()
}

func (c *Client) do(ctx context.Context, method, rawURL string) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt <= c.retries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", c.userAgent)
		req.Header.Set("Accept", "application/json,text/html;q=0.9,*/*;q=0.8")

		resp, err := c.http.Do(req)
		if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return resp, nil
		}

		if err == nil {
			if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500 {
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
				resp.Body.Close()
				return nil, fmt.Errorf("GET %s: status %d: %s", rawURL, resp.StatusCode, strings.TrimSpace(string(body)))
			}
			lastErr = fmt.Errorf("GET %s: status %d", rawURL, resp.StatusCode)
			wait := retryAfter(resp.Header.Get("Retry-After"))
			resp.Body.Close()
			if wait > 0 {
				if err := sleepContext(ctx, wait); err != nil {
					return nil, err
				}
				continue
			}
		} else {
			lastErr = err
		}

		if attempt < c.retries {
			backoff := time.Duration(250*(1<<attempt)) * time.Millisecond
			if err := sleepContext(ctx, backoff); err != nil {
				return nil, err
			}
		}
	}
	return nil, lastErr
}

func retryAfter(raw string) time.Duration {
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(raw); err == nil {
		if seconds < 0 {
			return 0
		}
		if seconds > 10 {
			seconds = 10
		}
		return time.Duration(seconds) * time.Second
	}
	if t, err := http.ParseTime(raw); err == nil {
		wait := time.Until(t)
		if wait < 0 {
			return 0
		}
		if wait > 10*time.Second {
			return 10 * time.Second
		}
		return wait
	}
	return 0
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) FetchPostsPage(ctx context.Context, page, perPage int) ([]wpPost, int, int, error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 100 {
		perPage = 100
	}

	u := c.resolve("/wp-json/wp/v2/posts")
	parsed, _ := url.Parse(u)
	q := parsed.Query()
	q.Set("page", strconv.Itoa(page))
	q.Set("per_page", strconv.Itoa(perPage))
	q.Set("orderby", "id")
	q.Set("order", "asc")
	q.Set("_embed", "author,wp:featuredmedia,wp:term")
	q.Set("_fields", "id,date,date_gmt,modified,modified_gmt,slug,link,title,content,excerpt,author,featured_media,categories,tags,_embedded")
	parsed.RawQuery = q.Encode()

	resp, err := c.do(ctx, http.MethodGet, parsed.String())
	if err != nil {
		return nil, 0, 0, err
	}
	defer resp.Body.Close()

	var posts []wpPost
	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(&posts); err != nil {
		return nil, 0, 0, fmt.Errorf("decode posts page %d: %w", page, err)
	}

	total, _ := strconv.Atoi(resp.Header.Get("X-WP-Total"))
	totalPages, _ := strconv.Atoi(resp.Header.Get("X-WP-TotalPages"))
	if totalPages == 0 && len(posts) > 0 {
		totalPages = page
	}
	return posts, total, totalPages, nil
}

func (c *Client) Download(ctx context.Context, rawURL string) (*http.Response, error) {
	return c.do(ctx, http.MethodGet, rawURL)
}

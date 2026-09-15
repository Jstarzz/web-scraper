package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Jstarzz/web-scraper/internal/model"
	"github.com/Jstarzz/web-scraper/internal/store"
	"github.com/jackc/pgx/v5"
)

type contextKey string

const (
	apiKeyContext     contextKey = "api-key"
	defaultWaitMS                = 12_000
	maxSearchQueryLen            = 500
)

type Server struct {
	store      *store.Store
	adminToken string
	limiter    *rateLimiter
	log        *slog.Logger
}

func New(st *store.Store, adminToken string, log *slog.Logger) http.Handler {
	s := &Server{store: st, adminToken: adminToken, limiter: newRateLimiter(), log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /readyz", s.ready)
	mux.Handle("POST /v1/search", s.withAPIKey(http.HandlerFunc(s.search)))
	mux.Handle("GET /v1/jobs/{id}", s.withAPIKey(http.HandlerFunc(s.job)))
	mux.Handle("GET /v1/history/{marketplace}/{externalID}", s.withAPIKey(http.HandlerFunc(s.history)))
	mux.Handle("POST /admin/api-keys", s.withAdmin(http.HandlerFunc(s.createAPIKey)))
	mux.Handle("GET /admin/api-keys", s.withAdmin(http.HandlerFunc(s.listAPIKeys)))
	mux.Handle("DELETE /admin/api-keys/{id}", s.withAdmin(http.HandlerFunc(s.revokeAPIKey)))
	mux.Handle("GET /admin/workers", s.withAdmin(http.HandlerFunc(s.workers)))
	return requestLog(log, mux)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Ping(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	var req model.SearchRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	req.Marketplace = strings.ToLower(strings.TrimSpace(req.Marketplace))
	req.Query = strings.TrimSpace(req.Query)
	if !validMarketplace(req.Marketplace) {
		writeError(w, http.StatusBadRequest, "marketplace must be amazon, aliexpress, or ebay")
		return
	}
	if req.Query == "" {
		writeError(w, http.StatusBadRequest, "query is required")
		return
	}
	if len(req.Query) > maxSearchQueryLen {
		writeError(w, http.StatusBadRequest, "query must be at most 500 characters")
		return
	}
	if req.Limit == 0 {
		req.Limit = 20
	}
	if req.Limit < 1 || req.Limit > 100 {
		writeError(w, http.StatusBadRequest, "limit must be 1..100")
		return
	}

	waitMS := defaultWaitMS
	if req.WaitMS != nil {
		waitMS = *req.WaitMS
	}
	if waitMS < 0 || waitMS > 30_000 {
		writeError(w, http.StatusBadRequest, "wait_ms must be 0..30000")
		return
	}

	compact := wantsCompact(r)
	key := r.Context().Value(apiKeyContext).(store.APIKey)
	job, err := s.store.CreateJob(r.Context(), req, key.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not enqueue job")
		return
	}
	if waitMS == 0 {
		writeJobJSON(w, http.StatusAccepted, job, compact)
		return
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	deadline := time.NewTimer(time.Duration(waitMS) * time.Millisecond)
	defer ticker.Stop()
	defer deadline.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			current, err := s.store.GetJob(r.Context(), job.ID, key.ID)
			if err != nil {
				if r.Context().Err() != nil {
					return
				}
				writeError(w, http.StatusInternalServerError, "could not read queued job")
				return
			}
			if writeTerminalJob(w, current, compact) {
				return
			}
		case <-deadline.C:
			current, err := s.store.GetJob(r.Context(), job.ID, key.ID)
			if err != nil {
				current = job
			}
			if writeTerminalJob(w, current, compact) {
				return
			}
			writeJobJSON(w, http.StatusAccepted, current, compact)
			return
		}
	}
}

func writeTerminalJob(w http.ResponseWriter, job model.Job, compact bool) bool {
	switch job.Status {
	case "complete":
		writeJobJSON(w, http.StatusOK, job, compact)
		return true
	case "failed":
		writeJobJSON(w, http.StatusBadGateway, job, compact)
		return true
	default:
		return false
	}
}

func (s *Server) job(w http.ResponseWriter, r *http.Request) {
	key := r.Context().Value(apiKeyContext).(store.APIKey)
	job, err := s.store.GetJob(r.Context(), r.PathValue("id"), key.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read job")
		return
	}
	writeJobJSON(w, http.StatusOK, job, wantsCompact(r))
}

func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	marketplace := strings.ToLower(strings.TrimSpace(r.PathValue("marketplace")))
	if !validMarketplace(marketplace) {
		writeError(w, http.StatusBadRequest, "marketplace must be amazon, aliexpress, or ebay")
		return
	}
	externalID := strings.TrimSpace(r.PathValue("externalID"))
	if externalID == "" {
		writeError(w, http.StatusBadRequest, "external id is required")
		return
	}

	days := 60
	if raw := r.URL.Query().Get("days"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 365 {
			writeError(w, http.StatusBadRequest, "days must be 1..365")
			return
		}
		days = n
	}
	points, err := s.store.History(r.Context(), marketplace, externalID, days)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read history")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"marketplace": marketplace,
		"external_id": externalID,
		"points":      points,
	})
}

func validMarketplace(marketplace string) bool {
	return marketplace == "amazon" || marketplace == "aliexpress" || marketplace == "ebay"
}

func (s *Server) createAPIKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name               string `json:"name"`
		RateLimitPerMinute int    `json:"rate_limit_per_minute"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	key, err := s.store.CreateAPIKey(r.Context(), strings.TrimSpace(body.Name), body.RateLimitPerMinute)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, key)
}

func (s *Server) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.store.ListAPIKeys(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list API keys")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"api_keys": keys})
}

func (s *Server) revokeAPIKey(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "API key id is required")
		return
	}
	if err := s.store.RevokeAPIKey(r.Context(), id); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "API key not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "could not revoke API key")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": true, "id": id})
}

func (s *Server) workers(w http.ResponseWriter, r *http.Request) {
	workers, err := s.store.ListWorkers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list workers")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workers": workers})
}

func (s *Server) withAPIKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := bearer(r.Header.Get("Authorization"))
		if raw == "" {
			writeError(w, http.StatusUnauthorized, "missing bearer API key")
			return
		}
		key, err := s.store.AuthenticateAPIKey(r.Context(), raw)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid API key")
			return
		}
		if !s.limiter.allow(key.ID, key.RateLimitPerMinute) {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), apiKeyContext, key)))
	})
}

func (s *Server) withAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.adminToken == "" {
			writeError(w, http.StatusServiceUnavailable, "ADMIN_TOKEN is not configured")
			return
		}
		raw := bearer(r.Header.Get("Authorization"))
		if len(raw) != len(s.adminToken) || subtle.ConstantTimeCompare([]byte(raw), []byte(s.adminToken)) != 1 {
			writeError(w, http.StatusUnauthorized, "invalid admin token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func bearer(header string) string {
	parts := strings.SplitN(header, " ", 2)
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		return strings.TrimSpace(parts[1])
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

func requestLog(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Info("request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(start))
	})
}

type rateWindow struct {
	start time.Time
	count int
}

type rateLimiter struct {
	mu    sync.Mutex
	items map[string]rateWindow
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{items: map[string]rateWindow{}}
}

func (l *rateLimiter) allow(id string, limit int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	window := l.items[id]
	if window.start.IsZero() || now.Sub(window.start) >= time.Minute {
		l.items[id] = rateWindow{start: now, count: 1}
		return true
	}
	if window.count >= limit {
		return false
	}
	window.count++
	l.items[id] = window
	return true
}

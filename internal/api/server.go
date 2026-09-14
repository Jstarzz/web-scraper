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
const apiKeyContext contextKey = "api-key"

type Server struct {
	store *store.Store
	adminToken string
	limiter *rateLimiter
	log *slog.Logger
}

func New(st *store.Store, adminToken string, log *slog.Logger) http.Handler {
	s := &Server{store:st,adminToken:adminToken,limiter:newRateLimiter(),log:log}
	mux:=http.NewServeMux()
	mux.HandleFunc("GET /healthz",s.health)
	mux.HandleFunc("GET /readyz",s.ready)
	mux.Handle("POST /v1/search",s.withAPIKey(http.HandlerFunc(s.search)))
	mux.Handle("GET /v1/jobs/{id}",s.withAPIKey(http.HandlerFunc(s.job)))
	mux.Handle("GET /v1/history/{marketplace}/{externalID}",s.withAPIKey(http.HandlerFunc(s.history)))
	mux.Handle("POST /admin/api-keys",s.withAdmin(http.HandlerFunc(s.createAPIKey)))
	mux.Handle("GET /admin/workers",s.withAdmin(http.HandlerFunc(s.workers)))
	return requestLog(log,mux)
}

func (s *Server) health(w http.ResponseWriter,r *http.Request){writeJSON(w,http.StatusOK,map[string]any{"ok":true})}
func (s *Server) ready(w http.ResponseWriter,r *http.Request){if err:=s.store.Ping(r.Context());err!=nil{writeError(w,http.StatusServiceUnavailable,"database unavailable");return};writeJSON(w,http.StatusOK,map[string]any{"ok":true})}

func (s *Server) search(w http.ResponseWriter,r *http.Request){
	var req model.SearchRequest
	if err:=json.NewDecoder(http.MaxBytesReader(w,r.Body,64<<10)).Decode(&req);err!=nil{writeError(w,400,"invalid JSON");return}
	req.Marketplace=strings.ToLower(strings.TrimSpace(req.Marketplace)); req.Query=strings.TrimSpace(req.Query)
	if req.Marketplace!="amazon"&&req.Marketplace!="aliexpress"&&req.Marketplace!="ebay"{writeError(w,400,"marketplace must be amazon, aliexpress, or ebay");return}
	if req.Query==""{writeError(w,400,"query is required");return}
	if req.Limit==0{req.Limit=20}; if req.Limit<1||req.Limit>100{writeError(w,400,"limit must be 1..100");return}
	if req.WaitMS==0{req.WaitMS=12000}; if req.WaitMS<0||req.WaitMS>30000{writeError(w,400,"wait_ms must be 0..30000");return}
	key:=r.Context().Value(apiKeyContext).(store.APIKey)
	job,err:=s.store.CreateJob(r.Context(),req,key.ID); if err!=nil{writeError(w,500,"could not enqueue job");return}
	if req.WaitMS==0{writeJSON(w,http.StatusAccepted,job);return}
	deadline:=time.Now().Add(time.Duration(req.WaitMS)*time.Millisecond)
	for time.Now().Before(deadline){
		time.Sleep(100*time.Millisecond)
		current,err:=s.store.GetJob(r.Context(),job.ID,key.ID); if err!=nil{break}
		if current.Status=="complete"{writeJSON(w,http.StatusOK,current);return}
		if current.Status=="failed"{writeJSON(w,http.StatusBadGateway,current);return}
	}
	current,err:=s.store.GetJob(r.Context(),job.ID,key.ID);if err!=nil{current=job}
	writeJSON(w,http.StatusAccepted,current)
}

func (s *Server) job(w http.ResponseWriter,r *http.Request){key:=r.Context().Value(apiKeyContext).(store.APIKey);job,err:=s.store.GetJob(r.Context(),r.PathValue("id"),key.ID);if errors.Is(err,pgx.ErrNoRows){writeError(w,404,"job not found");return};if err!=nil{writeError(w,500,"could not read job");return};writeJSON(w,200,job)}

func (s *Server) history(w http.ResponseWriter,r *http.Request){days:=60;if raw:=r.URL.Query().Get("days");raw!=""{if n,err:=strconv.Atoi(raw);err==nil{days=n}};points,err:=s.store.History(r.Context(),r.PathValue("marketplace"),r.PathValue("externalID"),days);if err!=nil{writeError(w,500,"could not read history");return};writeJSON(w,200,map[string]any{"marketplace":r.PathValue("marketplace"),"external_id":r.PathValue("externalID"),"points":points})}

func (s *Server) createAPIKey(w http.ResponseWriter,r *http.Request){var body struct{Name string `json:"name"`; RateLimitPerMinute int `json:"rate_limit_per_minute"`};if err:=json.NewDecoder(http.MaxBytesReader(w,r.Body,16<<10)).Decode(&body);err!=nil{writeError(w,400,"invalid JSON");return};key,err:=s.store.CreateAPIKey(r.Context(),strings.TrimSpace(body.Name),body.RateLimitPerMinute);if err!=nil{writeError(w,400,err.Error());return};writeJSON(w,http.StatusCreated,key)}
func (s *Server) workers(w http.ResponseWriter,r *http.Request){workers,err:=s.store.ListWorkers(r.Context());if err!=nil{writeError(w,500,"could not list workers");return};writeJSON(w,200,map[string]any{"workers":workers})}

func (s *Server) withAPIKey(next http.Handler) http.Handler{return http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){raw:=bearer(r.Header.Get("Authorization"));if raw==""{writeError(w,401,"missing bearer API key");return};key,err:=s.store.AuthenticateAPIKey(r.Context(),raw);if err!=nil{writeError(w,401,"invalid API key");return};if !s.limiter.allow(key.ID,key.RateLimitPerMinute){w.Header().Set("Retry-After","60");writeError(w,429,"rate limit exceeded");return};next.ServeHTTP(w,r.WithContext(context.WithValue(r.Context(),apiKeyContext,key)))})}
func (s *Server) withAdmin(next http.Handler) http.Handler{return http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){if s.adminToken==""{writeError(w,503,"ADMIN_TOKEN is not configured");return};raw:=bearer(r.Header.Get("Authorization"));if len(raw)!=len(s.adminToken)||subtle.ConstantTimeCompare([]byte(raw),[]byte(s.adminToken))!=1{writeError(w,401,"invalid admin token");return};next.ServeHTTP(w,r)})}
func bearer(header string)string{parts:=strings.SplitN(header," ",2);if len(parts)==2&&strings.EqualFold(parts[0],"Bearer"){return strings.TrimSpace(parts[1])};return ""}

func writeJSON(w http.ResponseWriter,status int,value any){w.Header().Set("Content-Type","application/json");w.WriteHeader(status);_ = json.NewEncoder(w).Encode(value)}
func writeError(w http.ResponseWriter,status int,message string){writeJSON(w,status,map[string]any{"error":message})}
func requestLog(log *slog.Logger,next http.Handler)http.Handler{return http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){start:=time.Now();next.ServeHTTP(w,r);log.Info("request","method",r.Method,"path",r.URL.Path,"duration",time.Since(start))})}

type rateWindow struct{start time.Time;count int}
type rateLimiter struct{mu sync.Mutex;items map[string]rateWindow}
func newRateLimiter()*rateLimiter{return &rateLimiter{items:map[string]rateWindow{}}}
func (l *rateLimiter)allow(id string,limit int)bool{l.mu.Lock();defer l.mu.Unlock();now:=time.Now();w:=l.items[id];if w.start.IsZero()||now.Sub(w.start)>=time.Minute{l.items[id]=rateWindow{start:now,count:1};return true};if w.count>=limit{return false};w.count++;l.items[id]=w;return true}

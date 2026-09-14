package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Jstarzz/web-scraper/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaFS embed.FS

type Store struct { pool *pgxpool.Pool }

type APIKey struct {
	ID string `json:"id"`
	Name string `json:"name"`
	Prefix string `json:"prefix"`
	RateLimitPerMinute int `json:"rate_limit_per_minute"`
}

type CreatedAPIKey struct {
	APIKey
	Key string `json:"key"`
}

func Open(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil { return nil, err }
	if err := pool.Ping(ctx); err != nil { pool.Close(); return nil, err }
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *Store) Migrate(ctx context.Context) error {
	schema, err := schemaFS.ReadFile("schema.sql")
	if err != nil { return err }
	_, err = s.pool.Exec(ctx, string(schema))
	return err
}

func hashKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func (s *Store) CreateAPIKey(ctx context.Context, name string, rpm int) (CreatedAPIKey, error) {
	if name == "" { return CreatedAPIKey{}, errors.New("name is required") }
	if rpm <= 0 { rpm = 120 }
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil { return CreatedAPIKey{}, err }
	raw := "ws_live_" + base64.RawURLEncoding.EncodeToString(secret)
	prefix := raw[:16]
	var created CreatedAPIKey
	created.Key = raw
	err := s.pool.QueryRow(ctx, `INSERT INTO api_keys (name,key_prefix,key_hash,rate_limit_per_minute) VALUES ($1,$2,$3,$4) RETURNING id::text,name,key_prefix,rate_limit_per_minute`, name, prefix, hashKey(raw), rpm).Scan(&created.ID, &created.Name, &created.Prefix, &created.RateLimitPerMinute)
	return created, err
}

func (s *Store) AuthenticateAPIKey(ctx context.Context, raw string) (APIKey, error) {
	var key APIKey
	err := s.pool.QueryRow(ctx, `UPDATE api_keys SET last_used_at=now() WHERE key_hash=$1 AND revoked_at IS NULL RETURNING id::text,name,key_prefix,rate_limit_per_minute`, hashKey(raw)).Scan(&key.ID, &key.Name, &key.Prefix, &key.RateLimitPerMinute)
	return key, err
}

func (s *Store) CreateJob(ctx context.Context, req model.SearchRequest, requester string) (model.Job, error) {
	var job model.Job
	err := s.pool.QueryRow(ctx, `INSERT INTO scrape_jobs (marketplace,query,result_limit,requested_by) VALUES ($1,$2,$3,$4::uuid) RETURNING id::text,marketplace,query,result_limit,status,created_at`, req.Marketplace, req.Query, req.Limit, requester).Scan(&job.ID,&job.Marketplace,&job.Query,&job.Limit,&job.Status,&job.CreatedAt)
	return job, err
}

func (s *Store) GetJob(ctx context.Context, id, requester string) (model.Job, error) {
	var job model.Job
	var result []byte
	var worker, errText *string
	var finished *time.Time
	err := s.pool.QueryRow(ctx, `SELECT id::text,marketplace,query,result_limit,status,worker_id,error,result,created_at,finished_at FROM scrape_jobs WHERE id=$1::uuid AND requested_by=$2::uuid`, id, requester).Scan(&job.ID,&job.Marketplace,&job.Query,&job.Limit,&job.Status,&worker,&errText,&result,&job.CreatedAt,&finished)
	if err != nil { return model.Job{}, err }
	if worker != nil { job.WorkerID=*worker }
	if errText != nil { job.Error=*errText }
	job.FinishedAt=finished
	if len(result) > 0 { _ = json.Unmarshal(result, &job.Result) }
	return job,nil
}

func (s *Store) ClaimJob(ctx context.Context, workerID string) (model.Job, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil { return model.Job{}, err }
	defer tx.Rollback(ctx)
	var job model.Job
	err = tx.QueryRow(ctx, `WITH next AS (
		SELECT id FROM scrape_jobs WHERE status='queued' ORDER BY priority DESC,created_at FOR UPDATE SKIP LOCKED LIMIT 1
	) UPDATE scrape_jobs j SET status='running',claimed_at=now(),worker_id=$1 FROM next WHERE j.id=next.id RETURNING j.id::text,j.marketplace,j.query,j.result_limit,j.status,j.created_at`, workerID).Scan(&job.ID,&job.Marketplace,&job.Query,&job.Limit,&job.Status,&job.CreatedAt)
	if err != nil { return model.Job{}, err }
	if err := tx.Commit(ctx); err != nil { return model.Job{}, err }
	return job,nil
}

func (s *Store) CompleteJob(ctx context.Context, job model.Job, listings []model.Listing) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil { return err }
	defer tx.Rollback(ctx)
	for _, item := range listings {
		if item.ExternalID == "" || item.URL == "" || item.Title == "" { continue }
		var productID int64
		err = tx.QueryRow(ctx, `INSERT INTO products (marketplace,external_id,canonical_url,title,image_url,seller)
			VALUES ($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''))
			ON CONFLICT (marketplace,external_id) DO UPDATE SET canonical_url=EXCLUDED.canonical_url,title=EXCLUDED.title,image_url=COALESCE(EXCLUDED.image_url,products.image_url),seller=COALESCE(EXCLUDED.seller,products.seller),updated_at=now()
			RETURNING id`, item.Marketplace,item.ExternalID,item.URL,item.Title,item.ImageURL,item.Seller).Scan(&productID)
		if err != nil { return err }
		_, err = tx.Exec(ctx, `INSERT INTO observations (product_id,job_id,price_minor,shipping_minor,currency,available,rating,review_count,source)
			SELECT $1,$2::uuid,$3,$4,NULLIF($5,''),$6,$7,$8,$9
			WHERE NOT EXISTS (
				SELECT 1 FROM observations o
				WHERE o.id=(SELECT id FROM observations WHERE product_id=$1 ORDER BY observed_at DESC LIMIT 1)
				AND o.price_minor IS NOT DISTINCT FROM $3 AND o.shipping_minor IS NOT DISTINCT FROM $4
				AND o.currency IS NOT DISTINCT FROM NULLIF($5,'') AND o.available IS NOT DISTINCT FROM $6
				AND o.rating IS NOT DISTINCT FROM $7 AND o.review_count IS NOT DISTINCT FROM $8
				AND o.observed_at > now()-interval '24 hours'
			)`, productID,job.ID,item.PriceMinor,item.ShipMinor,item.Currency,item.Available,item.Rating,item.ReviewCount,item.Marketplace)
		if err != nil { return err }
	}
	payload, err := json.Marshal(listings)
	if err != nil { return err }
	_, err = tx.Exec(ctx, `UPDATE scrape_jobs SET status='complete',result=$2::jsonb,finished_at=now(),error=NULL WHERE id=$1::uuid`, job.ID, string(payload))
	if err != nil { return err }
	return tx.Commit(ctx)
}

func (s *Store) FailJob(ctx context.Context, id, message string) error {
	_, err := s.pool.Exec(ctx, `UPDATE scrape_jobs SET status='failed',error=$2,finished_at=now() WHERE id=$1::uuid`, id, message)
	return err
}

func (s *Store) RequeueStaleJobs(ctx context.Context, olderThan time.Duration) (int64,error) {
	result, err := s.pool.Exec(ctx, `UPDATE scrape_jobs SET status='queued',claimed_at=NULL,worker_id=NULL,error='requeued after stale worker claim' WHERE status='running' AND claimed_at < now()-($1 * interval '1 second')`, olderThan.Seconds())
	if err != nil { return 0,err }
	return result.RowsAffected(),nil
}

func (s *Store) TouchWorker(ctx context.Context, id string, capabilities []string) error {
	payload,_ := json.Marshal(capabilities)
	_,err := s.pool.Exec(ctx, `INSERT INTO workers (id,last_seen,capabilities) VALUES ($1,now(),$2::jsonb) ON CONFLICT (id) DO UPDATE SET last_seen=now(),capabilities=EXCLUDED.capabilities`,id,string(payload))
	return err
}

func (s *Store) ListWorkers(ctx context.Context) ([]model.Worker,error) {
	rows,err:=s.pool.Query(ctx,`SELECT id,last_seen,capabilities FROM workers ORDER BY id`)
	if err!=nil{return nil,err}; defer rows.Close()
	workers:=[]model.Worker{}
	for rows.Next(){ var w model.Worker; var raw []byte; if err:=rows.Scan(&w.ID,&w.LastSeen,&raw);err!=nil{return nil,err}; _=json.Unmarshal(raw,&w.Capabilities); workers=append(workers,w)}
	return workers,rows.Err()
}

func (s *Store) History(ctx context.Context, marketplace, externalID string, days int) ([]model.PricePoint,error) {
	if days<1 {days=60}; if days>365 {days=365}
	rows,err:=s.pool.Query(ctx,`SELECT o.observed_at,o.price_minor,o.shipping_minor,COALESCE(o.currency,''),o.available,o.rating,o.review_count,o.source FROM observations o JOIN products p ON p.id=o.product_id WHERE p.marketplace=$1 AND p.external_id=$2 AND o.observed_at >= now()-($3::text || ' days')::interval ORDER BY o.observed_at`,marketplace,externalID,days)
	if err!=nil{return nil,err}; defer rows.Close()
	points:=[]model.PricePoint{}
	for rows.Next(){var p model.PricePoint;if err:=rows.Scan(&p.ObservedAt,&p.PriceMinor,&p.ShipMinor,&p.Currency,&p.Available,&p.Rating,&p.ReviewCount,&p.Source);err!=nil{return nil,err};points=append(points,p)}
	return points,rows.Err()
}

func (s *Store) PruneHistory(ctx context.Context, days int) (int64,error) {
	result,err:=s.pool.Exec(ctx,`DELETE FROM observations WHERE observed_at < now()-($1::text || ' days')::interval`,fmt.Sprint(days))
	if err!=nil{return 0,err}; return result.RowsAffected(),nil
}

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

var ErrJobClaimLost = errors.New("job claim lost")

type Store struct {
	pool *pgxpool.Pool
}

type APIKey struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Prefix             string `json:"prefix"`
	RateLimitPerMinute int    `json:"rate_limit_per_minute"`
}

type CreatedAPIKey struct {
	APIKey
	Key string `json:"key"`
}

func Open(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() {
	s.pool.Close()
}

func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

func (s *Store) Migrate(ctx context.Context) error {
	schema, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, string(schema))
	return err
}

func hashKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func (s *Store) CreateAPIKey(ctx context.Context, name string, rpm int) (CreatedAPIKey, error) {
	if name == "" {
		return CreatedAPIKey{}, errors.New("name is required")
	}
	if rpm <= 0 {
		rpm = 120
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return CreatedAPIKey{}, err
	}
	raw := "ws_live_" + base64.RawURLEncoding.EncodeToString(secret)
	prefix := raw[:16]
	var created CreatedAPIKey
	created.Key = raw
	err := s.pool.QueryRow(ctx, `
		INSERT INTO api_keys (name,key_prefix,key_hash,rate_limit_per_minute)
		VALUES ($1,$2,$3,$4)
		RETURNING id::text,name,key_prefix,rate_limit_per_minute`,
		name, prefix, hashKey(raw), rpm,
	).Scan(&created.ID, &created.Name, &created.Prefix, &created.RateLimitPerMinute)
	return created, err
}

func (s *Store) AuthenticateAPIKey(ctx context.Context, raw string) (APIKey, error) {
	var key APIKey
	err := s.pool.QueryRow(ctx, `
		UPDATE api_keys
		SET last_used_at=now()
		WHERE key_hash=$1 AND revoked_at IS NULL
		RETURNING id::text,name,key_prefix,rate_limit_per_minute`,
		hashKey(raw),
	).Scan(&key.ID, &key.Name, &key.Prefix, &key.RateLimitPerMinute)
	return key, err
}

func (s *Store) CreateJob(ctx context.Context, req model.SearchRequest, requester string) (model.Job, error) {
	var job model.Job
	err := s.pool.QueryRow(ctx, `
		INSERT INTO scrape_jobs (marketplace,query,result_limit,requested_by)
		VALUES ($1,$2,$3,$4::uuid)
		RETURNING id::text,marketplace,query,result_limit,status,created_at`,
		req.Marketplace, req.Query, req.Limit, requester,
	).Scan(&job.ID, &job.Marketplace, &job.Query, &job.Limit, &job.Status, &job.CreatedAt)
	return job, err
}

func scanJob(row pgx.Row) (model.Job, error) {
	var job model.Job
	var result []byte
	var worker, errText *string
	var finished *time.Time
	if err := row.Scan(
		&job.ID,
		&job.Marketplace,
		&job.Query,
		&job.Limit,
		&job.Status,
		&worker,
		&errText,
		&result,
		&job.CreatedAt,
		&finished,
	); err != nil {
		return model.Job{}, err
	}
	if worker != nil {
		job.WorkerID = *worker
	}
	if errText != nil {
		job.Error = *errText
	}
	job.FinishedAt = finished
	if len(result) > 0 {
		_ = json.Unmarshal(result, &job.Result)
	}
	return job, nil
}

func (s *Store) GetJob(ctx context.Context, id, requester string) (model.Job, error) {
	return scanJob(s.pool.QueryRow(ctx, `
		SELECT id::text,marketplace,query,result_limit,status,worker_id,error,result,created_at,finished_at
		FROM scrape_jobs
		WHERE id=$1::uuid AND requested_by=$2::uuid`,
		id, requester,
	))
}

// GetJobByAPIKey authorizes a status read and loads the job in one read-only
// query. Polling therefore does not consume the search rate bucket or update
// api_keys.last_used_at on every status check.
func (s *Store) GetJobByAPIKey(ctx context.Context, id, rawAPIKey string) (model.Job, error) {
	return scanJob(s.pool.QueryRow(ctx, `
		SELECT j.id::text,j.marketplace,j.query,j.result_limit,j.status,j.worker_id,j.error,j.result,j.created_at,j.finished_at
		FROM scrape_jobs j
		JOIN api_keys k ON k.id=j.requested_by
		WHERE j.id=$1::uuid
		  AND k.key_hash=$2
		  AND k.revoked_at IS NULL`,
		id, hashKey(rawAPIKey),
	))
}

func (s *Store) ClaimJob(ctx context.Context, workerID string) (model.Job, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return model.Job{}, err
	}
	defer tx.Rollback(ctx)

	var job model.Job
	err = tx.QueryRow(ctx, `
		WITH next AS (
			SELECT id
			FROM scrape_jobs
			WHERE status='queued'
			ORDER BY priority DESC,created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE scrape_jobs j
		SET status='running',claimed_at=now(),worker_id=$1,error=NULL
		FROM next
		WHERE j.id=next.id
		RETURNING j.id::text,j.marketplace,j.query,j.result_limit,j.status,j.worker_id,j.created_at`,
		workerID,
	).Scan(&job.ID, &job.Marketplace, &job.Query, &job.Limit, &job.Status, &job.WorkerID, &job.CreatedAt)
	if err != nil {
		return model.Job{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.Job{}, err
	}
	return job, nil
}

func (s *Store) RenewJobClaim(ctx context.Context, jobID, workerID string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE scrape_jobs
		SET claimed_at=now()
		WHERE id=$1::uuid AND status='running' AND worker_id=$2`,
		jobID, workerID,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrJobClaimLost
	}
	return nil
}

func (s *Store) CompleteJob(ctx context.Context, jobID, workerID string, listings []model.Listing) error {
	payload, err := json.Marshal(listings)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var requester string
	if err := tx.QueryRow(ctx, `
		UPDATE scrape_jobs
		SET status='complete',result=$3,finished_at=now()
		WHERE id=$1::uuid AND status='running' AND worker_id=$2
		RETURNING requested_by::text`, jobID, workerID, payload).Scan(&requester); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrJobClaimLost
		}
		return err
	}
	for _, listing := range listings {
		_, err := tx.Exec(ctx, `
			INSERT INTO listings (marketplace,external_id,title,url,image_url,seller,rating,review_count,sold_count,price_minor,shipping_minor,currency,available,sponsored,raw,observed_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,now())
			ON CONFLICT (marketplace,external_id) DO UPDATE SET
				title=EXCLUDED.title,url=EXCLUDED.url,image_url=EXCLUDED.image_url,seller=EXCLUDED.seller,rating=EXCLUDED.rating,
				review_count=EXCLUDED.review_count,sold_count=EXCLUDED.sold_count,price_minor=EXCLUDED.price_minor,
				shipping_minor=EXCLUDED.shipping_minor,currency=EXCLUDED.currency,available=EXCLUDED.available,sponsored=EXCLUDED.sponsored,
				raw=EXCLUDED.raw,observed_at=now()`,
			listing.Marketplace, listing.ExternalID, listing.Title, listing.URL, listing.ImageURL, listing.Seller,
			listing.Rating, listing.ReviewCount, listing.SoldCount, listing.PriceMinor, listing.ShippingMinor,
			listing.Currency, listing.Available, listing.Sponsored, listing.Raw,
		)
		if err != nil {
			return err
		}
		if listing.PriceMinor != nil {
			_, err = tx.Exec(ctx, `
				INSERT INTO price_history (marketplace,external_id,price_minor,shipping_minor,currency,observed_at)
				VALUES ($1,$2,$3,$4,$5,now())`,
				listing.Marketplace, listing.ExternalID, listing.PriceMinor, listing.ShippingMinor, listing.Currency,
			)
			if err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) FailJob(ctx context.Context, jobID, workerID, message string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE scrape_jobs SET status='failed',error=$3,finished_at=now()
		WHERE id=$1::uuid AND status='running' AND worker_id=$2`,
		jobID, workerID, message,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrJobClaimLost
	}
	return nil
}

func (s *Store) RequeueStaleJobs(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE scrape_jobs
		SET status='queued',worker_id=NULL,claimed_at=NULL,error='requeued after stale worker claim'
		WHERE status='running' AND claimed_at < now() - $1::interval`,
		intervalString(olderThan),
	)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (s *Store) UpsertWorker(ctx context.Context, workerID, hostname string, maxConcurrency, inFlight int, metadata map[string]any) error {
	payload, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO workers (id,hostname,max_concurrency,in_flight,last_seen_at,metadata)
		VALUES ($1,$2,$3,$4,now(),$5)
		ON CONFLICT (id) DO UPDATE SET
			hostname=EXCLUDED.hostname,max_concurrency=EXCLUDED.max_concurrency,in_flight=EXCLUDED.in_flight,last_seen_at=now(),metadata=EXCLUDED.metadata`,
		workerID, hostname, maxConcurrency, inFlight, payload,
	)
	return err
}

func (s *Store) ListWorkers(ctx context.Context) ([]model.Worker, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id,hostname,max_concurrency,in_flight,last_seen_at,metadata
		FROM workers
		ORDER BY last_seen_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var workers []model.Worker
	for rows.Next() {
		var worker model.Worker
		var payload []byte
		if err := rows.Scan(&worker.ID, &worker.Hostname, &worker.MaxConcurrency, &worker.InFlight, &worker.LastSeenAt, &payload); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(payload, &worker.Metadata)
		workers = append(workers, worker)
	}
	return workers, rows.Err()
}

func (s *Store) History(ctx context.Context, marketplace, externalID string, days int) ([]model.PricePoint, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT price_minor,shipping_minor,currency,observed_at
		FROM price_history
		WHERE marketplace=$1 AND external_id=$2 AND observed_at >= now() - ($3 || ' days')::interval
		ORDER BY observed_at`, marketplace, externalID, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var points []model.PricePoint
	for rows.Next() {
		var p model.PricePoint
		if err := rows.Scan(&p.PriceMinor, &p.ShippingMinor, &p.Currency, &p.ObservedAt); err != nil {
			return nil, err
		}
		points = append(points, p)
	}
	return points, rows.Err()
}

func (s *Store) ListAPIKeys(ctx context.Context) ([]APIKey, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id::text,name,key_prefix,rate_limit_per_minute
		FROM api_keys
		WHERE revoked_at IS NULL
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []APIKey
	for rows.Next() {
		var key APIKey
		if err := rows.Scan(&key.ID, &key.Name, &key.Prefix, &key.RateLimitPerMinute); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (s *Store) RevokeAPIKey(ctx context.Context, id string) error {
	return s.pool.QueryRow(ctx, `
		UPDATE api_keys SET revoked_at=now()
		WHERE id=$1::uuid AND revoked_at IS NULL
		RETURNING id`, id).Scan(&id)
}

func intervalString(d time.Duration) string {
	return fmt.Sprintf("%f seconds", d.Seconds())
}

package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type APIKeyRecord struct {
	APIKey
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

func (s *Store) ListAPIKeys(ctx context.Context) ([]APIKeyRecord, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id::text,name,key_prefix,rate_limit_per_minute,created_at,last_used_at,revoked_at
		FROM api_keys
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	keys := make([]APIKeyRecord, 0)
	for rows.Next() {
		var key APIKeyRecord
		if err := rows.Scan(
			&key.ID,
			&key.Name,
			&key.Prefix,
			&key.RateLimitPerMinute,
			&key.CreatedAt,
			&key.LastUsedAt,
			&key.RevokedAt,
		); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (s *Store) RevokeAPIKey(ctx context.Context, id string) error {
	row := s.pool.QueryRow(ctx, `
		UPDATE api_keys
		SET revoked_at = COALESCE(revoked_at, now())
		WHERE id=$1::uuid
		RETURNING id`, id)
	var returned string
	if err := row.Scan(&returned); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pgx.ErrNoRows
		}
		return err
	}
	return nil
}

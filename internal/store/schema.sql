CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS api_keys (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL,
    key_prefix text NOT NULL,
    key_hash text NOT NULL UNIQUE,
    rate_limit_per_minute integer NOT NULL DEFAULT 120 CHECK (rate_limit_per_minute > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    revoked_at timestamptz
);

CREATE TABLE IF NOT EXISTS scrape_jobs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    marketplace text NOT NULL CHECK (marketplace IN ('amazon','aliexpress','ebay')),
    query text NOT NULL,
    result_limit integer NOT NULL CHECK (result_limit BETWEEN 1 AND 100),
    status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','running','complete','failed')),
    priority integer NOT NULL DEFAULT 0,
    requested_by uuid NOT NULL REFERENCES api_keys(id),
    worker_id text,
    result jsonb,
    error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    claimed_at timestamptz,
    finished_at timestamptz
);
CREATE INDEX IF NOT EXISTS scrape_jobs_queue_idx ON scrape_jobs (status, priority DESC, created_at);
CREATE INDEX IF NOT EXISTS scrape_jobs_requester_idx ON scrape_jobs (requested_by, created_at DESC);

CREATE TABLE IF NOT EXISTS products (
    id bigserial PRIMARY KEY,
    marketplace text NOT NULL,
    external_id text NOT NULL,
    canonical_url text NOT NULL,
    title text NOT NULL,
    image_url text,
    seller text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (marketplace, external_id)
);

CREATE TABLE IF NOT EXISTS observations (
    id bigserial PRIMARY KEY,
    product_id bigint NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    job_id uuid REFERENCES scrape_jobs(id) ON DELETE SET NULL,
    observed_at timestamptz NOT NULL DEFAULT now(),
    price_minor bigint,
    original_price_minor bigint,
    shipping_minor bigint,
    currency text,
    available boolean,
    rating double precision,
    review_count bigint,
    sold_count bigint,
    sponsored boolean,
    source text NOT NULL
);

-- schema.sql is intentionally idempotent and doubles as the lightweight migration path
-- for existing LXC installs created before these richer extraction fields existed.
ALTER TABLE observations ADD COLUMN IF NOT EXISTS original_price_minor bigint;
ALTER TABLE observations ADD COLUMN IF NOT EXISTS sold_count bigint;
ALTER TABLE observations ADD COLUMN IF NOT EXISTS sponsored boolean;

CREATE INDEX IF NOT EXISTS observations_product_time_idx ON observations (product_id, observed_at DESC);
CREATE INDEX IF NOT EXISTS observations_time_idx ON observations (observed_at);

CREATE TABLE IF NOT EXISTS workers (
    id text PRIMARY KEY,
    last_seen timestamptz NOT NULL DEFAULT now(),
    capabilities jsonb NOT NULL DEFAULT '[]'::jsonb
);

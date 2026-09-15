package model

import "time"

type SearchRequest struct {
	Marketplace string `json:"marketplace"`
	Query       string `json:"query"`
	Limit       int    `json:"limit"`
	WaitMS      int    `json:"wait_ms,omitempty"`
}

type Listing struct {
	Marketplace        string   `json:"marketplace"`
	ExternalID         string   `json:"external_id"`
	Title              string   `json:"title"`
	URL                string   `json:"url"`
	ImageURL           string   `json:"image_url,omitempty"`
	Seller             string   `json:"seller,omitempty"`
	PriceMinor         *int64   `json:"price_minor,omitempty"`
	OriginalPriceMinor *int64   `json:"original_price_minor,omitempty"`
	ShipMinor          *int64   `json:"shipping_minor,omitempty"`
	Currency           string   `json:"currency,omitempty"`
	Available          *bool    `json:"available,omitempty"`
	Rating             *float64 `json:"rating,omitempty"`
	ReviewCount        *int64   `json:"review_count,omitempty"`
	SoldCount          *int64   `json:"sold_count,omitempty"`
	Sponsored          *bool    `json:"sponsored,omitempty"`
}

type Job struct {
	ID          string     `json:"id"`
	Marketplace string     `json:"marketplace"`
	Query       string     `json:"query"`
	Limit       int        `json:"limit"`
	Status      string     `json:"status"`
	WorkerID    string     `json:"worker_id,omitempty"`
	Error       string     `json:"error,omitempty"`
	Result      []Listing  `json:"result,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
}

type Worker struct {
	ID           string    `json:"id"`
	LastSeen     time.Time `json:"last_seen"`
	Capabilities []string  `json:"capabilities"`
}

type PricePoint struct {
	ObservedAt         time.Time `json:"observed_at"`
	PriceMinor         *int64    `json:"price_minor,omitempty"`
	OriginalPriceMinor *int64    `json:"original_price_minor,omitempty"`
	ShipMinor          *int64    `json:"shipping_minor,omitempty"`
	Currency           string    `json:"currency,omitempty"`
	Available          *bool     `json:"available,omitempty"`
	Rating             *float64  `json:"rating,omitempty"`
	ReviewCount        *int64    `json:"review_count,omitempty"`
	SoldCount          *int64    `json:"sold_count,omitempty"`
	Sponsored          *bool     `json:"sponsored,omitempty"`
	Source             string    `json:"source"`
}

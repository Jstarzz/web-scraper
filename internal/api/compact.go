package api

import (
	"net/http"
	"strings"

	"github.com/Jstarzz/web-scraper/internal/model"
)

type compactListing struct {
	ExternalID         string   `json:"external_id"`
	Title              string   `json:"title"`
	URL                string   `json:"url"`
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

type compactJob struct {
	ID          string           `json:"id"`
	Marketplace string           `json:"marketplace"`
	Query       string           `json:"query"`
	Status      string           `json:"status"`
	Error       string           `json:"error,omitempty"`
	Result      []compactListing `json:"result,omitempty"`
}

func wantsCompact(r *http.Request) bool {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("compact"))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

func compactJobView(job model.Job) compactJob {
	view := compactJob{
		ID:          job.ID,
		Marketplace: job.Marketplace,
		Query:       job.Query,
		Status:      job.Status,
		Error:       job.Error,
	}
	if len(job.Result) == 0 {
		return view
	}

	view.Result = make([]compactListing, 0, len(job.Result))
	for _, item := range job.Result {
		view.Result = append(view.Result, compactListing{
			ExternalID:         item.ExternalID,
			Title:              item.Title,
			URL:                item.URL,
			Seller:             item.Seller,
			PriceMinor:         item.PriceMinor,
			OriginalPriceMinor: item.OriginalPriceMinor,
			ShipMinor:          item.ShipMinor,
			Currency:           item.Currency,
			Available:          item.Available,
			Rating:             item.Rating,
			ReviewCount:        item.ReviewCount,
			SoldCount:          item.SoldCount,
			Sponsored:          item.Sponsored,
		})
	}
	return view
}

func writeJobJSON(w http.ResponseWriter, status int, job model.Job, compact bool) {
	if compact {
		writeJSON(w, status, compactJobView(job))
		return
	}
	writeJSON(w, status, job)
}

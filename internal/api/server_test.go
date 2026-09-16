package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Jstarzz/web-scraper/internal/model"
)

func TestRateLimiter(t *testing.T) {
	l := newRateLimiter()
	if !l.allow("a", 2) {
		t.Fatal("first request should pass")
	}
	if !l.allow("a", 2) {
		t.Fatal("second request should pass")
	}
	if l.allow("a", 2) {
		t.Fatal("third request should be limited")
	}
	if !l.allow("b", 1) {
		t.Fatal("limits must be isolated per key")
	}
}

func TestWantsCompact(t *testing.T) {
	for _, value := range []string{"1", "true", "YES"} {
		req := httptest.NewRequest("GET", "/v1/jobs/test?compact="+value, nil)
		if !wantsCompact(req) {
			t.Fatalf("compact=%s should enable compact output", value)
		}
	}

	req := httptest.NewRequest("GET", "/v1/jobs/test", nil)
	if wantsCompact(req) {
		t.Fatal("compact output must be opt-in")
	}
}

func TestCompactJobViewDropsHeavyRepeatedFields(t *testing.T) {
	price := int64(3347)
	available := true
	rating := 4.9
	sold := int64(600)
	job := model.Job{
		ID:          "job-1",
		Marketplace: "aliexpress",
		Query:       "esp32 display",
		Status:      "complete",
		WorkerID:    "xeon-primary-1/2",
		Result: []model.Listing{{
			Marketplace: "aliexpress",
			ExternalID:  "1005012957528517",
			Title:       "ESP32 Display Board",
			URL:         "https://www.aliexpress.com/item/1005012957528517.html",
			ImageURL:    "https://ae.example/large-image-url.jpg",
			Seller:      "Example Store",
			PriceMinor:  &price,
			Currency:    "XCD",
			Available:   &available,
			Rating:      &rating,
			SoldCount:   &sold,
		}},
	}

	payload, err := json.Marshal(compactJobView(job))
	if err != nil {
		t.Fatal(err)
	}
	body := string(payload)
	if strings.Contains(body, "image_url") || strings.Contains(body, "worker_id") || strings.Contains(body, "created_at") {
		t.Fatalf("compact response leaked heavy job/listing metadata: %s", body)
	}
	if strings.Count(body, "aliexpress") != 2 { // top-level marketplace + item URL host
		t.Fatalf("listing marketplace should not be repeated: %s", body)
	}
	if !strings.Contains(body, "1005012957528517") || !strings.Contains(body, "3347") {
		t.Fatalf("compact response lost useful product data: %s", body)
	}
}

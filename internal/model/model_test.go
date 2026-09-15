package model

import (
	"encoding/json"
	"testing"
)

func TestSearchRequestWaitMSDistinguishesOmittedFromZero(t *testing.T) {
	var omitted SearchRequest
	if err := json.Unmarshal([]byte(`{"marketplace":"amazon","query":"usb c hub","limit":10}`), &omitted); err != nil {
		t.Fatal(err)
	}
	if omitted.WaitMS != nil {
		t.Fatalf("omitted wait_ms should remain nil, got %d", *omitted.WaitMS)
	}

	var async SearchRequest
	if err := json.Unmarshal([]byte(`{"marketplace":"amazon","query":"usb c hub","limit":10,"wait_ms":0}`), &async); err != nil {
		t.Fatal(err)
	}
	if async.WaitMS == nil {
		t.Fatal("explicit wait_ms=0 must not be treated as omitted")
	}
	if *async.WaitMS != 0 {
		t.Fatalf("expected explicit wait_ms=0, got %d", *async.WaitMS)
	}
}

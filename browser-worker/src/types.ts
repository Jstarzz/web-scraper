export type Marketplace = "amazon" | "aliexpress" | "ebay";

export interface ScrapeRequest {
  marketplace: Marketplace;
  query: string;
  limit?: number;
}

export interface Listing {
  marketplace: Marketplace;
  external_id: string;
  title: string;
  url: string;
  image_url?: string;
  seller?: string;
  price_minor?: number;
  original_price_minor?: number;
  shipping_minor?: number;
  currency?: string;
  available?: boolean;
  rating?: number;
  review_count?: number;
  sold_count?: number;
  sponsored?: boolean;
}

export interface ScrapeTimings {
  direct_fetch_ms?: number;
  direct_parse_ms?: number;
  browser_fetch_ms?: number;
  browser_parse_ms?: number;
}

export interface ScrapeResult {
  listings: Listing[];
  strategy: "http" | "browser" | "hybrid";
  direct_count: number;
  duration_ms: number;
  timings?: ScrapeTimings;
}

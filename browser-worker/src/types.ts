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
  shipping_minor?: number;
  currency?: string;
  available?: boolean;
  rating?: number;
  review_count?: number;
}

export interface ScrapeResult {
  listings: Listing[];
  strategy: "http" | "browser" | "hybrid";
  direct_count: number;
  duration_ms: number;
}

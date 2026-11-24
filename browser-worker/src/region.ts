import type { Marketplace } from "./types.js";

// The scraper's IP geolocates outside the US, so stores pick a local currency
// (AliExpress returned XCD). Pin every store to the US region and USD.
export const regionCookies: Record<Marketplace, { name: string; value: string; domain: string }[]> = {
  amazon: [{ name: "i18n-prefs", value: "USD", domain: ".amazon.com" }],
  aliexpress: [{ name: "aep_usuc_f", value: "site=usa&c_tp=USD&region=US&b_locale=en_US", domain: ".aliexpress.com" }],
  ebay: [],
};

export function cookieHeader(marketplace: Marketplace): string {
  return regionCookies[marketplace].map((c) => `${c.name}=${c.value}`).join("; ");
}

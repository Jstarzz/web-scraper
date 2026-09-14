import * as cheerio from "cheerio";
import type { Listing } from "./types.js";

function compact(text: string | undefined | null): string {
  return (text ?? "").replace(/\s+/g, " ").trim();
}

export function money(text?: string | null): { minor?: number; currency?: string } {
  if (!text) return {};
  const normalized = compact(text).replace(/,/g, "");
  const currency = /CA\$/i.test(normalized)
    ? "CAD"
    : /AU\$/i.test(normalized)
      ? "AUD"
      : /£|\bGBP\b/i.test(normalized)
        ? "GBP"
        : /€|\bEUR\b/i.test(normalized)
          ? "EUR"
          : /\$|\bUSD\b|US\$/i.test(normalized)
            ? "USD"
            : undefined;
  const match = normalized.match(/(?:US|CA|AU)?\s*[$£€]\s*([0-9]+(?:\.[0-9]{1,2})?)/i)
    ?? normalized.match(/\b([0-9]+(?:\.[0-9]{1,2})?)\s*(?:USD|GBP|EUR)\b/i);
  if (!match) return {};
  const value = Number(match[1]);
  if (!Number.isFinite(value)) return {};
  return { minor: Math.round(value * 100), currency };
}

function countFrom(text?: string | null): number | undefined {
  if (!text) return undefined;
  const normalized = compact(text).replace(/,/g, "");
  const match = normalized.match(/([0-9]+(?:\.[0-9]+)?)\s*([KMB])?/i);
  if (!match) return undefined;
  const base = Number(match[1]);
  if (!Number.isFinite(base)) return undefined;
  const suffix = match[2]?.toUpperCase();
  const multiplier = suffix === "K" ? 1_000 : suffix === "M" ? 1_000_000 : suffix === "B" ? 1_000_000_000 : 1;
  return Math.round(base * multiplier);
}

function ratingFrom(text?: string | null): number | undefined {
  if (!text) return undefined;
  const match = compact(text).match(/([0-5](?:\.[0-9]+)?)/);
  if (!match) return undefined;
  const value = Number(match[1]);
  return Number.isFinite(value) && value <= 5 ? value : undefined;
}

function canonical(base: string, href: string): string {
  try {
    const url = new URL(href, base);
    url.hash = "";
    for (const key of [...url.searchParams.keys()]) {
      if (!new Set(["th", "sku_id"]).has(key)) url.searchParams.delete(key);
    }
    return url.toString();
  } catch {
    return href;
  }
}

function imageURL(value: string): string | undefined {
  const cleaned = compact(value);
  if (!cleaned) return undefined;
  return cleaned.startsWith("//") ? `https:${cleaned}` : cleaned;
}

function dedupe(listings: Listing[], limit: number): Listing[] {
  const seen = new Set<string>();
  const out: Listing[] = [];
  for (const item of listings) {
    if (!item.external_id || !item.title || !item.url) continue;
    const key = `${item.marketplace}:${item.external_id}`;
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(item);
    if (out.length >= limit) break;
  }
  return out;
}

export function parseAmazon(html: string, limit: number): Listing[] {
  const $ = cheerio.load(html);
  let cards = $('[data-component-type="s-search-result"]');
  if (!cards.length) {
    cards = $('div[data-asin]').filter((_, raw) => Boolean(compact($(raw).attr("data-asin"))));
  }

  const out: Listing[] = [];
  cards.each((_, raw) => {
    if (out.length >= limit) return false;
    const card = $(raw);
    const asin = compact(card.attr("data-asin"));
    if (!asin) return;
    const link = card.find('h2 a, a[href*="/dp/"]').first();
    const href = link.attr("href") ?? "";
    const title = compact(link.find("span").first().text())
      || compact(card.find("h2 span").first().text())
      || compact(card.find(".a-text-normal").first().text());
    if (!href || !title) return;

    const priceText = compact(card.find(".a-price .a-offscreen").first().text())
      || compact(card.find(".a-price").first().text());
    const price = money(priceText);
    const ratingText = compact(card.find('[aria-label*="out of 5 stars"]').first().attr("aria-label"))
      || compact(card.find(".a-icon-alt").first().text());
    const reviewText = compact(card.find('[aria-label*="ratings"]').first().attr("aria-label"))
      || compact(card.find('a[href*="customerReviews"] span').last().text())
      || compact(card.find(".a-size-base.s-underline-text").first().text());

    out.push({
      marketplace: "amazon",
      external_id: asin,
      title,
      url: canonical("https://www.amazon.com", href),
      image_url: imageURL(card.find("img.s-image").first().attr("src") ?? ""),
      price_minor: price.minor,
      currency: price.currency,
      available: true,
      rating: ratingFrom(ratingText),
      review_count: countFrom(reviewText),
    });
  });
  return dedupe(out, limit);
}

function dig(root: unknown, ...path: Array<string | number>): unknown {
  let value = root;
  for (const key of path) {
    if (typeof key === "number") {
      if (!Array.isArray(value) || key >= value.length) return undefined;
      value = value[key];
      continue;
    }
    if (typeof value !== "object" || value === null || Array.isArray(value)) return undefined;
    value = (value as Record<string, unknown>)[key];
  }
  return value;
}

function textValue(value: unknown): string {
  if (typeof value === "string" || typeof value === "number") return compact(String(value));
  return "";
}

function aliRunParams(html: string, limit: number): Listing[] {
  const match = html.match(/window\.runParams\s*=\s*(\{[\s\S]*?\})\s*;?\s*<\/script>/i);
  if (!match) return [];
  let root: unknown;
  try {
    root = JSON.parse(match[1]);
  } catch {
    return [];
  }
  const items = dig(root, "mods", "itemList", "content");
  if (!Array.isArray(items)) return [];

  const out: Listing[] = [];
  for (const raw of items) {
    if (out.length >= limit) break;
    const id = textValue(dig(raw, "productId"));
    const title = textValue(dig(raw, "title", "displayTitle")) || textValue(dig(raw, "title", "seoTitle"));
    if (!id || !title) continue;

    const formattedPrice = textValue(dig(raw, "prices", "salePrice", "formattedPrice"))
      || textValue(dig(raw, "prices", "originalPrice", "formattedPrice"));
    const parsedPrice = money(formattedPrice);
    const currency = textValue(dig(raw, "prices", "salePrice", "currencyCode")) || parsedPrice.currency;
    const href = textValue(dig(raw, "productDetailUrl")) || `/item/${id}.html`;
    const seller = textValue(dig(raw, "store", "storeName"));
    const rating = ratingFrom(textValue(dig(raw, "evaluation", "starRating")) || textValue(dig(raw, "evaluation", "averageStar")));
    const reviews = countFrom(textValue(dig(raw, "evaluation", "evaluationCount")) || textValue(dig(raw, "evaluation", "totalValidNum")));
    const image = textValue(dig(raw, "image", "imgUrl")) || textValue(dig(raw, "image", "imageUrl"));

    out.push({
      marketplace: "aliexpress",
      external_id: id,
      title,
      url: canonical("https://www.aliexpress.com", href),
      image_url: imageURL(image),
      seller: seller || undefined,
      price_minor: parsedPrice.minor,
      currency: currency || undefined,
      available: true,
      rating,
      review_count: reviews,
    });
  }
  return out;
}

function aliCard(link: cheerio.Cheerio<any>): cheerio.Cheerio<any> {
  const specific = link.closest('[class*="search-item"], [class*="search-card"], [class*="product-card"], [class*="product-item"], [class*="card-item"]');
  if (specific.length) return specific.first();
  let node = link;
  for (let i = 0; i < 4; i++) {
    const parent = node.parent();
    if (!parent.length) break;
    const text = compact(parent.text());
    node = parent;
    if (text.length >= 30 && text.length <= 1200 && /[$£€]|US\s*\$/i.test(text)) return node;
  }
  return link.parent();
}

function aliDOM(html: string, limit: number): Listing[] {
  const $ = cheerio.load(html);
  const out: Listing[] = [];
  const seen = new Set<string>();

  $('a[href*="/item/"]').each((_, raw) => {
    if (out.length >= limit) return false;
    const link = $(raw);
    const href = link.attr("href") ?? "";
    const match = href.match(/\/item\/(\d+)\.html/i);
    if (!match || seen.has(match[1])) return;
    seen.add(match[1]);

    const card = aliCard(link);
    const image = card.find("img").first();
    const title = compact(link.attr("title"))
      || compact(card.find('[class*="title"]').first().text())
      || compact(card.find("h1,h2,h3").first().text())
      || compact(image.attr("alt"))
      || compact(link.text());
    if (!title || title.length < 3) return;

    const priceText = compact(card.find('[class*="price"]').first().text()) || compact(card.text());
    const price = money(priceText);
    const shippingText = compact(card.find('[class*="shipping"]').first().text());
    const shipping = /free\s+shipping/i.test(shippingText) ? { minor: 0, currency: price.currency } : money(shippingText);
    const seller = compact(card.find('[class*="store"], [class*="shop"]').first().text());
    const ratingText = compact(card.find('[class*="rating"], [class*="star"]').first().text());
    const rawImage = image.attr("src") ?? image.attr("data-src") ?? image.attr("data-lazy-src") ?? "";

    out.push({
      marketplace: "aliexpress",
      external_id: match[1],
      title,
      url: canonical("https://www.aliexpress.com", href),
      image_url: imageURL(rawImage),
      seller: seller || undefined,
      price_minor: price.minor,
      shipping_minor: shipping.minor,
      currency: price.currency,
      available: true,
      rating: ratingFrom(ratingText),
    });
  });
  return out;
}

export function parseAliExpress(html: string, limit: number): Listing[] {
  const structured = aliRunParams(html, limit);
  const dom = aliDOM(html, limit);
  return dedupe([...structured, ...dom], limit);
}

export function parseEbay(html: string, limit: number): Listing[] {
  const $ = cheerio.load(html);
  const out: Listing[] = [];
  $("li.s-item").each((_, raw) => {
    if (out.length >= limit) return false;
    const card = $(raw);
    const link = card.find("a.s-item__link").first();
    const href = link.attr("href") ?? "";
    const id = href.match(/\/itm\/(?:[^/]+\/)?(\d+)/)?.[1];
    const title = compact(card.find(".s-item__title").first().text());
    if (!id || !title || title.toLowerCase() === "shop on ebay") return;
    const price = money(compact(card.find(".s-item__price").first().text()));
    const shipText = compact(card.find(".s-item__shipping, .s-item__logisticsCost").first().text());
    const ship = /free/i.test(shipText) ? { minor: 0, currency: price.currency } : money(shipText);
    out.push({
      marketplace: "ebay",
      external_id: id,
      title,
      url: canonical("https://www.ebay.com", href),
      image_url: imageURL(card.find("img").first().attr("src") ?? ""),
      seller: compact(card.find(".s-item__seller-info-text, .s-item__seller-info").first().text()) || undefined,
      price_minor: price.minor,
      shipping_minor: ship.minor,
      currency: price.currency,
      available: true,
    });
  });
  return dedupe(out, limit);
}

export function mergeListings(primary: Listing[], secondary: Listing[], limit: number): Listing[] {
  return dedupe([...primary, ...secondary], limit);
}

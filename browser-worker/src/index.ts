import http from "node:http";
import { chromium, type Browser, type Page } from "playwright";
import { mergeListings, parseAliExpress, parseAmazon, parseEbay } from "./parsers.js";
import type { Listing, Marketplace, ScrapeRequest, ScrapeResult } from "./types.js";

const port = Number(process.env.BROWSER_PORT ?? 3000);
const concurrency = Math.max(1, Number(process.env.BROWSER_CONCURRENCY ?? 4));
const navTimeout = Math.max(5_000, Number(process.env.BROWSER_NAV_TIMEOUT_MS ?? 30_000));
const httpTimeout = Math.max(2_000, Number(process.env.HTTP_FAST_TIMEOUT_MS ?? 12_000));
const httpFastPath = !/^(0|false|no)$/i.test(process.env.HTTP_FAST_PATH ?? "true");
const maxHTMLBytes = Math.max(1 << 20, Number(process.env.MAX_HTML_BYTES ?? 8 << 20));
const userAgent = process.env.SCRAPER_USER_AGENT || "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/140.0.0.0 Safari/537.36";

let browser: Browser;
let active = 0;
const waiters: Array<() => void> = [];

async function acquire(): Promise<void> {
  if (active < concurrency) {
    active++;
    return;
  }
  await new Promise<void>((resolve) => waiters.push(resolve));
  active++;
}

function release(): void {
  active--;
  waiters.shift()?.();
}

function searchURL(marketplace: Marketplace, query: string): string {
  if (marketplace === "amazon") return `https://www.amazon.com/s?k=${encodeURIComponent(query)}`;
  if (marketplace === "aliexpress") return `https://www.aliexpress.com/w/wholesale-${encodeURIComponent(query.replace(/\s+/g, "-"))}.html`;
  return `https://www.ebay.com/sch/i.html?_nkw=${encodeURIComponent(query)}`;
}

function parse(marketplace: Marketplace, html: string, limit: number): Listing[] {
  if (marketplace === "amazon") return parseAmazon(html, limit);
  if (marketplace === "aliexpress") return parseAliExpress(html, limit);
  return parseEbay(html, limit);
}

function looksChallenged(html: string): boolean {
  const sample = html.slice(0, 750_000).toLowerCase();
  return sample.includes("captcha")
    || sample.includes("robot check")
    || sample.includes("verify you are human")
    || sample.includes("unusual traffic")
    || sample.includes("access denied")
    || sample.includes("please slide to verify")
    || sample.includes("_____tmd_____/punish")
    || sample.includes("x5secdata")
    || sample.includes("nc_1_wrapper")
    || sample.includes("baxia");
}

async function directHTML(marketplace: Marketplace, query: string): Promise<string> {
  const response = await fetch(searchURL(marketplace, query), {
    redirect: "follow",
    signal: AbortSignal.timeout(httpTimeout),
    headers: {
      "user-agent": userAgent,
      "accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
      "accept-language": "en-US,en;q=0.9",
      "cache-control": "no-cache",
    },
  });
  if (!response.ok) throw new Error(`HTTP fast path status ${response.status}`);
  const contentLength = Number(response.headers.get("content-length") ?? 0);
  if (contentLength > maxHTMLBytes) throw new Error(`HTTP fast path response too large: ${contentLength}`);
  const html = await response.text();
  if (html.length > maxHTMLBytes) throw new Error(`HTTP fast path response exceeded ${maxHTMLBytes} bytes`);
  if (looksChallenged(html)) throw new Error("HTTP fast path received a challenge page");
  return html;
}

async function waitForMarketplace(page: Page, marketplace: Marketplace): Promise<void> {
  const selector = marketplace === "amazon"
    ? '[data-component-type="s-search-result"], div[data-asin]:not([data-asin=""])'
    : marketplace === "aliexpress"
      ? 'a[href*="/item/"]'
      : "li.s-item";
  await page.locator(selector).first().waitFor({ state: "attached", timeout: Math.min(navTimeout, 8_000) }).catch(() => undefined);
  if (marketplace === "aliexpress") {
    for (let i = 0; i < 2; i++) {
      await page.evaluate(() => window.scrollBy(0, Math.max(800, window.innerHeight)));
      await page.waitForTimeout(350);
    }
  }
}

async function browserHTML(marketplace: Marketplace, query: string): Promise<string> {
  await acquire();
  try {
    const context = await browser.newContext({
      locale: "en-US",
      userAgent,
      viewport: { width: 1365, height: 900 },
    });
    const page = await context.newPage();
    await page.route("**/*", async (route) => {
      const type = route.request().resourceType();
      if (type === "font" || type === "media") await route.abort();
      else await route.continue();
    });
    try {
      await page.goto(searchURL(marketplace, query), { waitUntil: "domcontentloaded", timeout: navTimeout });
      await waitForMarketplace(page, marketplace);
      const html = await page.content();
      if (looksChallenged(html)) throw new Error("browser received a challenge page");
      return html;
    } finally {
      await context.close();
    }
  } finally {
    release();
  }
}

function enough(listings: Listing[], limit: number): boolean {
  return listings.length >= Math.min(limit, Math.max(3, Math.ceil(limit * 0.5)));
}

async function scrape(input: ScrapeRequest): Promise<ScrapeResult> {
  const started = Date.now();
  const limit = Math.max(1, Math.min(100, input.limit ?? 20));
  const marketplace = input.marketplace;
  let direct: Listing[] = [];
  let directError: string | undefined;

  if (httpFastPath && (marketplace === "amazon" || marketplace === "aliexpress")) {
    try {
      direct = parse(marketplace, await directHTML(marketplace, input.query), limit);
      if (enough(direct, limit)) {
        return { listings: direct, strategy: "http", direct_count: direct.length, duration_ms: Date.now() - started };
      }
    } catch (error) {
      directError = error instanceof Error ? error.message : String(error);
    }
  }

  const browserListings = parse(marketplace, await browserHTML(marketplace, input.query), limit);
  const listings = mergeListings(browserListings, direct, limit);
  if (!listings.length) {
    throw new Error(`no listings extracted${directError ? `; fast path: ${directError}` : ""}`);
  }
  return {
    listings,
    strategy: direct.length ? "hybrid" : "browser",
    direct_count: direct.length,
    duration_ms: Date.now() - started,
  };
}

function json(res: http.ServerResponse, status: number, payload: unknown): void {
  const body = JSON.stringify(payload);
  res.writeHead(status, { "content-type": "application/json", "content-length": Buffer.byteLength(body) });
  res.end(body);
}

async function body(req: http.IncomingMessage): Promise<ScrapeRequest> {
  const chunks: Buffer[] = [];
  let total = 0;
  for await (const chunk of req) {
    const value = Buffer.from(chunk);
    total += value.length;
    if (total > 64 * 1024) throw new Error("request too large");
    chunks.push(value);
  }
  return JSON.parse(Buffer.concat(chunks).toString("utf8")) as ScrapeRequest;
}

browser = await chromium.launch({ headless: true });
const server = http.createServer(async (req, res) => {
  try {
    if (req.method === "GET" && req.url === "/healthz") {
      json(res, 200, { ok: true, active, concurrency, http_fast_path: httpFastPath, http_timeout_ms: httpTimeout });
      return;
    }
    if (req.method !== "POST" || req.url !== "/scrape") {
      json(res, 404, { error: "not found" });
      return;
    }
    const input = await body(req);
    if (!input.query || !["amazon", "aliexpress", "ebay"].includes(input.marketplace)) {
      json(res, 400, { error: "valid marketplace and query are required" });
      return;
    }
    const result = await scrape(input);
    console.log(JSON.stringify({ level: "info", message: "scrape complete", marketplace: input.marketplace, query: input.query, strategy: result.strategy, listings: result.listings.length, direct_count: result.direct_count, duration_ms: result.duration_ms }));
    json(res, 200, result);
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    console.warn(JSON.stringify({ level: "warn", message: "scrape failed", error: message }));
    json(res, 502, { error: message });
  }
});

server.listen(port, "0.0.0.0", () => console.log(JSON.stringify({ level: "info", message: "extraction worker listening", port, concurrency, http_fast_path: httpFastPath })));
for (const signal of ["SIGINT", "SIGTERM"] as const) {
  process.on(signal, async () => {
    server.close();
    await browser.close();
    process.exit(0);
  });
}

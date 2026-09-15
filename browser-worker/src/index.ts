import http from "node:http";
import { chromium, type Browser, type BrowserContext, type Page } from "playwright";
import { mergeListings, parseAliExpress, parseAmazon, parseEbay } from "./parsers.js";
import { PermitPool, ScrapeTelemetry } from "./runtime.js";
import type { Listing, Marketplace, ScrapeRequest, ScrapeResult } from "./types.js";

const port = Number(process.env.BROWSER_PORT ?? 3000);
const concurrency = Math.max(1, Number(process.env.BROWSER_CONCURRENCY ?? 4));
const navTimeout = Math.max(5_000, Number(process.env.BROWSER_NAV_TIMEOUT_MS ?? 30_000));
const httpTimeout = Math.max(2_000, Number(process.env.HTTP_FAST_TIMEOUT_MS ?? 12_000));
const httpFastPath = !/^(0|false|no)$/i.test(process.env.HTTP_FAST_PATH ?? "true");
const blockImages = !/^(0|false|no)$/i.test(process.env.BROWSER_BLOCK_IMAGES ?? "true");
const maxHTMLBytes = Math.max(1 << 20, Number(process.env.MAX_HTML_BYTES ?? 8 << 20));
const userAgent = process.env.SCRAPER_USER_AGENT || "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/140.0.0.0 Safari/537.36";
const requestJitterMs = Math.max(0, Number(process.env.REQUEST_JITTER_MS ?? 100));
const marketplaceMinIntervalMs: Record<Marketplace, number> = {
  amazon: Math.max(0, Number(process.env.AMAZON_MIN_INTERVAL_MS ?? 350)),
  aliexpress: Math.max(0, Number(process.env.ALIEXPRESS_MIN_INTERVAL_MS ?? 300)),
  ebay: Math.max(0, Number(process.env.EBAY_MIN_INTERVAL_MS ?? 200)),
};
const marketplaceConcurrency: Record<Marketplace, number> = {
  amazon: Math.max(1, Number(process.env.AMAZON_MAX_CONCURRENT ?? 4)),
  aliexpress: Math.max(1, Number(process.env.ALIEXPRESS_MAX_CONCURRENT ?? 2)),
  ebay: Math.max(1, Number(process.env.EBAY_MAX_CONCURRENT ?? 3)),
};

let browser: Browser;
const browserPool = new PermitPool(concurrency);
const marketplacePools: Record<Marketplace, PermitPool> = {
  amazon: new PermitPool(marketplaceConcurrency.amazon),
  aliexpress: new PermitPool(marketplaceConcurrency.aliexpress),
  ebay: new PermitPool(marketplaceConcurrency.ebay),
};
const telemetry = new ScrapeTelemetry();
const nextRequestAt = new Map<Marketplace, number>();
const pacingTails = new Map<Marketplace, Promise<void>>();

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function pace(marketplace: Marketplace): Promise<void> {
  const previous = pacingTails.get(marketplace) ?? Promise.resolve();
  let releaseTurn!: () => void;
  const turn = new Promise<void>((resolve) => { releaseTurn = resolve; });
  const tail = previous.then(() => turn);
  pacingTails.set(marketplace, tail);

  await previous;
  try {
    const wait = Math.max(0, (nextRequestAt.get(marketplace) ?? 0) - Date.now());
    if (wait > 0) await delay(wait);
    const jitter = requestJitterMs > 0 ? Math.floor(Math.random() * (requestJitterMs + 1)) : 0;
    nextRequestAt.set(marketplace, Date.now() + marketplaceMinIntervalMs[marketplace] + jitter);
  } finally {
    releaseTurn();
    if (pacingTails.get(marketplace) === tail) {
      void tail.finally(() => {
        if (pacingTails.get(marketplace) === tail) pacingTails.delete(marketplace);
      });
    }
  }
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

async function readBoundedHTML(response: Response): Promise<string> {
  const contentLength = Number(response.headers.get("content-length") ?? 0);
  if (contentLength > maxHTMLBytes) throw new Error(`HTTP fast path response too large: ${contentLength}`);
  if (!response.body) return response.text();

  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let total = 0;
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      if (!value) continue;
      total += value.byteLength;
      if (total > maxHTMLBytes) {
        await reader.cancel("response exceeded configured HTML limit");
        throw new Error(`HTTP fast path response exceeded ${maxHTMLBytes} bytes`);
      }
      chunks.push(value);
    }
  } finally {
    reader.releaseLock();
  }
  return Buffer.concat(chunks.map((chunk) => Buffer.from(chunk))).toString("utf8");
}

function timeoutSignal(parent: AbortSignal | undefined, timeoutMs: number): { signal: AbortSignal; dispose: () => void } {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(new Error(`request timed out after ${timeoutMs}ms`)), timeoutMs);
  const onAbort = () => controller.abort(parent?.reason ?? new Error("request aborted"));

  if (parent?.aborted) onAbort();
  else parent?.addEventListener("abort", onAbort, { once: true });

  return {
    signal: controller.signal,
    dispose: () => {
      clearTimeout(timeout);
      parent?.removeEventListener("abort", onAbort);
    },
  };
}

async function directHTML(marketplace: Marketplace, query: string, signal?: AbortSignal): Promise<string> {
  await pace(marketplace);
  await marketplacePools[marketplace].acquire(signal);
  const bounded = timeoutSignal(signal, httpTimeout);
  try {
    const response = await fetch(searchURL(marketplace, query), {
      redirect: "follow",
      signal: bounded.signal,
      headers: {
        "user-agent": userAgent,
        "accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
        "accept-language": "en-US,en;q=0.9",
        "cache-control": "no-cache",
      },
    });
    if (!response.ok) throw new Error(`HTTP fast path status ${response.status}`);
    const html = await readBoundedHTML(response);
    if (looksChallenged(html)) throw new Error("HTTP fast path received a challenge page");
    return html;
  } finally {
    bounded.dispose();
    marketplacePools[marketplace].release();
  }
}

async function waitForMarketplace(page: Page, marketplace: Marketplace): Promise<void> {
  const selector = marketplace === "amazon"
    ? '[data-component-type="s-search-result"], div[data-asin]:not([data-asin=""])'
    : marketplace === "aliexpress"
      ? 'a[href*="/item/"]'
      : "li.s-item";

  await page.locator(selector).first().waitFor({ state: "attached", timeout: Math.min(navTimeout, 8_000) }).catch(() => undefined);

  if (marketplace === "aliexpress") {
    await page.waitForTimeout(800);
    for (let i = 0; i < 3; i++) {
      await page.evaluate(() => window.scrollBy(0, Math.max(900, window.innerHeight)));
      await page.waitForTimeout(900);
    }
  }
}

async function browserHTML(marketplace: Marketplace, query: string, signal?: AbortSignal): Promise<string> {
  await pace(marketplace);
  await marketplacePools[marketplace].acquire(signal);
  let browserPermit = false;
  let context: BrowserContext | undefined;
  try {
    await browserPool.acquire(signal);
    browserPermit = true;
    if (signal?.aborted) throw signal.reason instanceof Error ? signal.reason : new Error("request aborted");

    context = await browser.newContext({
      locale: "en-US",
      userAgent,
      viewport: { width: 1365, height: 900 },
      serviceWorkers: "block",
    });
    const onAbort = () => void context?.close().catch(() => undefined);
    signal?.addEventListener("abort", onAbort, { once: true });

    try {
      const page = await context.newPage();
      await page.route("**/*", async (route) => {
        const type = route.request().resourceType();
        if (type === "font" || type === "media" || (blockImages && type === "image")) await route.abort();
        else await route.continue();
      });

      const response = await page.goto(searchURL(marketplace, query), { waitUntil: "domcontentloaded", timeout: navTimeout });
      if (response && response.status() >= 400) throw new Error(`browser navigation status ${response.status()}`);
      await waitForMarketplace(page, marketplace);
      const html = await page.content();
      if (Buffer.byteLength(html, "utf8") > maxHTMLBytes) throw new Error(`browser HTML exceeded ${maxHTMLBytes} bytes`);
      if (looksChallenged(html)) throw new Error("browser received a challenge page");
      return html;
    } finally {
      signal?.removeEventListener("abort", onAbort);
      await context.close().catch(() => undefined);
    }
  } finally {
    if (browserPermit) browserPool.release();
    marketplacePools[marketplace].release();
  }
}

function enough(listings: Listing[], limit: number): boolean {
  return listings.length >= Math.min(limit, Math.max(3, Math.ceil(limit * 0.5)));
}

async function scrape(input: ScrapeRequest, signal?: AbortSignal): Promise<ScrapeResult> {
  const started = Date.now();
  const limit = input.limit ?? 20;
  const marketplace = input.marketplace;
  let direct: Listing[] = [];
  let directError: string | undefined;

  if (httpFastPath && (marketplace === "amazon" || marketplace === "aliexpress")) {
    try {
      direct = parse(marketplace, await directHTML(marketplace, input.query, signal), limit);
      if (enough(direct, limit)) {
        return { listings: direct, strategy: "http", direct_count: direct.length, duration_ms: Date.now() - started };
      }
    } catch (error) {
      if (signal?.aborted) throw error;
      directError = error instanceof Error ? error.message : String(error);
    }
  }

  const browserListings = parse(marketplace, await browserHTML(marketplace, input.query, signal), limit);
  const listings = mergeListings(browserListings, direct, limit);
  if (!listings.length) throw new Error(`no listings extracted${directError ? `; fast path: ${directError}` : ""}`);
  return {
    listings,
    strategy: direct.length ? "hybrid" : "browser",
    direct_count: direct.length,
    duration_ms: Date.now() - started,
  };
}

function json(res: http.ServerResponse, status: number, payload: unknown): void {
  if (res.destroyed || res.writableEnded) return;
  const responseBody = JSON.stringify(payload);
  res.writeHead(status, { "content-type": "application/json", "content-length": Buffer.byteLength(responseBody) });
  res.end(responseBody);
}

async function body(req: http.IncomingMessage): Promise<unknown> {
  const chunks: Buffer[] = [];
  let total = 0;
  for await (const chunk of req) {
    const value = Buffer.from(chunk);
    total += value.length;
    if (total > 64 * 1024) throw new Error("request too large");
    chunks.push(value);
  }
  if (!chunks.length) throw new Error("request body is required");
  return JSON.parse(Buffer.concat(chunks).toString("utf8")) as unknown;
}

function validateScrapeRequest(value: unknown): ScrapeRequest | undefined {
  if (!value || typeof value !== "object" || Array.isArray(value)) return undefined;
  const input = value as Partial<ScrapeRequest>;
  if (!input.marketplace || !["amazon", "aliexpress", "ebay"].includes(input.marketplace)) return undefined;
  if (typeof input.query !== "string") return undefined;
  input.query = input.query.trim();
  if (!input.query || input.query.length > 500) return undefined;
  if (input.limit !== undefined && (!Number.isInteger(input.limit) || input.limit < 1 || input.limit > 100)) return undefined;
  return input as ScrapeRequest;
}

browser = await chromium.launch({ headless: true });
const server = http.createServer(async (req, res) => {
  const path = req.url?.split("?", 1)[0] ?? "/";
  if (req.method === "GET" && (path === "/healthz" || path === "/readyz")) {
    json(res, 200, {
      ok: browser.isConnected(),
      browser: browserPool.snapshot(),
      marketplaces: Object.fromEntries((Object.entries(marketplacePools) as Array<[Marketplace, PermitPool]>).map(([marketplace, pool]) => [marketplace, pool.snapshot()])),
      http_fast_path: httpFastPath,
      block_images: blockImages,
      http_timeout_ms: httpTimeout,
      max_html_bytes: maxHTMLBytes,
      request_jitter_ms: requestJitterMs,
      marketplace_min_interval_ms: marketplaceMinIntervalMs,
      telemetry: telemetry.snapshot(),
    });
    return;
  }
  if (req.method !== "POST" || path !== "/scrape") {
    json(res, 404, { error: "not found" });
    return;
  }

  let input: ScrapeRequest | undefined;
  try {
    input = validateScrapeRequest(await body(req));
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    json(res, 400, { error: message === "request too large" ? message : "invalid JSON request" });
    return;
  }
  if (!input) {
    json(res, 400, { error: "marketplace must be amazon, aliexpress, or ebay; query must be 1..500 characters; limit must be 1..100" });
    return;
  }

  const started = Date.now();
  const controller = new AbortController();
  const onClose = () => {
    if (!res.writableEnded) controller.abort(new Error("client disconnected"));
  };
  res.once("close", onClose);
  telemetry.request(input.marketplace);

  try {
    const result = await scrape(input, controller.signal);
    telemetry.success(input.marketplace, result);
    console.log(JSON.stringify({
      level: "info",
      message: "scrape complete",
      marketplace: input.marketplace,
      query: input.query,
      strategy: result.strategy,
      listings: result.listings.length,
      direct_count: result.direct_count,
      duration_ms: result.duration_ms,
    }));
    json(res, 200, result);
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    telemetry.failure(input.marketplace, error, Date.now() - started);
    if (!controller.signal.aborted) {
      console.warn(JSON.stringify({ level: "warn", message: "scrape failed", marketplace: input.marketplace, query: input.query, error: message }));
      json(res, 502, { error: message });
    }
  } finally {
    res.removeListener("close", onClose);
  }
});

server.listen(port, "0.0.0.0", () => console.log(JSON.stringify({
  level: "info",
  message: "extraction worker listening",
  port,
  concurrency,
  http_fast_path: httpFastPath,
  block_images: blockImages,
  marketplace_max_concurrent: marketplaceConcurrency,
})));

let shuttingDown = false;
for (const signal of ["SIGINT", "SIGTERM"] as const) {
  process.on(signal, async () => {
    if (shuttingDown) return;
    shuttingDown = true;
    await new Promise<void>((resolve) => server.close(() => resolve()));
    await browser.close();
  });
}

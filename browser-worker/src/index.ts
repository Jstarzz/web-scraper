import http from "node:http";
import { chromium, type Browser, type Page } from "playwright";

interface ScrapeRequest { marketplace: "amazon" | "aliexpress" | "ebay"; query: string; limit?: number }
interface Listing { marketplace: string; external_id: string; title: string; url: string; image_url?: string; seller?: string; price_minor?: number; shipping_minor?: number; currency?: string; available?: boolean; rating?: number; review_count?: number }

const port = Number(process.env.BROWSER_PORT ?? 3000);
const concurrency = Math.max(1, Number(process.env.BROWSER_CONCURRENCY ?? 4));
const navTimeout = Math.max(5000, Number(process.env.BROWSER_NAV_TIMEOUT_MS ?? 30000));
let browser: Browser;
let active = 0;
const waiters: Array<() => void> = [];

async function acquire() { if (active < concurrency) { active++; return; } await new Promise<void>(resolve => waiters.push(resolve)); active++; }
function release() { active--; waiters.shift()?.(); }

function money(text?: string | null): { minor?: number; currency?: string } {
  if (!text) return {};
  const currency = text.includes("£") ? "GBP" : text.includes("€") ? "EUR" : text.includes("CA$") ? "CAD" : text.includes("AU$") ? "AUD" : text.includes("$") ? "USD" : undefined;
  const match = text.replace(/,/g, "").match(/(?:US|CA|AU)?\$?\s*([0-9]+(?:\.[0-9]{1,2})?)/);
  if (!match) return {};
  return { minor: Math.round(Number(match[1]) * 100), currency };
}
function intFrom(text?: string | null): number | undefined { if (!text) return undefined; const m = text.replace(/,/g, "").match(/([0-9]+)/); return m ? Number(m[1]) : undefined; }
function ratingFrom(text?: string | null): number | undefined { if (!text) return undefined; const m=text.match(/([0-5](?:\.[0-9]+)?)/); return m ? Number(m[1]) : undefined; }
function absolute(base: string, href: string) { try { return new URL(href, base).toString(); } catch { return href; } }

async function amazon(page: Page, query: string, limit: number): Promise<Listing[]> {
  const url = `https://www.amazon.com/s?k=${encodeURIComponent(query)}`;
  await page.goto(url, { waitUntil: "domcontentloaded", timeout: navTimeout });
  await page.waitForTimeout(700);
  return page.locator('div[data-component-type="s-search-result"]').evaluateAll((nodes, max) => nodes.slice(0, Number(max)).map((node) => {
    const el=node as HTMLElement; const asin=el.getAttribute("data-asin") ?? "";
    const link=el.querySelector<HTMLAnchorElement>("h2 a"); const title=el.querySelector<HTMLElement>("h2")?.innerText.trim() ?? "";
    const price=el.querySelector<HTMLElement>(".a-price .a-offscreen")?.innerText ?? "";
    const image=el.querySelector<HTMLImageElement>("img.s-image")?.src ?? "";
    const rating=el.querySelector<HTMLElement>(".a-icon-alt")?.innerText ?? "";
    const reviews=el.querySelector<HTMLElement>('a[href*="#customerReviews"] span, .s-link-style .s-underline-text')?.textContent ?? "";
    return { asin, href:link?.href ?? "", title, price, image, rating, reviews };
  }), limit).then(rows => rows.filter(x=>x.asin&&x.href&&x.title).map(x=>{const p=money(x.price);return {marketplace:"amazon",external_id:x.asin,title:x.title,url:x.href,image_url:x.image,price_minor:p.minor,currency:p.currency,available:true,rating:ratingFrom(x.rating),review_count:intFrom(x.reviews)}}));
}

async function ebay(page: Page, query: string, limit: number): Promise<Listing[]> {
  const base=`https://www.ebay.com/sch/i.html?_nkw=${encodeURIComponent(query)}`;
  await page.goto(base,{waitUntil:"domcontentloaded",timeout:navTimeout}); await page.waitForTimeout(500);
  const rows=await page.locator("li.s-item").evaluateAll((nodes,max)=>nodes.slice(0,Number(max)*2).map(node=>{const el=node as HTMLElement;const a=el.querySelector<HTMLAnchorElement>("a.s-item__link");return {href:a?.href??"",title:el.querySelector<HTMLElement>(".s-item__title")?.innerText.trim()??"",price:el.querySelector<HTMLElement>(".s-item__price")?.innerText??"",shipping:el.querySelector<HTMLElement>(".s-item__shipping, .s-item__logisticsCost")?.innerText??"",image:el.querySelector<HTMLImageElement>("img")?.src??"",seller:el.querySelector<HTMLElement>(".s-item__seller-info-text, .s-item__seller-info")?.innerText??""}}),limit);
  const out:Listing[]=[]; for(const x of rows){const id=x.href.match(/\/itm\/(?:[^/]+\/)?(\d+)/)?.[1];if(!id||!x.title||x.title.toLowerCase()==="shop on ebay")continue;const p=money(x.price),s=money(x.shipping);out.push({marketplace:"ebay",external_id:id,title:x.title,url:x.href,image_url:x.image,seller:x.seller,price_minor:p.minor,shipping_minor:s.minor,currency:p.currency,available:true});if(out.length>=limit)break} return out;
}

async function aliexpress(page: Page, query: string, limit: number): Promise<Listing[]> {
  const base=`https://www.aliexpress.com/w/wholesale-${encodeURIComponent(query.replace(/\s+/g,"-"))}.html`;
  await page.goto(base,{waitUntil:"domcontentloaded",timeout:navTimeout}); await page.waitForTimeout(1200);
  const rows=await page.locator('a[href*="/item/"]').evaluateAll((links,max)=>{const seen=new Set<string>();const out:any[]=[];for(const raw of links){const a=raw as HTMLAnchorElement;const m=a.href.match(/\/item\/(\d+)\.html/);if(!m||seen.has(m[1]))continue;seen.add(m[1]);const card=(a.closest('[class*="search-item"], [class*="product"], [class*="card"]')??a.parentElement) as HTMLElement|null;const text=(card?.innerText??a.innerText??"").trim();const img=card?.querySelector<HTMLImageElement>("img")?.src??"";out.push({id:m[1],href:a.href,text,img,title:(a.getAttribute("title")??card?.querySelector<HTMLElement>('[class*="title"]')?.innerText??a.innerText??"").trim()});if(out.length>=Number(max))break}return out},limit);
  return rows.map(x=>{const p=money(x.text);const title=x.title||x.text.split("\n")[0]||`AliExpress item ${x.id}`;return {marketplace:"aliexpress",external_id:x.id,title,url:absolute("https://www.aliexpress.com",x.href),image_url:x.img,price_minor:p.minor,currency:p.currency,available:true}}).filter(x=>x.url&&x.title);
}

async function scrape(input: ScrapeRequest): Promise<Listing[]> {
  const limit=Math.max(1,Math.min(100,input.limit??20));
  await acquire();
  try {
    const context=await browser.newContext({locale:"en-US"}); const page=await context.newPage();
    try {
      if(input.marketplace==="amazon") return await amazon(page,input.query,limit);
      if(input.marketplace==="ebay") return await ebay(page,input.query,limit);
      if(input.marketplace==="aliexpress") return await aliexpress(page,input.query,limit);
      throw new Error("unsupported marketplace");
    } finally { await context.close(); }
  } finally { release(); }
}

function json(res:http.ServerResponse,status:number,payload:unknown){const body=JSON.stringify(payload);res.writeHead(status,{"content-type":"application/json","content-length":Buffer.byteLength(body)});res.end(body)}
async function body(req:http.IncomingMessage){const chunks:Buffer[]=[];let total=0;for await(const chunk of req){const b=Buffer.from(chunk);total+=b.length;if(total>64*1024)throw new Error("request too large");chunks.push(b)}return JSON.parse(Buffer.concat(chunks).toString("utf8")) as ScrapeRequest}

browser=await chromium.launch({headless:true});
const server=http.createServer(async(req,res)=>{try{if(req.method==="GET"&&req.url==="/healthz"){json(res,200,{ok:true,active,concurrency});return}if(req.method!=="POST"||req.url!=="/scrape"){json(res,404,{error:"not found"});return}const input=await body(req);if(!input.query||!input.marketplace){json(res,400,{error:"marketplace and query are required"});return}const listings=await scrape(input);json(res,200,{listings})}catch(error){json(res,502,{error:error instanceof Error?error.message:String(error)})}});
server.listen(port,"0.0.0.0",()=>console.log(JSON.stringify({level:"info",message:"browser worker listening",port,concurrency})));
for(const signal of ["SIGINT","SIGTERM"] as const){process.on(signal,async()=>{server.close();await browser.close();process.exit(0)})}

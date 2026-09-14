import assert from "node:assert/strict";
import test from "node:test";
import { parseAliExpress, parseAmazon } from "./parsers.js";

test("parses Amazon search result cards", () => {
  const html = `
  <div data-component-type="s-search-result" data-asin="B0TEST123">
    <h2><a href="/dp/B0TEST123?ref=abc"><span>Example GPU 16GB</span></a></h2>
    <span class="a-price"><span class="a-offscreen">$499.99</span></span>
    <span aria-label="4.7 out of 5 stars"></span>
    <span aria-label="1.2K ratings"></span>
    <img class="s-image" src="https://images.example/gpu.jpg" />
  </div>`;
  const [item] = parseAmazon(html, 10);
  assert.equal(item.external_id, "B0TEST123");
  assert.equal(item.title, "Example GPU 16GB");
  assert.equal(item.price_minor, 49_999);
  assert.equal(item.currency, "USD");
  assert.equal(item.rating, 4.7);
  assert.equal(item.review_count, 1_200);
});

test("uses Amazon data-asin fallback when component marker changes", () => {
  const html = `<div data-asin="B0FALLBACK"><a href="/dp/B0FALLBACK"><span class="a-text-normal">Fallback Item</span></a><span class="a-price"><span class="a-offscreen">$10.00</span></span></div>`;
  const [item] = parseAmazon(html, 10);
  assert.equal(item.external_id, "B0FALLBACK");
  assert.equal(item.price_minor, 1_000);
});

test("prefers AliExpress runParams search data", () => {
  const runParams = {
    mods: {
      itemList: {
        content: [{
          productId: "1005009999999999",
          title: { displayTitle: "ESP32 Touch Display" },
          prices: { salePrice: { formattedPrice: "US $19.95", currencyCode: "USD" } },
          trade: { tradeDesc: "2K+ sold" },
          store: { storeName: "Embedded Store" },
          evaluation: { starRating: "4.8", evaluationCount: "730" },
          image: { imgUrl: "//ae.example/display.jpg" },
          productDetailUrl: "//www.aliexpress.com/item/1005009999999999.html?spm=abc"
        }]
      }
    }
  };
  const html = `<script>window.runParams = ${JSON.stringify(runParams)};</script>`;
  const [item] = parseAliExpress(html, 10);
  assert.equal(item.external_id, "1005009999999999");
  assert.equal(item.title, "ESP32 Touch Display");
  assert.equal(item.price_minor, 1_995);
  assert.equal(item.currency, "USD");
  assert.equal(item.seller, "Embedded Store");
  assert.equal(item.rating, 4.8);
  assert.equal(item.review_count, 730);
  assert.equal(item.image_url, "https://ae.example/display.jpg");
});

test("parses and deduplicates AliExpress DOM cards", () => {
  const html = `
  <div class="search-card-item">
    <a href="https://www.aliexpress.com/item/1005001234567890.html?spm=test" title="USB C Development Board">
      <img alt="USB C Development Board" src="https://ae.example/a.jpg" />
    </a>
    <div class="price-area">US $12.34</div>
    <div class="shipping">Free shipping</div>
    <div class="rating">4.9</div>
    <div class="store-name">Example Store</div>
  </div>
  <a href="https://www.aliexpress.com/item/1005001234567890.html">duplicate</a>`;
  const items = parseAliExpress(html, 10);
  assert.equal(items.length, 1);
  assert.equal(items[0].external_id, "1005001234567890");
  assert.equal(items[0].title, "USB C Development Board");
  assert.equal(items[0].price_minor, 1_234);
  assert.equal(items[0].shipping_minor, 0);
  assert.equal(items[0].currency, "USD");
  assert.equal(items[0].rating, 4.9);
  assert.equal(items[0].seller, "Example Store");
});

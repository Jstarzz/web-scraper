import { test } from "node:test";
import assert from "node:assert/strict";
import { cookieHeader } from "./region.js";

test("aliexpress is pinned to USD and the US region (regression: XCD prices)", () => {
  const header = cookieHeader("aliexpress");
  assert.match(header, /c_tp=USD/);
  assert.match(header, /region=US/);
});

test("amazon is pinned to USD", () => {
  assert.equal(cookieHeader("amazon"), "i18n-prefs=USD");
});

test("ebay sends no region cookie", () => {
  assert.equal(cookieHeader("ebay"), "");
});

import assert from "node:assert/strict";
import test from "node:test";
import { PermitPool, ScrapeTelemetry } from "./runtime.js";

test("permit pool transfers permits to queued waiters without exceeding the limit", async () => {
  const pool = new PermitPool(1);
  await pool.acquire();

  let acquired = false;
  const queued = pool.acquire().then(() => { acquired = true; });
  await new Promise((resolve) => setImmediate(resolve));

  assert.deepEqual(pool.snapshot(), { limit: 1, active: 1, waiting: 1 });
  assert.equal(acquired, false);

  pool.release();
  await queued;
  assert.equal(acquired, true);
  assert.deepEqual(pool.snapshot(), { limit: 1, active: 1, waiting: 0 });

  pool.release();
  assert.deepEqual(pool.snapshot(), { limit: 1, active: 0, waiting: 0 });
});

test("aborted queued permits are removed without leaking capacity", async () => {
  const pool = new PermitPool(1);
  await pool.acquire();

  const controller = new AbortController();
  const queued = pool.acquire(controller.signal);
  controller.abort(new Error("cancelled scrape"));

  await assert.rejects(queued, /cancelled scrape/);
  assert.deepEqual(pool.snapshot(), { limit: 1, active: 1, waiting: 0 });

  pool.release();
  assert.deepEqual(pool.snapshot(), { limit: 1, active: 0, waiting: 0 });
});

test("scrape telemetry tracks successes, failures, challenge failures, and strategies", () => {
  const telemetry = new ScrapeTelemetry();
  telemetry.request("amazon");
  telemetry.success("amazon", {
    listings: [{ marketplace: "amazon", external_id: "B0TEST", title: "Test", url: "https://www.amazon.com/dp/B0TEST" }],
    strategy: "http",
    direct_count: 1,
    duration_ms: 120,
  });

  telemetry.request("aliexpress");
  telemetry.failure("aliexpress", new Error("challenge page received"), 250);

  const snapshot = telemetry.snapshot() as {
    marketplaces: Record<string, {
      requests: number;
      successes: number;
      failures: number;
      challengeFailures: number;
      strategies: Record<string, number>;
    }>;
  };

  assert.equal(snapshot.marketplaces.amazon.requests, 1);
  assert.equal(snapshot.marketplaces.amazon.successes, 1);
  assert.equal(snapshot.marketplaces.amazon.strategies.http, 1);
  assert.equal(snapshot.marketplaces.aliexpress.failures, 1);
  assert.equal(snapshot.marketplaces.aliexpress.challengeFailures, 1);
});

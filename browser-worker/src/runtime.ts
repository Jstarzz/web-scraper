import type { Marketplace, ScrapeResult } from "./types.js";

type Waiter = {
  resolve: () => void;
  reject: (error: Error) => void;
  signal?: AbortSignal;
  onAbort?: () => void;
};

function abortError(signal?: AbortSignal): Error {
  const reason = signal?.reason;
  return reason instanceof Error ? reason : new Error("request aborted");
}

export class PermitPool {
  readonly limit: number;
  #active = 0;
  #waiters: Waiter[] = [];

  constructor(limit: number) {
    if (!Number.isInteger(limit) || limit < 1) throw new Error("permit pool limit must be a positive integer");
    this.limit = limit;
  }

  get active(): number {
    return this.#active;
  }

  get waiting(): number {
    return this.#waiters.length;
  }

  async acquire(signal?: AbortSignal): Promise<void> {
    if (signal?.aborted) throw abortError(signal);
    if (this.#active < this.limit) {
      this.#active++;
      return;
    }

    await new Promise<void>((resolve, reject) => {
      const waiter: Waiter = {
        resolve,
        reject: (error) => reject(error),
        signal,
      };
      if (signal) {
        waiter.onAbort = () => {
          const index = this.#waiters.indexOf(waiter);
          if (index >= 0) this.#waiters.splice(index, 1);
          reject(abortError(signal));
        };
        signal.addEventListener("abort", waiter.onAbort, { once: true });
      }
      this.#waiters.push(waiter);
    });
    // release() transfers an existing permit directly to this waiter, so the
    // active count intentionally does not change here.
  }

  release(): void {
    while (this.#waiters.length) {
      const next = this.#waiters.shift()!;
      if (next.onAbort && next.signal) next.signal.removeEventListener("abort", next.onAbort);
      if (next.signal?.aborted) {
        next.reject(abortError(next.signal));
        continue;
      }
      next.resolve();
      return;
    }
    if (this.#active <= 0) throw new Error("permit pool released without an active permit");
    this.#active--;
  }

  snapshot(): { limit: number; active: number; waiting: number } {
    return { limit: this.limit, active: this.#active, waiting: this.#waiters.length };
  }
}

type StrategyCounters = Record<ScrapeResult["strategy"], number>;

type MarketplaceCounters = {
  requests: number;
  successes: number;
  failures: number;
  challengeFailures: number;
  listings: number;
  totalDurationMs: number;
  lastDurationMs: number | null;
  lastCompletedAt: string | null;
  lastError: string | null;
  strategies: StrategyCounters;
};

function emptyCounters(): MarketplaceCounters {
  return {
    requests: 0,
    successes: 0,
    failures: 0,
    challengeFailures: 0,
    listings: 0,
    totalDurationMs: 0,
    lastDurationMs: null,
    lastCompletedAt: null,
    lastError: null,
    strategies: { http: 0, browser: 0, hybrid: 0 },
  };
}

function isChallenge(message: string): boolean {
  return /challenge|captcha|verify you are human|robot check|unusual traffic|access denied|punish|x5sec|baxia/i.test(message);
}

export class ScrapeTelemetry {
  #startedAt = new Date();
  #counters: Record<Marketplace, MarketplaceCounters> = {
    amazon: emptyCounters(),
    aliexpress: emptyCounters(),
    ebay: emptyCounters(),
  };

  request(marketplace: Marketplace): void {
    this.#counters[marketplace].requests++;
  }

  success(marketplace: Marketplace, result: ScrapeResult): void {
    const value = this.#counters[marketplace];
    value.successes++;
    value.listings += result.listings.length;
    value.totalDurationMs += result.duration_ms;
    value.lastDurationMs = result.duration_ms;
    value.lastCompletedAt = new Date().toISOString();
    value.lastError = null;
    value.strategies[result.strategy]++;
  }

  failure(marketplace: Marketplace, error: unknown, durationMs: number): void {
    const value = this.#counters[marketplace];
    const message = error instanceof Error ? error.message : String(error);
    value.failures++;
    if (isChallenge(message)) value.challengeFailures++;
    value.totalDurationMs += durationMs;
    value.lastDurationMs = durationMs;
    value.lastCompletedAt = new Date().toISOString();
    value.lastError = message.slice(0, 500);
  }

  snapshot(): Record<string, unknown> {
    const marketplaces = Object.fromEntries((Object.entries(this.#counters) as Array<[Marketplace, MarketplaceCounters]>).map(([marketplace, value]) => {
      const completed = value.successes + value.failures;
      return [marketplace, {
        ...value,
        successRate: completed ? value.successes / completed : null,
        averageDurationMs: completed ? Math.round(value.totalDurationMs / completed) : null,
        averageListingsPerSuccess: value.successes ? value.listings / value.successes : null,
      }];
    }));

    return {
      startedAt: this.#startedAt.toISOString(),
      uptimeSeconds: Math.floor((Date.now() - this.#startedAt.getTime()) / 1000),
      marketplaces,
    };
  }
}

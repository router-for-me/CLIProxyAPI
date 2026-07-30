import { render, screen, waitFor } from "@testing-library/react";
import { describe, it, expect, vi, beforeAll, afterEach } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import UsageRequestsTable from "../UsageRequestsTable";

let page = 0;
const MOCK_ITEMS = Array.from({ length: 50 }, (_, i) => ({
  id: i,
  request_id: `r${i}`,
  timestamp: "2026-01-01T00:00:00Z",
  account_hash: "acc",
  model: `m${i % 5}`,
  endpoint: "e",
  provider: "p",
  total_tokens: 100,
  input_tokens: 60,
  output_tokens: 40,
  reasoning_tokens: 0,
  cached_tokens: 0,
  cache_read_tokens: 0,
  cache_creation_tokens: 0,
  latency_ms: 100,
  ttft_ms: 50,
  failed: 0,
  fail_status: 0,
  alias: null,
  estimated_cost: 0.001,
}));

beforeAll(() => {
  // Mock IntersectionObserver for infinite scroll
  class MockIntersectionObserver {
    readonly root: Element | null = null;
    readonly rootMargin: string = "";
    readonly thresholds: ReadonlyArray<number> = [];
    private callback: IntersectionObserverCallback;
    private elements: Element[] = [];

    constructor(callback: IntersectionObserverCallback) {
      this.callback = callback;
    }
    observe(element: Element) {
      this.elements.push(element);
      // Immediately trigger as intersecting to simulate "in viewport"
      this.callback(
        [{ isIntersecting: true, target: element } as IntersectionObserverEntry],
        this as unknown as IntersectionObserver,
      );
    }
    unobserve() {}
    disconnect() {}
    takeRecords() { return []; }
  }
  vi.stubGlobal("IntersectionObserver", MockIntersectionObserver);
});

afterEach(() => {
  page = 0;
});

function renderWithQuery() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <UsageRequestsTable />
    </QueryClientProvider>,
  );
}

describe("UsageRequestsTable", () => {
  it("renders first page of 50 rows", async () => {
    global.fetch = vi.fn(async () => {
      page++;
      return {
        ok: true,
        status: 200,
        json: async () => ({
          requests: MOCK_ITEMS,
          next_cursor: "50",
          range: "24h",
          models_filter: [],
          accounts_filter: [],
        }),
      };
    }) as unknown as typeof fetch;

    renderWithQuery();
    // Header row + 50 data rows = 51
    await waitFor(() => {
      expect(screen.getAllByRole("row").length).toBe(51);
    });
  });

  it("loads next page via IntersectionObserver", async () => {
    let callCount = 0;
    global.fetch = vi.fn(async () => {
      callCount++;
      return {
        ok: true,
        status: 200,
        json: async () => ({
          requests: MOCK_ITEMS.slice(0, callCount === 1 ? 50 : 30),
          next_cursor: callCount < 3 ? String(callCount * 50) : null,
          range: "24h",
          models_filter: [],
          accounts_filter: [],
        }),
      };
    }) as unknown as typeof fetch;

    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <UsageRequestsTable />
      </QueryClientProvider>,
    );

    // Wait for first page (50 rows + header = 51)
    await waitFor(() => {
      expect(screen.getAllByRole("row").length).toBeGreaterThanOrEqual(51);
    });
    // Wait for second page to load (50 + 30 = 80 rows + header = 81)
    await waitFor(() => {
      expect(screen.getAllByRole("row").length).toBeGreaterThanOrEqual(81);
    });
  });

  it("shows empty state when no data", async () => {
    global.fetch = vi.fn(async () => ({
      ok: true,
      status: 200,
      json: async () => ({
        requests: [],
        next_cursor: null,
        range: "24h",
        models_filter: [],
        accounts_filter: [],
      }),
    })) as unknown as typeof fetch;

    renderWithQuery();
    await waitFor(() => {
      expect(screen.getByText(/暂无数据/)).toBeInTheDocument();
    });
  });

  it("shows summary bar with total count", async () => {
    global.fetch = vi.fn(async () => {
      page++;
      return {
        ok: true,
        status: 200,
        json: async () => ({
          requests: MOCK_ITEMS.slice(0, 25),
          next_cursor: page < 2 ? "25" : null,
          range: "24h",
          models_filter: [],
          accounts_filter: [],
        }),
      };
    }) as unknown as typeof fetch;

    renderWithQuery();
    await waitFor(() => {
      expect(screen.getByText(/显示 25 条结果/)).toBeInTheDocument();
    });
  });

  it("renders status badge with success/fail", async () => {
    const itemsWithFail = MOCK_ITEMS.map((r, i) => ({
      ...r,
      failed: i === 0 ? 1 : 0,
      fail_status: i === 0 ? 500 : 0,
    }));
    global.fetch = vi.fn(async () => ({
      ok: true,
      status: 200,
      json: async () => ({
        requests: itemsWithFail,
        next_cursor: null,
        range: "24h",
        models_filter: [],
        accounts_filter: [],
      }),
    })) as unknown as typeof fetch;

    renderWithQuery();
    await waitFor(() => {
      // Success badges
      const successBadges = screen.getAllByText("成功");
      expect(successBadges.length).toBe(49);
      // Fail badge
      expect(screen.getByText("失败")).toBeInTheDocument();
    });
  });

  it("refetches requests periodically so the table does not stay stale", async () => {
    // Regression: /usage table must auto-refresh on a cadence (like the overview
    // summary does), otherwise the user sees stale rows that never update.
    vi.useFakeTimers({ shouldAdvanceTime: true, advanceTimeDelta: 1 });
    let callCount = 0;
    global.fetch = vi.fn(async () => {
      callCount++;
      return {
        ok: true,
        status: 200,
        json: async () => ({
          requests: [
            { ...MOCK_ITEMS[0], request_id: `r-${callCount}`, model: `m-${callCount}` },
          ],
          next_cursor: null,
          range: "24h",
          models_filter: [],
          accounts_filter: [],
        }),
      };
    }) as unknown as typeof fetch;

    try {
      renderWithQuery();
      // Initial fetch
      await waitFor(() => {
        expect(screen.getByText("m-1")).toBeInTheDocument();
      });
      expect(callCount).toBe(1);

      // Advance past the configured refetch interval (use a generous window).
      // If no refetchInterval is set, callCount stays at 1 -> test fails.
      vi.advanceTimersByTime(60_000);
      await waitFor(() => {
        expect(callCount).toBeGreaterThanOrEqual(2);
      });
    } finally {
      vi.useRealTimers();
    }
  });
});
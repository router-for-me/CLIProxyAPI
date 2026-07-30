import { render, screen } from "@testing-library/react";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useUIStore } from "@/stores/uiStore";
import { DetailDrawer } from "../DetailDrawer";

const mockRequests = {
  requests: Array.from({ length: 12 }, (_, i) => ({
    id: i,
    request_id: `r${i}`,
    timestamp: "2026-01-01T00:00:00Z",
    account: `acc${i}`,
    model: `m${i % 3}`,
    endpoint: "e",
    provider: "p",
    total_tokens: 100 * i,
    latency_ms: 100,
    failed: 0,
    fail_status: 0,
    input_tokens: 60,
    output_tokens: 40,
    reasoning_tokens: 0,
    cached_tokens: 0,
    cache_read_tokens: 0,
    cache_creation_tokens: 0,
    ttft_ms: 50,
    alias: null,
  })),
  next_cursor: null,
  range: "24h",
  models_filter: [],
  accounts_filter: [],
};

beforeEach(() => {
  useUIStore.setState({ detailDrawerRequestId: null });
  global.fetch = vi.fn(async () => ({
    ok: true,
    status: 200,
    json: async () => mockRequests,
  })) as unknown as typeof fetch;
});

describe("DetailDrawer", () => {
  it("renders nothing when no request id is selected", () => {
    render(
      <QueryClientProvider client={new QueryClient()}>
        <DetailDrawer />
      </QueryClientProvider>,
    );
    expect(screen.queryByText("请求详情")).not.toBeInTheDocument();
  });

  it("renders request details when a request id is selected", async () => {
    useUIStore.setState({ detailDrawerRequestId: "0" });
    render(
      <QueryClientProvider client={new QueryClient()}>
        <DetailDrawer />
      </QueryClientProvider>,
    );
    expect(await screen.findByText("请求详情")).toBeInTheDocument();
    expect(await screen.findByText(/"r0"/)).toBeInTheDocument();
  });

  it("closes when the close button is clicked", async () => {
    const user = (await import("@testing-library/user-event")).default.setup();
    useUIStore.setState({ detailDrawerRequestId: "0" });
    render(
      <QueryClientProvider client={new QueryClient()}>
        <DetailDrawer />
      </QueryClientProvider>,
    );
    expect(await screen.findByText("请求详情")).toBeInTheDocument();

    const closeButton = screen.getByRole("button", { name: /close/i });
    await user.click(closeButton);
    expect(screen.queryByText("请求详情")).not.toBeInTheDocument();
  });
});
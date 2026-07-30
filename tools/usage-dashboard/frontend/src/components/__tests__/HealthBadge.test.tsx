import { render, screen } from "@testing-library/react";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { HealthBadge, HealthBadgeLive } from "../HealthBadge";

describe("HealthBadge", () => {
  it("renders green dot when healthy", () => {
    render(<HealthBadge ok={true} lastPollAt="2026-01-01T00:00:00Z" />);
    expect(screen.getByText(/healthy|健康/i)).toBeInTheDocument();
    expect(document.querySelector(".bg-green-500")).toBeInTheDocument();
  });

  it("renders red dot when degraded", () => {
    render(<HealthBadge ok={false} lastPollAt="" error="connection refused" />);
    expect(screen.getByText(/降级/i)).toBeInTheDocument();
    expect(document.querySelector(".bg-red-500")).toBeInTheDocument();
  });

  it("renders unknown before first poll", () => {
    render(<HealthBadge ok={null} lastPollAt="" />);
    expect(screen.getByText(/—|unknown|未知/i)).toBeInTheDocument();
  });
});

describe("HealthBadgeLive", () => {
  const wrapper = ({ children }: { children: React.ReactNode }) => {
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  };

  beforeEach(() => {
    vi.restoreAllMocks();
  });

  it("renders healthy state from API data", async () => {
    global.fetch = vi.fn(async () => ({
      ok: true,
      status: 200,
      json: async () => ({
        last_poll_at: "2026-01-01T00:00:00Z",
        last_poll_ok: true,
      }),
    })) as unknown as typeof fetch;

    render(<HealthBadgeLive />, { wrapper });
    expect(await screen.findByText(/healthy|健康/i)).toBeInTheDocument();
    expect(document.querySelector(".bg-green-500")).toBeInTheDocument();
  });

  it("renders degraded state from API data", async () => {
    global.fetch = vi.fn(async () => ({
      ok: true,
      status: 200,
      json: async () => ({
        last_poll_at: "2026-01-01T00:00:00Z",
        last_poll_ok: false,
        last_poll_error: "connection refused",
      }),
    })) as unknown as typeof fetch;

    render(<HealthBadgeLive />, { wrapper });
    expect(await screen.findByText(/降级.*connection refused/i)).toBeInTheDocument();
    expect(document.querySelector(".bg-red-500")).toBeInTheDocument();
  });

  it("renders unknown when API has no data", async () => {
    global.fetch = vi.fn(async () => ({
      ok: true,
      status: 200,
      json: async () => ({}),
    })) as unknown as typeof fetch;

    render(<HealthBadgeLive />, { wrapper });
    expect(await screen.findByText(/—|unknown|未知/i)).toBeInTheDocument();
  });

  it("renders aria-live region", async () => {
    global.fetch = vi.fn(async () => ({
      ok: true,
      status: 200,
      json: async () => ({
        last_poll_at: "2026-01-01T00:00:00Z",
        last_poll_ok: true,
      }),
    })) as unknown as typeof fetch;

    const { container } = render(<HealthBadgeLive />, { wrapper });
    const liveRegion = container.querySelector('[role="status"]');
    expect(liveRegion).toBeInTheDocument();
    expect(liveRegion).toHaveAttribute("aria-live", "polite");
    expect(liveRegion).toHaveAttribute("aria-atomic", "true");
  });
});
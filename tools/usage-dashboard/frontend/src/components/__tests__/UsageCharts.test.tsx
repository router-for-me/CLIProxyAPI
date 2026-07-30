import { render, screen, waitFor } from "@testing-library/react";
import { describe, it, expect, vi, beforeAll } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import ProviderDistributionChart from "../charts/ProviderDistributionChart";
import EndpointDistributionChart from "../charts/EndpointDistributionChart";
import UsageTrendChart from "../charts/UsageTrendChart";

function mockFetchResponse(url: URL | string) {
  const path = typeof url === "string" ? url : url.pathname;
  if (path.includes("/api/v1/providers")) {
    return {
      ok: true, status: 200,
      json: async () => ({
        providers: [
          { provider: "openai", total_tokens: 5000, estimated_cost: 0.05 },
          { provider: "anthropic", total_tokens: 3000, estimated_cost: 0.03 },
        ],
      }),
    };
  }
  if (path.includes("/api/v1/endpoints")) {
    return {
      ok: true, status: 200,
      json: async () => ({
        endpoints: [
          { endpoint: "/chat", total_tokens: 5000, estimated_cost: 0.05 },
        ],
      }),
    };
  }
  return {
    ok: true, status: 200,
    json: async () => ({
      hours: [
        { hour: "2026-01-01T00:00:00Z", total_tokens: 1000 },
      ],
    }),
  };
}

beforeAll(() => {
  global.fetch = vi.fn(async (url: URL | string) => mockFetchResponse(url)) as unknown as typeof fetch;
});

function renderWithQuery(ui: React.ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>);
}

describe("ProviderDistributionChart", () => {
  it("renders a Bar chart", async () => {
    renderWithQuery(<ProviderDistributionChart />);
    await waitFor(() => expect(screen.getByTestId("bar-mock")).toBeInTheDocument());
  });

  it("shows Token/Cost toggle buttons", async () => {
    renderWithQuery(<ProviderDistributionChart />);
    await waitFor(() => {
      expect(screen.getByRole("button", { name: /token|token/i })).toBeInTheDocument();
      expect(screen.getByRole("button", { name: /cost|费用/i })).toBeInTheDocument();
    });
  });
});

describe("EndpointDistributionChart", () => {
  it("renders a Bar chart", async () => {
    renderWithQuery(<EndpointDistributionChart />);
    await waitFor(() => expect(screen.getByTestId("bar-mock")).toBeInTheDocument());
  });
});

describe("UsageTrendChart", () => {
  it("renders a Line chart", async () => {
    renderWithQuery(<UsageTrendChart />);
    await waitFor(() => expect(screen.getByTestId("line-mock")).toBeInTheDocument());
  });
});
import { render, screen, waitFor } from "@testing-library/react";
import { describe, it, expect, vi, beforeAll } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import ModelDistributionChart from "../ModelDistributionChart";

function render_() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={qc}><ModelDistributionChart /></QueryClientProvider>);
}

beforeAll(() => {
  global.fetch = vi.fn(async () => ({
    ok: true,
    status: 200,
    json: async () => ({
      models: [
        { model: "gpt-4", total_tokens: 5000, estimated_cost: 0.05 },
        { model: "claude", total_tokens: 3000, estimated_cost: 0.03 },
      ],
    }),
  })) as unknown as typeof fetch;
});

describe("ModelDistributionChart", () => {
  it("renders a Bar chart", async () => {
    render_();
    await waitFor(() => expect(screen.getByTestId("bar-mock")).toBeInTheDocument());
  });

  it("shows Token/Cost toggle buttons", async () => {
    render_();
    await waitFor(() => {
      expect(screen.getByRole("button", { name: /token|token/i })).toBeInTheDocument();
      expect(screen.getByRole("button", { name: /cost|费用/i })).toBeInTheDocument();
    });
  });
});
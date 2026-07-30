import { render, screen, waitFor } from "@testing-library/react";
import { describe, it, expect, vi, beforeAll } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import TokenTrendChart from "../TokenTrendChart";

function render_() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={qc}><TokenTrendChart /></QueryClientProvider>);
}

beforeAll(() => {
  global.fetch = vi.fn(async () => ({
    ok: true,
    status: 200,
    json: async () => ({
      hours: [
        { hour: "2026-01-01T00:00:00Z", total_tokens: 1000 },
        { hour: "2026-01-01T01:00:00Z", total_tokens: 2000 },
      ],
    }),
  })) as unknown as typeof fetch;
});

describe("TokenTrendChart", () => {
  it("renders a Line chart", async () => {
    render_();
    await waitFor(() => expect(screen.getByTestId("line-mock")).toBeInTheDocument());
  });
});
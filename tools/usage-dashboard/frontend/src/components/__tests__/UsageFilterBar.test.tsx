import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi, beforeAll } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import UsageFilterBar from "../UsageFilterBar";
import { useFiltersStore } from "@/stores/filtersStore";

beforeAll(() => {
  global.fetch = vi.fn((url: RequestInfo | URL) => {
    const path = typeof url === "string" ? url : url.toString();
    let body: Record<string, unknown> = { accounts_filter: [] };
    if (path.includes("/models")) body = { models: [{ model: "gpt-4" }, { model: "claude" }], accounts_filter: [] };
    if (path.includes("/accounts")) body = { accounts: [{ account: "acc1" }], accounts_filter: [] };
    if (path.includes("/providers")) body = { providers: [{ provider: "openai" }], accounts_filter: [] };
    if (path.includes("/endpoints")) body = { endpoints: [{ endpoint: "/v1/chat" }], accounts_filter: [] };
    return Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } }));
  });
});

describe("UsageFilterBar", () => {
  it("renders 4 multi-select triggers", async () => {
    render(<QueryClientProvider client={new QueryClient()}><UsageFilterBar /></QueryClientProvider>);
    expect(screen.getByText("模型")).toBeInTheDocument();
    expect(screen.getByText("账号")).toBeInTheDocument();
    expect(screen.getByText("供应商")).toBeInTheDocument();
    expect(screen.getByText("端点")).toBeInTheDocument();
  });

  it("clicking Refresh triggers a refetch", async () => {
    const invalidate = vi.fn();
    render(<QueryClientProvider client={new QueryClient()}><UsageFilterBar onRefresh={invalidate} /></QueryClientProvider>);
    await userEvent.click(screen.getByRole("button", { name: /刷新/i }));
    expect(invalidate).toHaveBeenCalled();
  });

  it("Reset clears filtersStore", async () => {
    useFiltersStore.setState({
      selectedModels: ["gpt-4"],
      selectedAccounts: ["acc1"],
      selectedProviders: [],
      selectedEndpoints: [],
    });
    render(<QueryClientProvider client={new QueryClient()}><UsageFilterBar /></QueryClientProvider>);
    await userEvent.click(screen.getByRole("button", { name: /重置/i }));
    expect(useFiltersStore.getState().selectedModels).toEqual([]);
    expect(useFiltersStore.getState().selectedAccounts).toEqual([]);
  });
});
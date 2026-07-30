import { renderHook, act } from "@testing-library/react";
import { describe, it, expect, vi, beforeAll, afterAll } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useExport } from "../useExport";

beforeAll(() => {
  // Mock fetch
  global.fetch = vi.fn(async () => ({
    ok: true,
    status: 200,
    text: async () => "timestamp,model\n2026-01-01,gpt-4",
    headers: {
      get: (k: string) =>
        k === "content-disposition"
          ? 'attachment; filename="usage_export.csv"'
          : null,
    },
    blob: async () => new Blob(["x"], { type: "text/csv" }),
  })) as unknown as typeof fetch;

  // jsdom doesn't propagate URL from window to globalThis
  globalThis.URL = window.URL;
  window.URL.createObjectURL = vi.fn(() => "blob:fake");
  window.URL.revokeObjectURL = vi.fn();
});

afterAll(() => {
  vi.restoreAllMocks();
});

describe("useExport", () => {
  it("downloads a CSV blob", async () => {
    const qc = new QueryClient();
    const wrapper = ({ children }: { children: React.ReactNode }) => (
      <QueryClientProvider client={qc}>{children}</QueryClientProvider>
    );
    const { result } = renderHook(() => useExport(), { wrapper });
    await act(async () => {
      await result.current.export({ range: "24h" });
    });
    expect(window.URL.createObjectURL).toHaveBeenCalled();
  });
});
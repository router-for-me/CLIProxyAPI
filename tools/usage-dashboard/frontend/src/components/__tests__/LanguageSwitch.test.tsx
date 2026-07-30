import { render, screen, act } from "@testing-library/react";
import { describe, it, expect, beforeEach } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { Header } from "../Header";
import { useLanguageStore } from "@/stores/languageStore";

function wrap(ui: React.ReactNode) {
  const qc = new QueryClient();
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>{ui}</MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("Language switching", () => {
  beforeEach(() => {
    useLanguageStore.getState().setLang("zh");
  });

  it("defaults to Chinese nav labels", () => {
    wrap(<Header />);
    expect(screen.getByText("概览")).toBeInTheDocument();
    expect(screen.getByText("用量明细")).toBeInTheDocument();
  });

  it("switches nav labels to English", async () => {
    wrap(<Header />);
    act(() => useLanguageStore.getState().setLang("en"));
    expect(await screen.findByText("Overview")).toBeInTheDocument();
    expect(await screen.findByText("Usage")).toBeInTheDocument();
  });
});

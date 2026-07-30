import { describe, it, expect, beforeEach } from "vitest";
import { useFiltersStore } from "../filtersStore";

describe("filtersStore", () => {
  beforeEach(() => useFiltersStore.setState({
    range: "24h", from: undefined, to: undefined,
    granularity: "hour", selectedModels: [], selectedAccounts: [],
  }));

  it("updates range and clears explicit from/to", () => {
    useFiltersStore.getState().setRange("7d");
    expect(useFiltersStore.getState().range).toBe("7d");
    expect(useFiltersStore.getState().from).toBeUndefined();
  });

  it("sets explicit from/to and clears range preset", () => {
    useFiltersStore.getState().setExplicitRange("2026-01-01T00:00:00Z", "2026-01-02T00:00:00Z");
    expect(useFiltersStore.getState().range).toBe("explicit");
    expect(useFiltersStore.getState().from).toBe("2026-01-01T00:00:00Z");
  });

  it("toggles a model selection", () => {
    useFiltersStore.getState().toggleModel("gpt-4");
    expect(useFiltersStore.getState().selectedModels).toEqual(["gpt-4"]);
    useFiltersStore.getState().toggleModel("gpt-4");
    expect(useFiltersStore.getState().selectedModels).toEqual([]);
  });

  it("preserves object identity for unchanged slices (avoids re-renders)", () => {
    const before = useFiltersStore.getState().selectedAccounts;
    useFiltersStore.getState().setRange("1h");
    expect(useFiltersStore.getState().selectedAccounts).toBe(before);
  });
});
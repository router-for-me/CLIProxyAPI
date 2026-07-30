import { describe, it, expect, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { RangeSelector } from "../RangeSelector";
import { useFiltersStore } from "@/stores/filtersStore";
import { TestProvider } from "@/test-utils";

describe("RangeSelector", () => {
  beforeEach(() => useFiltersStore.setState({
    range: "24h", from: undefined, to: undefined,
    granularity: "hour", selectedModels: [], selectedAccounts: [],
  }));

  it("renders the current range preset", () => {
    render(<RangeSelector />, { wrapper: TestProvider });
    expect(screen.getByRole("combobox")).toBeInTheDocument();
  });

  it("changing RangeSelector updates the store", async () => {
    const user = userEvent.setup();
    render(<RangeSelector />, { wrapper: TestProvider });

    // Open the select popup
    const trigger = screen.getByRole("combobox");
    await user.click(trigger);

    // Find and click the "7d" option
    const option = await screen.findByText("近 7 天");
    await user.click(option);

    expect(useFiltersStore.getState().range).toBe("7d");
  });
});
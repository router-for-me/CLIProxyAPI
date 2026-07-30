import { render, screen } from "@testing-library/react";
import { describe, it, expect } from "vitest";
import ChartPanel from "../ChartPanel";

describe("ChartPanel", () => {
  it("renders title and children", () => {
    render(
      <ChartPanel title="Test Chart">
        <div data-testid="child" />
      </ChartPanel>,
    );
    expect(screen.getByText("Test Chart")).toBeInTheDocument();
    expect(screen.getByTestId("child")).toBeInTheDocument();
  });
});
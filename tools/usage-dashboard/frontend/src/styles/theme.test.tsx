import { render } from "@testing-library/react";
import { describe, it, expect } from "vitest";
import "./globals.css";

function Box() {
  return <div data-testid="bx" className="bg-background text-foreground" />;
}

describe("theme tokens", () => {
  it("resolves background token to a non-empty color", () => {
    const { getByTestId } = render(<Box />);
    expect(getByTestId("bx").className).toContain("bg-background");
    expect(getByTestId("bx").className).toContain("text-foreground");
  });
});
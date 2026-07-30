import { render, screen } from "@testing-library/react";
import { describe, it, expect } from "vitest";
import { KpiCard } from "../KpiCard";

describe("KpiCard", () => {
  it("renders label, value, and sub", () => {
    render(<KpiCard label="Today Requests" value="1234" sub="↑ 12% vs yesterday" />);
    expect(screen.getByText("Today Requests")).toBeInTheDocument();
    expect(screen.getByText("1234")).toBeInTheDocument();
    expect(screen.getByText(/↑ 12%/)).toBeInTheDocument();
  });

  it("shows loading skeleton when value is undefined", () => {
    render(<KpiCard label="X" value={undefined} sub="" loading />);
    expect(screen.queryByText("X")).toBeInTheDocument();
    // Skeleton is an element with aria-busy
    expect(document.querySelector("[aria-busy='true']")).toBeInTheDocument();
  });
});
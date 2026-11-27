import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { QuotaMeter } from "./ui";
import type { QuotaWindow } from "../types";

const NOW = Date.parse("2026-10-13T12:00:00Z");

function makeWindow(overrides: Partial<QuotaWindow> = {}): QuotaWindow {
  return {
    key: "weekly",
    kind: "weekly",
    label: "Weekly",
    used_percent: 70,
    remaining_percent: 30,
    source: "poll",
    observed_at: "2026-10-13T11:59:00Z",
    stale: false,
    reset_passed: false,
    ...overrides,
  };
}

afterEach(() => {
  cleanup();
});

describe("QuotaMeter", () => {
  it("reports remaining capacity, never utilization", () => {
    render(<QuotaMeter w={makeWindow({ used_percent: 70, remaining_percent: 30 })} label="Weekly" now={NOW} />);
    const meter = screen.getByRole("meter");
    expect(meter.getAttribute("aria-valuenow")).toBe("30");
    expect(screen.getByText("30%")).toBeTruthy();
  });

  it("shows 'No usage this window' when fully remaining and no reset time", () => {
    render(<QuotaMeter w={makeWindow({ used_percent: 0, remaining_percent: 100, resets_at: undefined })} label="Weekly" now={NOW} />);
    expect(screen.getByText("No usage this window")).toBeTruthy();
  });

  it("uses the critical tone when the window is rejected, even with capacity left", () => {
    const { container } = render(<QuotaMeter w={makeWindow({ rejected: true, remaining_percent: 50, used_percent: 50 })} label="Weekly" now={NOW} />);
    const el = container.querySelector(".meter");
    expect(el).not.toBeNull();
    expect(el!.classList.contains("critical")).toBe(true);
  });

  it("marks reset-passed windows as stale and asks for data", () => {
    const { container } = render(<QuotaMeter w={makeWindow({ reset_passed: true })} label="Weekly" now={NOW} />);
    const el = container.querySelector(".meter");
    expect(el!.classList.contains("stale")).toBe(true);
    expect(screen.getByText(/awaiting data/)).toBeTruthy();
  });

  it("renders a dash when there is no window", () => {
    render(<QuotaMeter w={undefined} label="Weekly" now={NOW} />);
    expect(screen.getByText("—")).toBeTruthy();
  });
});

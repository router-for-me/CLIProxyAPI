import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import type { Account } from "../types";
import { AccountTable } from "./AccountTable";

const NOW = Date.parse("2026-10-13T12:00:00Z");

const account: Account = {
  id: "claude-a",
  display: "Claude A",
  provider: "claude",
  present: true,
  disabled: false,
  unavailable: true,
  cooldown_stale: true,
  host_priority: 0,
  plugin_priority: 0,
  polled: true,
  windows: [],
  quota_state: "ready",
  quota_reason: "",
  routing: "blocked",
  routing_reason: "cooldown_stale",
  remaining_percent: 50,
  expiring_soon: false,
  active_sessions: 0,
  preferred: false,
  rank: 0,
  health: { auth_failed: false, auth_suspect: false },
  burn: {},
};

afterEach(() => {
  cleanup();
});

describe("AccountTable", () => {
  it("shows the stale CPA cooldown badge with clear instructions", () => {
    render(<AccountTable accounts={[account]} now={NOW} onOpen={() => {}} />);

    const badge = screen.getByText("CPA cooldown stale");
    expect(badge.classList.contains("warning")).toBe(true);
    expect(badge.getAttribute("title")).toContain("Clear the cooldown in CPA");
  });

  it("shows a failing poll even after an earlier successful one", () => {
    const failing: Account = { ...account, unavailable: false, cooldown_stale: false, routing: "blocked", routing_reason: "quota_stale", poll_error: "invalid_model_window", last_poll_at: "2026-10-13T11:00:00Z" };
    render(<AccountTable accounts={[failing]} now={NOW} onOpen={() => {}} />);

    expect(screen.getByText("Poll failing: invalid_model_window")).toBeTruthy();
  });
});

import { describe, expect, it } from "vitest";
import { band, clampPct, compact, duration, goDuration, pct, reason, relative, windowName } from "./format";

const NOW = Date.parse("2026-10-13T12:00:00Z");

describe("clampPct", () => {
  it("maps undefined, null and NaN to 0", () => {
    expect(clampPct(undefined)).toBe(0);
    expect(clampPct(null)).toBe(0);
    expect(clampPct(NaN)).toBe(0);
  });

  it("clamps to the 0..100 range", () => {
    expect(clampPct(-5)).toBe(0);
    expect(clampPct(150)).toBe(100);
  });

  it("passes in-range values through", () => {
    expect(clampPct(42)).toBe(42);
  });
});

describe("pct", () => {
  it("rounds to a whole percent", () => {
    expect(pct(42.4)).toBe("42%");
  });

  it("keeps one decimal for values between 0 and 10", () => {
    expect(pct(3.25)).toBe("3.3%");
  });

  it("formats 100 without decimals", () => {
    expect(pct(100)).toBe("100%");
  });

  it("renders a dash for undefined", () => {
    expect(pct(undefined)).toBe("—");
  });
});

describe("band", () => {
  it("classifies remaining capacity into good, warning and critical", () => {
    expect(band(50)).toBe("good");
    expect(band(35)).toBe("warning");
    expect(band(20)).toBe("warning");
    expect(band(10)).toBe("critical");
    expect(band(0)).toBe("critical");
  });
});

describe("duration", () => {
  it("formats seconds, minutes, hours and days", () => {
    expect(duration(45_000)).toBe("45s");
    expect(duration(5 * 60_000)).toBe("5m");
    expect(duration((3 * 3600 + 20 * 60) * 1000)).toBe("3h 20m");
    expect(duration((2 * 86400 + 5 * 3600) * 1000)).toBe("2d 5h");
  });

  it("prefixes negative durations with a minus sign", () => {
    expect(duration(-45_000)).toBe("-45s");
  });
});

describe("relative", () => {
  it("reports 'now' within five seconds", () => {
    expect(relative("2026-10-13T12:00:02Z", NOW)).toBe("now");
  });

  it("reports future times with 'in'", () => {
    expect(relative("2026-10-13T15:20:00Z", NOW)).toBe("in 3h 20m");
  });

  it("reports past times with 'ago'", () => {
    expect(relative("2026-10-13T11:56:00Z", NOW)).toBe("4m ago");
  });

  it("renders a dash for missing or unparseable input", () => {
    expect(relative(undefined, NOW)).toBe("—");
    expect(relative("garbage", NOW)).toBe("—");
  });
});

describe("reason", () => {
  it("describes exhausted windows", () => {
    expect(reason("exhausted:weekly_exhausted")).toBe("Exhausted (weekly)");
    expect(reason("exhausted:model_weekly:opus_exhausted")).toBe("Exhausted (weekly Opus)");
  });

  it("mentions the HTTP status for auth failures", () => {
    expect(reason("auth_failed_401")).toContain("401");
  });

  it("mentions the remaining percent for low headroom", () => {
    expect(reason("low_headroom:3")).toContain("3%");
  });

  it("explains stale CPA cooldown skips", () => {
    expect(reason("cooldown_stale")).toContain("Clear the cooldown in CPA");
  });

  it("explains learned reasons with the underlying reason", () => {
    expect(reason("learned:removed")).toContain("Credential removed");
  });

  it("returns non-empty text for known plain codes", () => {
    const text = reason("not_offered");
    expect(typeof text).toBe("string");
    expect(text.length).toBeGreaterThan(0);
  });

  it("returns an empty string for missing codes", () => {
    expect(reason(undefined)).toBe("");
  });
});

describe("windowName", () => {
  it("names the five-hour window", () => {
    expect(windowName("five_hour")).toBe("5-hour");
  });

  it("names per-model weekly windows", () => {
    expect(windowName("model_weekly:fable")).toBe("weekly Fable");
  });
});

describe("goDuration", () => {
  it("drops trailing zero units from Go durations", () => {
    expect(goDuration("3h0m0s")).toBe("3h");
    expect(goDuration("30m0s")).toBe("30m");
  });

  it("returns a string for mixed durations without crashing", () => {
    expect(typeof goDuration("1h30m0s")).toBe("string");
  });
});

describe("compact", () => {
  it("formats counts with k and M suffixes", () => {
    expect(compact(950)).toBe("950");
    expect(compact(1500)).toBe("1.5k");
    expect(compact(25_000)).toBe("25k");
    expect(compact(2_500_000)).toBe("2.5M");
  });
});

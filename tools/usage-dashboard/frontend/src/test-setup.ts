import "@testing-library/jest-dom/vitest";
import { vi } from "vitest";
import { createElement } from "react";

vi.mock("react-chartjs-2", () => ({
  Bar: (props: { data?: { datasets?: Array<{ label?: string }> } }) =>
    createElement("div", {
      "data-testid": "bar-mock",
      "data-mode": props.data?.datasets?.[0]?.label,
    }),
  Line: (props: { data?: { labels?: string[] } }) =>
    createElement("div", {
      "data-testid": "line-mock",
      "data-labels": props.data?.labels?.join(","),
    }),
  Doughnut: () => createElement("div", { "data-testid": "doughnut-mock" }),
  Pie: () => createElement("div", { "data-testid": "pie-mock" }),
}));
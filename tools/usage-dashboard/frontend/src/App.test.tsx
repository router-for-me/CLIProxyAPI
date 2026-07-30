import { render, screen } from "@testing-library/react";
import { RouterProvider, createMemoryRouter } from "react-router-dom";
import { describe, it, expect } from "vitest";
import { routes } from "./router";
import { TestProvider } from "./test-utils";

describe("App routing", () => {
  it("renders Dashboard at /", async () => {
    const router = createMemoryRouter(routes, { initialEntries: ["/"] });
    render(<RouterProvider router={router} />, { wrapper: TestProvider });
    expect(await screen.findByText("API 密钥")).toBeInTheDocument();
  });

  it("renders Usage at /usage with KPI cards", async () => {
    const router = createMemoryRouter(routes, { initialEntries: ["/usage"] });
    render(<RouterProvider router={router} />, { wrapper: TestProvider });
    expect(await screen.findByText("总请求数")).toBeInTheDocument();
  });

  it("shows nav with links to / and /usage", () => {
    const router = createMemoryRouter(routes, { initialEntries: ["/"] });
    render(<RouterProvider router={router} />, { wrapper: TestProvider });
    expect(screen.getByRole("link", { name: /概览/i })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /用量明细/i })).toBeInTheDocument();
  });
});
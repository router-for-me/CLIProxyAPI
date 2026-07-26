import { render, screen } from "@testing-library/react";
import { describe, it, expect } from "vitest";
import App from "./App";

describe("App", () => {
  it("renders the dashboard placeholder", () => {
    render(<App />);
    expect(screen.getByText(/usage dashboard/i)).toBeInTheDocument();
  });
});
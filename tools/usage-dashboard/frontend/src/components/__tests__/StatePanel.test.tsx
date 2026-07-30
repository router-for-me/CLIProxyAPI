import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi } from "vitest";
import { StatePanel } from "../StatePanel";

describe("StatePanel", () => {
  it("shows children when loaded with data", () => {
    render(
      <StatePanel loading={false} error={null} isEmpty={false}>
        content
      </StatePanel>,
    );
    expect(screen.getByText("content")).toBeInTheDocument();
  });

  it("shows skeleton when loading", () => {
    render(
      <StatePanel loading={true} error={null} isEmpty={false}>
        content
      </StatePanel>,
    );
    expect(document.querySelector("[aria-busy='true']")).toBeInTheDocument();
    expect(screen.queryByText("content")).not.toBeInTheDocument();
  });

  it("shows error + Retry when errored", () => {
    render(
      <StatePanel
        loading={false}
        error={new Error("oops")}
        isEmpty={false}
        onRetry={() => {}}
      >
        content
      </StatePanel>,
    );
    expect(screen.getByText(/加载失败/i)).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /retry|重试/i }),
    ).toBeInTheDocument();
  });

  it("shows empty state when isEmpty", () => {
    render(
      <StatePanel loading={false} error={null} isEmpty={true}>
        content
      </StatePanel>,
    );
    expect(screen.getByText(/暂无数据/i)).toBeInTheDocument();
  });

  it("Retry click calls onRetry", async () => {
    const onRetry = vi.fn();
    render(
      <StatePanel
        loading={false}
        error={new Error("x")}
        isEmpty={false}
        onRetry={onRetry}
      >
        x
      </StatePanel>,
    );
    await userEvent.click(
      screen.getByRole("button", { name: /retry|重试/i }),
    );
    expect(onRetry).toHaveBeenCalled();
  });
});
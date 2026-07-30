import { lazy } from "react";
import { type RouteObject } from "react-router-dom";
import App from "./App";
import { RouteErrorBoundary } from "@/components/RouteErrorBoundary";

const Dashboard = lazy(() =>
  import("./pages/Dashboard").then((m) => ({ default: m.Dashboard })),
);
const Usage = lazy(() =>
  import("./pages/Usage").then((m) => ({ default: m.Usage })),
);

export const routes: RouteObject[] = [
  {
    path: "/",
    element: <App />,
    children: [
      { index: true, element: <RouteErrorBoundary><Dashboard /></RouteErrorBoundary> },
      { path: "usage", element: <RouteErrorBoundary><Usage /></RouteErrorBoundary> },
    ],
  },
];
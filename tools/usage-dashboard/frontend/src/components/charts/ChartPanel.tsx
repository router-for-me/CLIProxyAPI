import type { ReactNode } from "react";

interface ChartPanelProps {
  title: string;
  children: ReactNode;
}

export default function ChartPanel({ title, children }: ChartPanelProps) {
  return (
    <div className="rounded-lg border p-4">
      <h3 className="mb-3 text-sm font-medium">{title}</h3>
      <div className="relative h-64">{children}</div>
    </div>
  );
}
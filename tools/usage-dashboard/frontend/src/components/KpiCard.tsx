import { Card } from "@/components/ui/card";
import { cn } from "@/lib/utils";

interface KpiCardProps {
  label: string;
  value?: string | number;
  sub?: React.ReactNode;
  loading?: boolean;
  className?: string;
}

export function KpiCard({ label, value, sub, loading, className }: KpiCardProps) {
  return (
    <Card className={cn("p-4", className)}>
      <div className="text-xs text-muted-foreground">{label}</div>
      {loading ? (
        <div aria-busy="true" className="mt-1 h-7 w-20 animate-pulse rounded bg-muted" />
      ) : (
        <div className="mt-1 text-xl font-semibold tabular-nums">{value ?? "—"}</div>
      )}
      {sub && <div className="mt-1 text-xs text-muted-foreground">{sub}</div>}
    </Card>
  );
}
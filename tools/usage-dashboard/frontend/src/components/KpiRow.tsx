import { cn } from "@/lib/utils";

interface KpiRowProps {
  children: React.ReactNode;
  className?: string;
}

export function KpiRow({ children, className }: KpiRowProps) {
  return (
    <div className={cn("grid grid-cols-4 gap-3", className)}>
      {children}
    </div>
  );
}
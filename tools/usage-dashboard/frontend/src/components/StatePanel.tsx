import { AlertCircle } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useT } from "@/stores/languageStore";

interface StatePanelProps {
  loading: boolean;
  error: Error | null;
  isEmpty: boolean;
  onRetry?: () => void;
  children?: React.ReactNode;
  skeletonClassName?: string;
}

export function StatePanel({
  loading,
  error,
  isEmpty,
  onRetry,
  children,
  skeletonClassName,
}: StatePanelProps) {
  const t = useT();
  if (loading) {
    return (
      <div
        aria-busy="true"
        className={`animate-pulse rounded bg-muted ${skeletonClassName ?? "h-32 w-full"}`}
      />
    );
  }
  if (error) {
    return (
      <div className="flex flex-col items-center gap-2 p-4 text-muted-foreground">
        <AlertCircle className="h-8 w-8 text-red-500" />
        <div>{t("state.load_failed")}</div>
        {onRetry && (
          <Button size="sm" variant="ghost" onClick={onRetry}>
            {t("state.retry")}
          </Button>
        )}
      </div>
    );
  }
  if (isEmpty) {
    return (
      <div className="p-4 text-center text-muted-foreground">{t("state.no_data")}</div>
    );
  }
  return <>{children}</>;
}
import { useRef, useEffect, useCallback } from "react";
import { StatePanel } from "@/components/StatePanel";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { useRequestsInfinite } from "@/api/hooks/useRequestsInfinite";
import { useUIStore } from "@/stores/uiStore";
import { useT } from "@/stores/languageStore";
import { formatTokens, formatMs } from "@/lib/format";
export default function UsageRequestsTable() {
  const { data, fetchNextPage, hasNextPage, isFetchingNextPage, isLoading, error, refetch } =
    useRequestsInfinite();
  const openDetail = useUIStore((s) => s.openDetailDrawer);
  const t = useT();
  const sentinelRef = useRef<HTMLDivElement>(null);

  const rows = data?.pages.flatMap((p) => p.requests) ?? [];
  const totalRows = rows.length;

  const handleIntersect = useCallback(
    (entries: IntersectionObserverEntry[]) => {
      if (entries[0]?.isIntersecting && hasNextPage && !isFetchingNextPage) {
        fetchNextPage();
      }
    },
    [hasNextPage, isFetchingNextPage, fetchNextPage],
  );

  useEffect(() => {
    const el = sentinelRef.current;
    if (!el) return;
    const observer = new IntersectionObserver(handleIntersect, {
      rootMargin: "200px",
    });
    observer.observe(el);
    return () => observer.disconnect();
  }, [handleIntersect]);

  if (isLoading) {
    return (
      <div className="space-y-2">
        <div className="flex gap-4 px-4 py-2 text-sm text-muted-foreground">
          <span className="w-36">{t("col.time")}</span>
          <span className="w-24">{t("col.model")}</span>
          <span className="w-20">{t("col.provider")}</span>
          <span className="w-16 text-right">{t("col.input")}</span>
          <span className="w-16 text-right">{t("col.output")}</span>
          <span className="w-20 text-right">{t("col.cost")}</span>
          <span className="w-12">{t("col.status")}</span>
          <span className="w-16 text-right">{t("col.duration")}</span>
        </div>
        {Array.from({ length: 6 }, (_, i) => (
          <div key={i} className="flex gap-4 px-4 py-2">
            <Skeleton className="h-4 w-36" />
            <Skeleton className="h-4 w-24" />
            <Skeleton className="h-4 w-20" />
            <Skeleton className="h-4 w-16" />
            <Skeleton className="h-4 w-16" />
            <Skeleton className="h-4 w-20" />
            <Skeleton className="h-4 w-12" />
            <Skeleton className="h-4 w-16" />
          </div>
        ))}
      </div>
    );
  }

  if (totalRows === 0) {
    return (
      <div className="flex items-center justify-center py-12 text-sm text-muted-foreground">
        {t("state.no_data")}
      </div>
    );
  }

  return (
    <div>
      <StatePanel loading={false} error={error} isEmpty={false} onRetry={() => refetch()}>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t("col.time")}</TableHead>
              <TableHead>{t("col.model")}</TableHead>
              <TableHead>{t("col.provider")}</TableHead>
              <TableHead>{t("col.user")}</TableHead>
              <TableHead className="text-right">{t("col.input")}</TableHead>
              <TableHead className="text-right">{t("col.output")}</TableHead>
              <TableHead className="text-right">{t("col.cost")}</TableHead>
              <TableHead>{t("col.status")}</TableHead>
              <TableHead className="text-right">{t("col.duration")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((r) => (
              <TableRow
                key={r.request_id}
                className="cursor-pointer"
                onClick={() => openDetail(r.request_id)}
              >
                <TableCell className="text-xs whitespace-nowrap">
                  {r.local_time ?? r.timestamp}
                </TableCell>
                <TableCell className="text-xs max-w-32 truncate">
                  {r.model}
                </TableCell>
                <TableCell className="text-xs">{r.provider}</TableCell>
                <TableCell className="text-xs max-w-32 truncate">{r.account}</TableCell>
                <TableCell className="text-right text-xs">
                  {formatTokens(r.input_tokens)}
                </TableCell>
                <TableCell className="text-right text-xs">
                  {formatTokens(r.output_tokens)}
                </TableCell>
                <TableCell className="text-right text-xs font-mono">
                  {r.estimated_cost != null
                    ? `$${r.estimated_cost.toFixed(4)}`
                    : "—"}
                </TableCell>
                <TableCell className="text-xs">
                  {r.failed ? (
                    <Badge variant="destructive">{t("status.failed")}</Badge>
                  ) : (
                    <Badge variant="secondary">{t("status.success")}</Badge>
                  )}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
        <div className="flex items-center justify-between px-4 py-2 text-sm text-muted-foreground">
          <span>{t("state.showing_n_results", { n: totalRows })}</span>
          {isFetchingNextPage && <span>{t("state.loading")}</span>}
        </div>
        <div ref={sentinelRef} className="h-px" />
      </StatePanel>
    </div>
  );
}
import { memo } from "react";
import { StatePanel } from "@/components/StatePanel";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Skeleton } from "@/components/ui/skeleton";
import { useRequests } from "@/api/hooks/useRequests";
import { useFilterKey } from "@/stores/filtersStore";
import { useUIStore } from "@/stores/uiStore";
import { useT } from "@/stores/languageStore";
import { formatTokens, formatMs } from "@/lib/format";

interface RowData {
  id: number | string;
  timestamp: string;
  local_time?: string;
  account: string;
  model: string;
  total_tokens?: number | null;
  latency_ms?: number | null;
  failed: number | boolean;
  fail_status: number | null;
  request_id?: string;
}

function RecentUsageTable() {
  const filters = useFilterKey();
  const t = useT();
  const { data, isLoading, error, refetch } = useRequests({ ...filters, limit: 12 }) as {
    data?: { requests: Array<RowData> };
    isLoading: boolean;
    error: Error | null;
    refetch: () => void;
  };
  const openDetail = useUIStore((s) => s.openDetailDrawer);

  if (isLoading) {
    return (
      <div className="space-y-2">
        <div className="flex gap-4 px-4 py-2 text-sm text-muted-foreground">
          <span className="w-32">{t("col.time")}</span>
          <span className="w-20">{t("col.account")}</span>
          <span className="w-24">{t("col.model")}</span>
          <span className="w-16 text-right">{t("col.token")}</span>
          <span className="w-16 text-right">{t("col.latency")}</span>
          <span className="w-12">{t("col.status")}</span>
        </div>
        {Array.from({ length: 6 }, (_, i) => (
          <div key={i} className="flex gap-4 px-4 py-2">
            <Skeleton className="h-4 w-32" />
            <Skeleton className="h-4 w-20" />
            <Skeleton className="h-4 w-24" />
            <Skeleton className="h-4 w-16" />
            <Skeleton className="h-4 w-16" />
            <Skeleton className="h-4 w-12" />
          </div>
        ))}
      </div>
    );
  }

  const rows = data?.requests ?? [];
  if (rows.length === 0) {
    return (
      <div className="flex items-center justify-center py-12 text-sm text-muted-foreground">
        {t("state.no_data")}
      </div>
    );
  }

  return (
    <StatePanel loading={false} error={error} isEmpty={false} onRetry={() => refetch()}>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{t("col.time")}</TableHead>
            <TableHead>{t("col.account")}</TableHead>
            <TableHead>{t("col.model")}</TableHead>
            <TableHead className="text-right">{t("col.token")}</TableHead>
            <TableHead className="text-right">{t("col.latency")}</TableHead>
            <TableHead>{t("col.status")}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((r) => (
            <Row
              key={r.id}
              row={r}
              onClick={() => openDetail(String(r.id))}
            />
          ))}
        </TableBody>
      </Table>
    </StatePanel>
  );
}

const Row = memo(function Row({
  row,
  onClick,
}: {
  row: RowData;
  onClick: () => void;
}) {
  return (
    <TableRow className="cursor-pointer" onClick={onClick}>
      <TableCell className="text-xs whitespace-nowrap">
        {row.local_time ?? row.timestamp}
      </TableCell>
      <TableCell className="text-xs">{row.account || "—"}</TableCell>
      <TableCell className="text-xs">{row.model}</TableCell>
      <TableCell className="text-right text-xs">
        {formatTokens(row.total_tokens ?? 0)}
      </TableCell>
      <TableCell className="text-right text-xs">
        {formatMs(row.latency_ms ?? 0)}
      </TableCell>
      <TableCell className="text-xs">
        {row.failed ? `✗ ${row.fail_status ?? ""}` : "✓"}
      </TableCell>
    </TableRow>
  );
});

export default RecentUsageTable;
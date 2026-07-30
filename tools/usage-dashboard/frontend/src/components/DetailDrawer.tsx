import { StatePanel } from "@/components/StatePanel";
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { useRequests } from "@/api/hooks/useRequests";
import { useFilterKey } from "@/stores/filtersStore";
import { useUIStore } from "@/stores/uiStore";
import { useT } from "@/stores/languageStore";

interface DrawerRowData {
  id: number | string;
  request_id?: string;
  timestamp: string;
  account_hash?: string | null;
  model: string;
  endpoint?: string;
  provider?: string;
  total_tokens?: number | null;
  input_tokens?: number | null;
  output_tokens?: number | null;
  reasoning_tokens?: number | null;
  cached_tokens?: number | null;
  cache_read_tokens?: number | null;
  cache_creation_tokens?: number | null;
  latency_ms?: number | null;
  ttft_ms?: number | null;
  failed: number | boolean;
  fail_status: number | null;
  alias?: string | null;
}

interface DrawerResponse {
  requests: DrawerRowData[];
  next_cursor?: string | null;
}

export function DetailDrawer() {
  const id = useUIStore((s) => s.detailDrawerRequestId);
  const close = useUIStore((s) => s.openDetailDrawer);
  const filters = useFilterKey();
  const t = useT();
  const { data, isLoading, error, refetch } = useRequests<DrawerResponse>({ ...filters, limit: 100 });
  const row = data?.requests?.find((r) => String(r.id) === id);

  return (
    <Sheet
      open={id !== null}
      onOpenChange={(o) => !o && close(null)}
    >
      <SheetContent>
        <SheetHeader>
          <SheetTitle>{t("drawer.request_detail")}</SheetTitle>
        </SheetHeader>
        <StatePanel loading={isLoading} error={error} isEmpty={!row} onRetry={() => refetch()}>
          {row && (
            <pre className="overflow-auto whitespace-pre-wrap text-xs text-muted-foreground">
              {JSON.stringify(row, null, 2)}
            </pre>
          )}
        </StatePanel>
      </SheetContent>
    </Sheet>
  );
}
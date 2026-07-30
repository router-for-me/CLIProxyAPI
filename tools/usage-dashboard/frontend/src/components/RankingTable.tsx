import { StatePanel } from "@/components/StatePanel";
import { useSummary } from "@/api/hooks/useSummary";
import { useFilterKey } from "@/stores/filtersStore";
import { useT } from "@/stores/languageStore";
import { formatTokens } from "@/lib/format";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
interface Props {
  onSelect?: (accountHash: string) => void;
}

export default function RankingTable({ onSelect }: Props) {
  const filters = useFilterKey();
  const t = useT();
  const { data, isLoading, error, refetch } = useSummary(filters);
  const rows = data?.accounts ?? [];
  const showCost = data?.price_coverage !== undefined && data?.price_coverage !== "empty";

  if (isLoading) {
    return <StatePanel loading={true} error={null} isEmpty={false} />;
  }

  if (rows.length === 0) {
    return <StatePanel loading={false} error={null} isEmpty={true} />;
  }

  return (
    <StatePanel loading={false} error={error} isEmpty={false} onRetry={() => refetch()}>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{t("col.account")}</TableHead>
            <TableHead className="text-right">{t("col.requests")}</TableHead>
            <TableHead className="text-right">{t("col.tokens")}</TableHead>
            {showCost && <TableHead className="text-right">{t("col.cost")}</TableHead>}
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((a, i) => (
            <TableRow
              key={i}
              className="cursor-pointer"
              onClick={() => onSelect?.(a.account)}
            >
              <TableCell>{a.account}</TableCell>
              <TableCell className="text-right">{a.requests}</TableCell>
              <TableCell className="text-right">
                {formatTokens(a.total_tokens)}
              </TableCell>
              {showCost && <TableCell className="text-right">—</TableCell>}
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </StatePanel>
  );
}
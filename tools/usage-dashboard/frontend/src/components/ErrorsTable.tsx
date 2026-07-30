import { StatePanel } from "@/components/StatePanel";
import { useErrors } from "@/api/hooks/useErrors";
import { useFilterKey } from "@/stores/filtersStore";
import { useT } from "@/stores/languageStore";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
interface ErrorRow {
  fail_status: number;
  model: string;
  count: number;
  percentage: number;
  last_seen: string;
}

interface ErrorsResponse {
  range: string;
  models_filter: string[];
  accounts_filter: string[];
  errors: ErrorRow[];
}

interface Props {
  onDrillDown?: (filter: { model?: string }) => void;
}

export default function ErrorsTable({ onDrillDown }: Props) {
  const filters = useFilterKey();
  const t = useT();
  const { data: raw, isLoading, error, refetch } = useErrors(filters);
  const data = raw as unknown as ErrorsResponse | undefined;
  const rows = data?.errors ?? [];

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
            <TableHead>{t("col.status_code")}</TableHead>
            <TableHead>{t("col.model")}</TableHead>
            <TableHead className="text-right">{t("col.count")}</TableHead>
            <TableHead className="text-right">{t("col.percentage")}</TableHead>
            <TableHead>{t("col.last_seen")}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((e, i) => (
            <TableRow
              key={i}
              className="cursor-pointer"
              onClick={() => onDrillDown?.({ model: e.model })}
            >
              <TableCell>{e.fail_status}</TableCell>
              <TableCell>{e.model}</TableCell>
              <TableCell className="text-right">{e.count}</TableCell>
              <TableCell className="text-right">
                {e.percentage.toFixed(1)}%
              </TableCell>
              <TableCell>{e.last_seen}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </StatePanel>
  );
}
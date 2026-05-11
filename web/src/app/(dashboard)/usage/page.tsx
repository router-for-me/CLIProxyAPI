"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { api } from "@/lib/api";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
import { toast } from "sonner";
import {
  Activity,
  ArrowDownToLine,
  ArrowUpFromLine,
  AlertTriangle,
  RefreshCw,
  Download,
  Upload,
  ArrowUpDown,
  ArrowUp,
  ArrowDown,
} from "lucide-react";

interface ModelUsage {
  request_count: number;
  input_tokens: number;
  output_tokens: number;
}

interface UsageData {
  usage: { [model: string]: ModelUsage };
  failed_requests: number;
}

type SortKey = "model" | "request_count" | "input_tokens" | "output_tokens";
type SortDir = "asc" | "desc";

function formatNumber(n: number | undefined | null): string {
  return (n ?? 0).toLocaleString();
}

function SummaryCardSkeleton() {
  return (
    <Card>
      <CardHeader>
        <Skeleton className="h-5 w-28" />
      </CardHeader>
      <CardContent>
        <Skeleton className="h-8 w-20" />
      </CardContent>
    </Card>
  );
}

function TableSkeleton() {
  return (
    <>
      {Array.from({ length: 5 }).map((_, i) => (
        <TableRow key={i}>
          <TableCell>
            <Skeleton className="h-4 w-32" />
          </TableCell>
          <TableCell>
            <Skeleton className="h-4 w-16 ml-auto" />
          </TableCell>
          <TableCell>
            <Skeleton className="h-4 w-20 ml-auto" />
          </TableCell>
          <TableCell>
            <Skeleton className="h-4 w-20 ml-auto" />
          </TableCell>
        </TableRow>
      ))}
    </>
  );
}

export default function UsagePage() {
  const [data, setData] = useState<UsageData | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const [sortKey, setSortKey] = useState<SortKey>("request_count");
  const [sortDir, setSortDir] = useState<SortDir>("desc");
  const [importOpen, setImportOpen] = useState(false);
  const [importFile, setImportFile] = useState<File | null>(null);
  const [importing, setImporting] = useState(false);

  const fetchData = useCallback(async () => {
    setIsLoading(true);
    try {
      const res = await api.usage.getUsageStatistics();
      setData(res as unknown as UsageData);
    } catch {
      toast.error("Failed to load usage statistics");
    } finally {
      setIsLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchData();
  }, [fetchData]);

  const modelEntries = useMemo(() => {
    if (!data?.usage) return [];
    return Object.entries(data.usage).map(([model, stats]) => ({
      model,
      request_count: stats?.request_count ?? 0,
      input_tokens: stats?.input_tokens ?? 0,
      output_tokens: stats?.output_tokens ?? 0,
    }));
  }, [data]);

  const totals = useMemo(() => {
    return modelEntries.reduce(
      (acc, entry) => ({
        totalRequests: acc.totalRequests + entry.request_count,
        inputTokens: acc.inputTokens + entry.input_tokens,
        outputTokens: acc.outputTokens + entry.output_tokens,
      }),
      { totalRequests: 0, inputTokens: 0, outputTokens: 0 },
    );
  }, [modelEntries]);

  const sortedEntries = useMemo(() => {
    const sorted = [...modelEntries].sort((a, b) => {
      let cmp: number;
      if (sortKey === "model") {
        cmp = a.model.localeCompare(b.model);
      } else {
        cmp = a[sortKey] - b[sortKey];
      }
      return sortDir === "asc" ? cmp : -cmp;
    });
    return sorted;
  }, [modelEntries, sortKey, sortDir]);

  const handleSort = (key: SortKey) => {
    if (sortKey === key) {
      setSortDir((d) => (d === "asc" ? "desc" : "asc"));
    } else {
      setSortKey(key);
      setSortDir(key === "model" ? "asc" : "desc");
    }
  };

  const SortIcon = ({ column }: { column: SortKey }) => {
    if (sortKey !== column) {
      return <ArrowUpDown className="size-3 opacity-50" />;
    }
    return sortDir === "asc" ? (
      <ArrowUp className="size-3" />
    ) : (
      <ArrowDown className="size-3" />
    );
  };

  const handleExport = async () => {
    try {
      const blob = await api.usage.exportUsageStatistics();
      const url = URL.createObjectURL(
        blob instanceof Blob ? blob : new Blob([JSON.stringify(blob)]),
      );
      const a = document.createElement("a");
      a.href = url;
      a.download = `usage-export-${new Date().toISOString().slice(0, 10)}.json`;
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
      URL.revokeObjectURL(url);
      toast.success("Usage data exported");
    } catch {
      toast.error("Failed to export usage data");
    }
  };

  const handleImport = async () => {
    if (!importFile) return;
    setImporting(true);
    try {
      const text = await importFile.text();
      const parsed = JSON.parse(text);
      const result = await api.usage.importUsageStatistics(parsed);
      toast.success(
        `Import complete: ${(result as Record<string, unknown>).added ?? 0} added, ${(result as Record<string, unknown>).skipped ?? 0} skipped`,
      );
      setImportOpen(false);
      setImportFile(null);
      fetchData();
    } catch {
      toast.error("Failed to import usage data");
    } finally {
      setImporting(false);
    }
  };

  const failedRequests = data?.failed_requests ?? 0;

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-semibold">Usage</h1>
        <div className="flex items-center gap-2">
          <Button variant="outline" size="sm" onClick={fetchData}>
            <RefreshCw className="size-4" />
            Refresh
          </Button>
          <Button variant="outline" size="sm" onClick={handleExport}>
            <Download className="size-4" />
            Export
          </Button>
          <Dialog open={importOpen} onOpenChange={setImportOpen}>
            <DialogTrigger asChild>
              <Button variant="outline" size="sm">
                <Upload className="size-4" />
                Import
              </Button>
            </DialogTrigger>
            <DialogContent>
              <DialogHeader>
                <DialogTitle>Import Usage Data</DialogTitle>
                <DialogDescription>
                  Select a JSON file to import usage statistics.
                </DialogDescription>
              </DialogHeader>
              <Input
                type="file"
                accept=".json"
                onChange={(e) =>
                  setImportFile(e.target.files?.[0] ?? null)
                }
              />
              <DialogFooter>
                <Button
                  variant="outline"
                  onClick={() => setImportOpen(false)}
                  disabled={importing}
                >
                  Cancel
                </Button>
                <Button onClick={handleImport} disabled={!importFile || importing}>
                  {importing ? "Importing..." : "Import"}
                </Button>
              </DialogFooter>
            </DialogContent>
          </Dialog>
        </div>
      </div>

      <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-4">
        {isLoading ? (
          <>
            <SummaryCardSkeleton />
            <SummaryCardSkeleton />
            <SummaryCardSkeleton />
            <SummaryCardSkeleton />
          </>
        ) : (
          <>
            <Card>
              <CardHeader>
                <div className="flex items-center gap-2">
                  <Activity className="size-5 text-muted-foreground" />
                  <CardTitle>Total Requests</CardTitle>
                </div>
              </CardHeader>
              <CardContent>
                <span className="text-2xl font-semibold tabular-nums">
                  {formatNumber(totals.totalRequests)}
                </span>
              </CardContent>
            </Card>

            <Card>
              <CardHeader>
                <div className="flex items-center gap-2">
                  <ArrowDownToLine className="size-5 text-muted-foreground" />
                  <CardTitle>Input Tokens</CardTitle>
                </div>
              </CardHeader>
              <CardContent>
                <span className="text-2xl font-semibold tabular-nums">
                  {formatNumber(totals.inputTokens)}
                </span>
              </CardContent>
            </Card>

            <Card>
              <CardHeader>
                <div className="flex items-center gap-2">
                  <ArrowUpFromLine className="size-5 text-muted-foreground" />
                  <CardTitle>Output Tokens</CardTitle>
                </div>
              </CardHeader>
              <CardContent>
                <span className="text-2xl font-semibold tabular-nums">
                  {formatNumber(totals.outputTokens)}
                </span>
              </CardContent>
            </Card>

            <Card>
              <CardHeader>
                <div className="flex items-center gap-2">
                  <AlertTriangle
                    className={cn(
                      "size-5",
                      failedRequests > 0
                        ? "text-destructive"
                        : "text-muted-foreground",
                    )}
                  />
                  <CardTitle>Failed Requests</CardTitle>
                </div>
              </CardHeader>
              <CardContent>
                <span
                  className={cn(
                    "text-2xl font-semibold tabular-nums",
                    failedRequests > 0 && "text-destructive",
                  )}
                >
                  {formatNumber(failedRequests)}
                </span>
                {failedRequests > 0 && (
                  <Badge variant="destructive" className="ml-2">
                    Errors
                  </Badge>
                )}
              </CardContent>
            </Card>
          </>
        )}
      </div>

      <Card>
        <CardContent className="pt-4">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>
                  <button
                    type="button"
                    className="inline-flex items-center gap-1 hover:underline"
                    onClick={() => handleSort("model")}
                  >
                    Model
                    <SortIcon column="model" />
                  </button>
                </TableHead>
                <TableHead className="text-right">
                  <button
                    type="button"
                    className="inline-flex items-center gap-1 hover:underline ml-auto"
                    onClick={() => handleSort("request_count")}
                  >
                    Requests
                    <SortIcon column="request_count" />
                  </button>
                </TableHead>
                <TableHead className="text-right">
                  <button
                    type="button"
                    className="inline-flex items-center gap-1 hover:underline ml-auto"
                    onClick={() => handleSort("input_tokens")}
                  >
                    Input Tokens
                    <SortIcon column="input_tokens" />
                  </button>
                </TableHead>
                <TableHead className="text-right">
                  <button
                    type="button"
                    className="inline-flex items-center gap-1 hover:underline ml-auto"
                    onClick={() => handleSort("output_tokens")}
                  >
                    Output Tokens
                    <SortIcon column="output_tokens" />
                  </button>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {isLoading ? (
                <TableSkeleton />
              ) : sortedEntries.length === 0 ? (
                <TableRow>
                  <TableCell
                    colSpan={4}
                    className="h-24 text-center text-muted-foreground"
                  >
                    No usage data available
                  </TableCell>
                </TableRow>
              ) : (
                sortedEntries.map((entry) => (
                  <TableRow key={entry.model}>
                    <TableCell className="font-medium">
                      {entry.model}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      {formatNumber(entry.request_count)}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      {formatNumber(entry.input_tokens)}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      {formatNumber(entry.output_tokens)}
                    </TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        </CardContent>
      </Card>
    </div>
  );
}

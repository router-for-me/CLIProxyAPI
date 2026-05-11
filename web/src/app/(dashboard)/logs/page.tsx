"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { api, type ApplicationLogs, type ErrorLogFile } from "@/lib/api";
import { cn } from "@/lib/utils";
import { toast } from "sonner";

import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { ScrollArea } from "@/components/ui/scroll-area";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Badge } from "@/components/ui/badge";
import { Separator } from "@/components/ui/separator";

import {
  FileText,
  Trash2,
  RefreshCw,
  Download,
  AlertTriangle,
  Terminal,
  Bug,
  Info,
} from "lucide-react";

function getLogLevel(line: string): "ERROR" | "WARN" | "INFO" | "DEBUG" | "OTHER" {
  const upper = line.toUpperCase();
  if (upper.includes("ERROR") || upper.includes("ERR")) return "ERROR";
  if (upper.includes("WARN") || upper.includes("WARNING")) return "WARN";
  if (upper.includes("INFO")) return "INFO";
  if (upper.includes("DEBUG") || upper.includes("DBG")) return "DEBUG";
  return "OTHER";
}

function logLevelColor(level: string): string {
  switch (level) {
    case "ERROR":
      return "text-red-500";
    case "WARN":
      return "text-yellow-500";
    case "DEBUG":
      return "text-muted-foreground";
    default:
      return "text-foreground";
  }
}

function formatFileSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function formatDate(dateStr: string): string {
  try {
    return new Date(dateStr).toLocaleString();
  } catch {
    return dateStr;
  }
}

function ApplicationLogsTab() {
  const [logs, setLogs] = useState<string[]>([]);
  const [lineCount, setLineCount] = useState(0);
  const [latestTimestamp, setLatestTimestamp] = useState<number | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const [isClearing, setIsClearing] = useState(false);
  const scrollRef = useRef<HTMLDivElement>(null);
  const isAutoScroll = useRef(true);
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const fetchLogs = useCallback(async (after?: number) => {
    try {
      const data: ApplicationLogs = await api.logs.getLogs(after);
      if (after != null) {
        setLogs((prev) => [...prev, ...data.lines]);
      } else {
        setLogs(data.lines);
      }
      setLineCount(data.line_count);
      setLatestTimestamp(data.latest_timestamp);
    } catch (err) {
      toast.error("Failed to fetch logs", {
        description: err instanceof Error ? err.message : "Unknown error",
      });
    } finally {
      setIsLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchLogs();
  }, [fetchLogs]);

  useEffect(() => {
    pollRef.current = setInterval(() => {
      if (latestTimestamp != null) {
        fetchLogs(latestTimestamp);
      }
    }, 3000);
    return () => {
      if (pollRef.current) clearInterval(pollRef.current);
    };
  }, [latestTimestamp, fetchLogs]);

  useEffect(() => {
    if (isAutoScroll.current && scrollRef.current) {
      const viewport = scrollRef.current.querySelector(
        "[data-radix-scroll-area-viewport]"
      );
      if (viewport) {
        viewport.scrollTop = viewport.scrollHeight;
      }
    }
  }, [logs]);

  const handleScroll = useCallback((e: React.UIEvent<HTMLDivElement>) => {
    const target = e.currentTarget;
    const atBottom =
      Math.abs(target.scrollHeight - target.scrollTop - target.clientHeight) < 10;
    isAutoScroll.current = atBottom;
  }, []);

  const handleClearLogs = useCallback(async () => {
    setIsClearing(true);
    try {
      await api.logs.deleteLogs();
      setLogs([]);
      setLineCount(0);
      setLatestTimestamp(null);
      toast.success("Logs cleared");
    } catch (err) {
      toast.error("Failed to clear logs", {
        description: err instanceof Error ? err.message : "Unknown error",
      });
    } finally {
      setIsClearing(false);
    }
  }, []);

  if (isLoading) {
    return (
      <div className="flex flex-col gap-3">
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-[400px] w-full" />
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <Badge variant="secondary">{lineCount} lines</Badge>
        </div>
        <AlertDialog>
          <AlertDialogTrigger asChild>
            <Button variant="destructive" size="sm" disabled={isClearing}>
              <Trash2 />
              Clear Logs
            </Button>
          </AlertDialogTrigger>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>Clear all logs?</AlertDialogTitle>
              <AlertDialogDescription>
                This action cannot be undone. All application logs will be
                permanently deleted.
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>Cancel</AlertDialogCancel>
              <AlertDialogAction className="bg-destructive/10 text-destructive hover:bg-destructive/20" onClick={handleClearLogs}>
                Clear Logs
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </div>
      <ScrollArea
        ref={scrollRef}
        className="h-[500px] rounded-md border bg-muted/30"
        onScrollCapture={handleScroll}
      >
        <div className="p-2 font-mono text-xs leading-5">
          {logs.length === 0 ? (
            <div className="flex items-center justify-center py-12 text-muted-foreground">
              No logs available
            </div>
          ) : (
            logs.map((line, i) => {
              const level = getLogLevel(line);
              return (
                <div
                  key={i}
                  className={cn(
                    "whitespace-pre-wrap break-all",
                    logLevelColor(level)
                  )}
                >
                  {line}
                </div>
              );
            })
          )}
        </div>
      </ScrollArea>
    </div>
  );
}

function ErrorLogsTab() {
  const [files, setFiles] = useState<ErrorLogFile[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [downloading, setDownloading] = useState<string | null>(null);

  const fetchErrorLogs = useCallback(async () => {
    setIsLoading(true);
    try {
      const data = await api.logs.getRequestErrorLogs();
      setFiles(data.files ?? []);
    } catch (err) {
      toast.error("Failed to fetch error logs", {
        description: err instanceof Error ? err.message : "Unknown error",
      });
    } finally {
      setIsLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchErrorLogs();
  }, [fetchErrorLogs]);

  const handleDownload = useCallback(async (name: string) => {
    setDownloading(name);
    try {
      const blob = await api.logs.downloadRequestErrorLog(name);
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = name;
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
      URL.revokeObjectURL(url);
    } catch (err) {
      toast.error("Failed to download error log", {
        description: err instanceof Error ? err.message : "Unknown error",
      });
    } finally {
      setDownloading(null);
    }
  }, []);

  if (isLoading) {
    return (
      <div className="flex flex-col gap-3">
        <Skeleton className="h-8 w-32" />
        <Skeleton className="h-[300px] w-full" />
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center justify-between">
        <Badge variant="secondary">{files.length} files</Badge>
        <Button variant="outline" size="sm" onClick={fetchErrorLogs}>
          <RefreshCw />
          Refresh
        </Button>
      </div>
      {files.length === 0 ? (
        <div className="flex items-center justify-center rounded-md border py-12 text-muted-foreground">
          No error log files found
        </div>
      ) : (
        <div className="rounded-md border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>File Name</TableHead>
                <TableHead>Size</TableHead>
                <TableHead>Modified</TableHead>
                <TableHead className="text-right">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {files.map((file) => (
                <TableRow key={file.name}>
                  <TableCell className="font-mono text-xs">
                    <div className="flex items-center gap-2">
                      <FileText className="size-4 text-muted-foreground" />
                      {file.name}
                    </div>
                  </TableCell>
                  <TableCell>{formatFileSize(file.size)}</TableCell>
                  <TableCell>{formatDate(file.modified)}</TableCell>
                  <TableCell className="text-right">
                    <Button
                      variant="outline"
                      size="xs"
                      onClick={() => handleDownload(file.name)}
                      disabled={downloading === file.name}
                    >
                      <Download />
                      {downloading === file.name ? "Downloading..." : "Download"}
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}
    </div>
  );
}

function RequestLogsTab() {
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const [isToggling, setIsToggling] = useState(false);
  const [requestId, setRequestId] = useState("");
  const [requestLog, setRequestLog] = useState<Record<string, unknown> | null>(null);
  const [isFetchingLog, setIsFetchingLog] = useState(false);

  useEffect(() => {
    api.logs
      .getRequestLogEnabled()
      .then((data) => setEnabled(data))
      .catch((err) => {
        toast.error("Failed to fetch request log status", {
          description: err instanceof Error ? err.message : "Unknown error",
        });
      })
      .finally(() => setIsLoading(false));
  }, []);

  const handleToggle = useCallback(async (value: boolean) => {
    setIsToggling(true);
    try {
      await api.logs.putRequestLog(value);
      setEnabled(value);
      toast.success(value ? "Request logging enabled" : "Request logging disabled");
    } catch (err) {
      toast.error("Failed to update request logging", {
        description: err instanceof Error ? err.message : "Unknown error",
      });
    } finally {
      setIsToggling(false);
    }
  }, []);

  const handleFetchRequestLog = useCallback(async () => {
    if (!requestId.trim()) return;
    setIsFetchingLog(true);
    setRequestLog(null);
    try {
      const data = await api.logs.getRequestLogByID(requestId.trim());
      setRequestLog(data as unknown as Record<string, unknown>);
    } catch (err) {
      toast.error("Failed to fetch request log", {
        description: err instanceof Error ? err.message : "Unknown error",
      });
    } finally {
      setIsFetchingLog(false);
    }
  }, [requestId]);

  if (isLoading) {
    return (
      <div className="flex flex-col gap-3">
        <Skeleton className="h-8 w-64" />
        <Skeleton className="h-[200px] w-full" />
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-4">
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Bug className="size-4" />
            Request Logging
          </CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-4">
          <div className="flex items-center gap-3">
            <Switch
              checked={enabled ?? false}
              onCheckedChange={handleToggle}
              disabled={isToggling}
            />
            <span className="text-sm">
              {enabled ? "Enabled" : "Disabled"}
            </span>
          </div>
          {enabled && (
            <>
              <Separator />
              <div className="flex flex-col gap-2 text-sm text-muted-foreground">
                <p>
                  Request logging is active. Individual request logs can be
                  downloaded by their request ID.
                </p>
              </div>
              <Separator />
              <div className="flex flex-col gap-2">
                <label className="text-sm font-medium">
                  Fetch Request Log by ID
                </label>
                <div className="flex items-center gap-2">
                  <input
                    type="text"
                    value={requestId}
                    onChange={(e) => setRequestId(e.target.value)}
                    placeholder="Enter request ID"
                    className="flex h-8 w-full rounded-md border border-input bg-background px-3 py-1 text-sm shadow-sm transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                    onKeyDown={(e) => {
                      if (e.key === "Enter") handleFetchRequestLog();
                    }}
                  />
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={handleFetchRequestLog}
                    disabled={isFetchingLog || !requestId.trim()}
                  >
                    {isFetchingLog ? (
                      <RefreshCw className="animate-spin" />
                    ) : (
                      <Info />
                    )}
                    Fetch
                  </Button>
                </div>
                {requestLog && (
                  <ScrollArea className="h-[300px] rounded-md border bg-muted/30">
                    <pre className="p-2 font-mono text-xs whitespace-pre-wrap break-all">
                      {JSON.stringify(requestLog, null, 2)}
                    </pre>
                  </ScrollArea>
                )}
              </div>
            </>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

export default function LogsPage() {
  return (
    <div className="flex flex-col gap-6">
      <div className="flex items-center gap-2">
        <Terminal className="size-5" />
        <h1 className="text-xl font-semibold">Logs</h1>
      </div>
      <Tabs defaultValue="application">
        <TabsList>
          <TabsTrigger value="application">
            <FileText className="size-4" />
            Application
          </TabsTrigger>
          <TabsTrigger value="errors">
            <AlertTriangle className="size-4" />
            Error Logs
          </TabsTrigger>
          <TabsTrigger value="requests">
            <Bug className="size-4" />
            Request Logs
          </TabsTrigger>
        </TabsList>
        <TabsContent value="application">
          <ApplicationLogsTab />
        </TabsContent>
        <TabsContent value="errors">
          <ErrorLogsTab />
        </TabsContent>
        <TabsContent value="requests">
          <RequestLogsTab />
        </TabsContent>
      </Tabs>
    </div>
  );
}

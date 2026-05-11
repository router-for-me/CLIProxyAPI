"use client";

import { useEffect, useState } from "react";
import { api, type AuthFile, type LatestVersion } from "@/lib/api";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { Server, Key, Shield, BarChart3, Settings } from "lucide-react";

function CardSkeleton() {
  return (
    <Card>
      <CardHeader>
        <Skeleton className="h-5 w-32" />
        <Skeleton className="h-4 w-48" />
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <Skeleton className="h-4 w-full" />
        <Skeleton className="h-4 w-3/4" />
        <Skeleton className="h-4 w-1/2" />
      </CardContent>
    </Card>
  );
}

export default function DashboardPage() {
  const [version, setVersion] = useState<LatestVersion | null>(null);
  const [authFiles, setAuthFiles] = useState<AuthFile[] | null>(null);
  const [debugMode, setDebugMode] = useState<boolean | null>(null);
  const [proxyURL, setProxyURL] = useState<string | null>(null);
  const [routingStrategy, setRoutingStrategy] = useState<string | null>(null);
  const [requestRetry, setRequestRetry] = useState<number | null>(null);
  const [apiKeysCount, setApiKeysCount] = useState<number | null>(null);
  const [usageEnabled, setUsageEnabled] = useState<boolean | null>(null);
  const [isLoading, setIsLoading] = useState(true);

  useEffect(() => {
    Promise.all([
      api.config.getLatestVersion().catch(() => null),
      api.authFiles.listAuthFiles().catch(() => null),
      api.boolean.getDebug().catch(() => null),
      api.string.getProxyURL().catch(() => null),
      api.routing.getRoutingStrategy().catch(() => null),
      api.integer.getRequestRetry().catch(() => null),
      api.apiKeys.getAPIKeys().then((k) => k.length).catch(() => null),
      api.boolean.getUsageStatisticsEnabled().catch(() => null),
    ])
      .then(
        ([
          versionData,
          authFilesData,
          debugData,
          proxyData,
          routingData,
          retryData,
          keysCount,
          usageData,
        ]) => {
          setVersion(versionData);
          setAuthFiles(authFilesData);
          setDebugMode(debugData);
          setProxyURL(proxyData);
          setRoutingStrategy(routingData);
          setRequestRetry(retryData);
          setApiKeysCount(keysCount);
          setUsageEnabled(usageData);
        },
      )
      .finally(() => setIsLoading(false));
  }, []);

  const providerCounts = authFiles
    ? authFiles.reduce<Record<string, number>>((acc, file) => {
        const provider = file.provider || "unknown";
        acc[provider] = (acc[provider] || 0) + 1;
        return acc;
      }, {})
    : null;

  const activeAuthFiles = authFiles
    ? authFiles.filter((f) => !f.disabled).length
    : 0;
  const disabledAuthFiles = authFiles
    ? authFiles.filter((f) => f.disabled).length
    : 0;

  if (isLoading) {
    return (
      <div className="flex flex-col gap-4">
        <h1 className="text-2xl font-semibold">Dashboard</h1>
        <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
          <CardSkeleton />
          <CardSkeleton />
          <CardSkeleton />
          <CardSkeleton />
          <CardSkeleton />
        </div>
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-4">
      <h1 className="text-2xl font-semibold">Dashboard</h1>

      <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
        <Card>
          <CardHeader>
            <div className="flex items-center gap-2">
              <Server className="size-5 text-muted-foreground" />
              <CardTitle>Server Version</CardTitle>
            </div>
            <CardDescription>Current server version and status</CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            {version ? (
              <>
                <div className="flex items-center gap-2">
                  <span className="text-sm text-muted-foreground">Version</span>
                  <Badge variant="secondary">{version["latest-version"]}</Badge>
                </div>
                <div className="flex items-center gap-2">
                  <span className="text-sm text-muted-foreground">Config</span>
                  <Badge variant="outline">Loaded</Badge>
                </div>
              </>
            ) : (
              <span className="text-sm text-muted-foreground">
                Version info unavailable
              </span>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <div className="flex items-center gap-2">
              <Key className="size-5 text-muted-foreground" />
              <CardTitle>Provider Overview</CardTitle>
            </div>
            <CardDescription>Auth files grouped by provider</CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            {providerCounts && Object.keys(providerCounts).length > 0 ? (
              <>
                <div className="flex flex-wrap gap-2">
                  {Object.entries(providerCounts).map(([provider, count]) => (
                    <Badge key={provider} variant="secondary">
                      {provider}: {count}
                    </Badge>
                  ))}
                </div>
                <div className="flex items-center gap-2 text-sm text-muted-foreground">
                  <span>{activeAuthFiles} active</span>
                  {disabledAuthFiles > 0 && (
                    <span>({disabledAuthFiles} disabled)</span>
                  )}
                </div>
              </>
            ) : (
              <span className="text-sm text-muted-foreground">
                No auth files configured
              </span>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <div className="flex items-center gap-2">
              <Settings className="size-5 text-muted-foreground" />
              <CardTitle>Config Summary</CardTitle>
            </div>
            <CardDescription>Key settings at a glance</CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <div className="flex items-center justify-between">
              <span className="text-sm text-muted-foreground">Debug Mode</span>
              <Badge variant={debugMode ? "default" : "outline"}>
                {debugMode ? "Enabled" : "Disabled"}
              </Badge>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-sm text-muted-foreground">Proxy URL</span>
              <span className="text-sm truncate max-w-[160px]">
                {proxyURL || "Not set"}
              </span>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-sm text-muted-foreground">
                Routing Strategy
              </span>
              <Badge variant="secondary">
                {routingStrategy || "default"}
              </Badge>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-sm text-muted-foreground">
                Request Retry
              </span>
              <span className="text-sm">
                {requestRetry ?? "N/A"}
              </span>
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <div className="flex items-center gap-2">
              <Shield className="size-5 text-muted-foreground" />
              <CardTitle>API Keys</CardTitle>
            </div>
            <CardDescription>Configured API key entries</CardDescription>
          </CardHeader>
          <CardContent>
            <div className="flex items-center gap-2">
              <span className="text-2xl font-semibold">
                {apiKeysCount ?? 0}
              </span>
              <span className="text-sm text-muted-foreground">
                keys configured
              </span>
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <div className="flex items-center gap-2">
              <BarChart3 className="size-5 text-muted-foreground" />
              <CardTitle>Usage Statistics</CardTitle>
            </div>
            <CardDescription>Usage tracking status</CardDescription>
          </CardHeader>
          <CardContent>
            <div className="flex items-center gap-2">
              <Badge variant={usageEnabled ? "default" : "outline"}>
                {usageEnabled ? "Enabled" : "Disabled"}
              </Badge>
            </div>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}

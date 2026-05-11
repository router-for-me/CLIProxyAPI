"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { api, type OAuthProvider, type AuthFile } from "@/lib/api";
import { toast } from "sonner";
import {
  KeyRound,
  ShieldCheck,
  ShieldOff,
  ExternalLink,
  Copy,
} from "lucide-react";

import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { Spinner } from "@/components/ui/spinner";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

interface AuthFlowResult {
  url?: string;
  user_code?: string;
  verification_uri?: string;
  state?: string;
}

const PROVIDER_AUTH_FN: Record<string, (params?: Record<string, string>) => Promise<AuthFlowResult>> = {
  claude: () => api.oauthFlows.requestAnthropicToken(),
  codex: () => api.oauthFlows.requestCodexToken(),
  gemini: (params) => api.oauthFlows.requestGeminiCLIToken(params),
  antigravity: (params) => api.oauthFlows.requestAntigravityToken(params),
  kimi: () => api.oauthFlows.requestKimiToken(),
  kiro: (params) => api.oauthFlows.requestKiroToken(params),
  "github-copilot": () => api.oauthFlows.requestGitHubToken(),
  gitlab: (params) => api.oauthFlows.requestGitLabToken(params),
  cursor: (params) => api.oauthFlows.requestCursorToken(params),
  qoder: (params) => api.oauthFlows.requestQoderToken(params),
  kilo: () => api.oauthFlows.requestKiloToken(),
  iflow: (params) => api.oauthFlows.requestIFlowToken(params),
};

interface ProviderParamDef {
  key: string;
  label: string;
  placeholder?: string;
  required?: boolean;
  secret?: boolean;
}

const PROVIDER_PARAMS: Record<string, ProviderParamDef[]> = {
  gitlab: [
    { key: "client_id", label: "Client ID", placeholder: "GitLab OAuth Application ID", required: true },
    { key: "client_secret", label: "Client Secret", placeholder: "GitLab OAuth Secret (optional)", secret: true },
    { key: "base_url", label: "Base URL", placeholder: "https://gitlab.com (default)" },
  ],
};

const WEB_OAUTH_PROVIDERS: Record<string, { startUrl: string; statusUrl: string }> = {
  codearts: { startUrl: "/v0/oauth/codearts/start", statusUrl: "/v0/oauth/codearts/status" },
};

function flowTypeBadge(authType: string) {
  const variant =
    authType === "browser" || authType === "authorization_code_pkce" || authType === "pkce_polling" || authType === "pkce_custom_uri" ? "default" :
    authType === "device_code" ? "secondary" :
    authType === "google_oauth2" || authType === "aws_builder_id" || authType === "web_oauth" ? "outline" :
    "outline";
  const label =
    authType === "authorization_code_pkce" ? "PKCE" :
    authType === "pkce_polling" ? "PKCE" :
    authType === "pkce_custom_uri" ? "PKCE" :
    authType === "google_oauth2" ? "OAuth2" :
    authType === "aws_builder_id" ? "Builder ID" :
    authType === "web_oauth" ? "Web OAuth" :
    authType === "device_code" ? "Device Code" :
    authType === "token" ? "Token" :
    authType;
  return <Badge variant={variant}>{label}</Badge>;
}

function authStatusBadge(hasAuth: boolean) {
  return hasAuth ? (
    <Badge variant="default" className="gap-1">
      <ShieldCheck className="size-3" />
      Authenticated
    </Badge>
  ) : (
    <Badge variant="outline" className="gap-1 text-muted-foreground">
      <ShieldOff className="size-3" />
      Not Authenticated
    </Badge>
  );
}

export default function OAuthPage() {
  return (
    <div className="flex flex-col gap-6">
      <div className="flex items-center gap-2">
        <KeyRound className="size-5 text-muted-foreground" />
        <h1 className="text-lg font-semibold">OAuth</h1>
      </div>

      <ProvidersTab />
    </div>
  );
}

function ProvidersTab() {
  const [providers, setProviders] = useState<OAuthProvider[]>([]);
  const [authFiles, setAuthFiles] = useState<AuthFile[]>([]);
  const [loading, setLoading] = useState(true);
  const fetchIdRef = useRef(0);

  const [authDialogOpen, setAuthDialogOpen] = useState(false);
  const [authDialogProvider, setAuthDialogProvider] = useState<string>("");
  const [authDialogDisplayName, setAuthDialogDisplayName] = useState<string>("");
  const [authDialogUserCode, setAuthDialogUserCode] = useState<string>("");
  const [authDialogVerificationUri, setAuthDialogVerificationUri] = useState<string>("");
  const [authDialogIsDeviceCode, setAuthDialogIsDeviceCode] = useState(false);
  const [authenticating, setAuthenticating] = useState(false);
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const [paramDialogOpen, setParamDialogOpen] = useState(false);
  const [paramDialogProvider, setParamDialogProvider] = useState<OAuthProvider | null>(null);
  const [paramDialogValues, setParamDialogValues] = useState<Record<string, string>>({});

  const fetchData = useCallback(async () => {
    const fetchId = ++fetchIdRef.current;
    try {
      const [provData, authData] = await Promise.all([
        api.oauth.getOAuthProviders(),
        api.authFiles.listAuthFiles(),
      ]);
      if (fetchId === fetchIdRef.current) {
        setProviders(provData);
        setAuthFiles(authData);
      }
    } catch (err) {
      if (fetchId === fetchIdRef.current) {
        toast.error("Failed to load OAuth providers", {
          description: err instanceof Error ? err.message : undefined,
        });
      }
    } finally {
      if (fetchId === fetchIdRef.current) {
        setLoading(false);
      }
    }
  }, []);

  useEffect(() => {
    fetchData();
  }, [fetchData]);

  const providerHasAuth = useCallback(
    (providerName: string) => {
      return authFiles.some(
        (f) => f.provider === providerName && f.status !== "disabled"
      );
    },
    [authFiles]
  );

  const stopPolling = useCallback(() => {
    if (pollRef.current) {
      clearInterval(pollRef.current);
      pollRef.current = null;
    }
  }, []);

  const startAuthFlow = useCallback(
    async (provider: OAuthProvider, params?: Record<string, string>) => {
      const authFn = PROVIDER_AUTH_FN[provider.key];
      if (!authFn) return;

      setAuthenticating(true);
      setAuthDialogProvider(provider.key);
      setAuthDialogDisplayName(provider.display_name);
      setAuthDialogUserCode("");
      setAuthDialogVerificationUri("");
      setAuthDialogIsDeviceCode(false);
      setAuthDialogOpen(true);

      try {
        const filteredParams = params
          ? Object.fromEntries(Object.entries(params).filter(([, v]) => v.trim() !== ""))
          : undefined;
        const result = await authFn(filteredParams);

        if (result.user_code && result.verification_uri) {
          setAuthDialogIsDeviceCode(true);
          setAuthDialogUserCode(result.user_code);
          setAuthDialogVerificationUri(result.verification_uri);
        } else if (result.url) {
          window.open(result.url, "_blank");
        }

        const state = result.state || "";
        stopPolling();
        pollRef.current = setInterval(async () => {
          try {
            const status = await api.oauthFlows.getAuthStatus(state);
            if (status.status === "ok") {
              stopPolling();
              setAuthDialogOpen(false);
              setAuthenticating(false);
              toast.success(`${provider.display_name} authenticated successfully`);
              await fetchData();
            } else if (status.status === "error") {
              stopPolling();
              setAuthDialogOpen(false);
              setAuthenticating(false);
              toast.error(`Authentication failed for ${provider.display_name}`, {
                description: status.error || status.message,
              });
            }
          } catch {
            // continue polling on transient errors
          }
        }, 2000);
      } catch (err) {
        setAuthDialogOpen(false);
        setAuthenticating(false);
        toast.error(`Failed to start auth flow for ${provider.display_name}`, {
          description: err instanceof Error ? err.message : undefined,
        });
      }
    },
    [fetchData, stopPolling]
  );

  const startWebOAuthFlow = useCallback(
    async (provider: OAuthProvider, config: { startUrl: string; statusUrl: string }) => {
      setAuthenticating(true);
      setAuthDialogProvider(provider.key);
      setAuthDialogDisplayName(provider.display_name);
      setAuthDialogUserCode("");
      setAuthDialogVerificationUri("");
      setAuthDialogIsDeviceCode(false);
      setAuthDialogOpen(true);

      try {
        const resp = await fetch(config.startUrl, { headers: { Accept: "application/json" } });
        const data = await resp.json();
        if (!resp.ok || data.error) {
          throw new Error(data.error || `HTTP ${resp.status}`);
        }

        if (data.url) {
          window.open(data.url, "_blank");
        }

        const state = data.state || "";
        stopPolling();
        pollRef.current = setInterval(async () => {
          try {
            const statusResp = await fetch(`${config.statusUrl}?state=${state}`);
            const status = await statusResp.json();
            if (status.status === "success") {
              stopPolling();
              setAuthDialogOpen(false);
              setAuthenticating(false);
              toast.success(`${provider.display_name} authenticated successfully`, {
                description: status.message,
              });
              await fetchData();
            } else if (status.status === "failed") {
              stopPolling();
              setAuthDialogOpen(false);
              setAuthenticating(false);
              toast.error(`Authentication failed for ${provider.display_name}`, {
                description: status.error,
              });
            }
          } catch {
            // continue polling on transient errors
          }
        }, 3000);
      } catch (err) {
        setAuthDialogOpen(false);
        setAuthenticating(false);
        toast.error(`Failed to start ${provider.display_name} auth flow`, {
          description: err instanceof Error ? err.message : undefined,
        });
      }
    },
    [fetchData, stopPolling]
  );

  const handleLogin = useCallback(
    (provider: OAuthProvider) => {
      const webOAuth = WEB_OAUTH_PROVIDERS[provider.key];
      if (webOAuth) {
        startWebOAuthFlow(provider, webOAuth);
        return;
      }

      const authFn = PROVIDER_AUTH_FN[provider.key];
      if (!authFn) {
        if (provider.flow_type === "token") {
          toast.info(`${provider.display_name} requires manual token configuration via CLI or config file`);
        } else {
          toast.error(`No auth flow configured for ${provider.display_name}`);
        }
        return;
      }

      const requiredParams = PROVIDER_PARAMS[provider.key];
      if (requiredParams?.some((p) => p.required)) {
        setParamDialogProvider(provider);
        setParamDialogValues({});
        setParamDialogOpen(true);
        return;
      }

      startAuthFlow(provider);
    },
    [startAuthFlow, startWebOAuthFlow]
  );

  const handleParamSubmit = useCallback(() => {
    if (!paramDialogProvider) return;
    const params = PROVIDER_PARAMS[paramDialogProvider.key];
    const missing = params?.filter((p) => p.required && !paramDialogValues[p.key]?.trim());
    if (missing && missing.length > 0) {
      toast.error(`Required field: ${missing.map((p) => p.label).join(", ")}`);
      return;
    }
    setParamDialogOpen(false);
    startAuthFlow(paramDialogProvider, paramDialogValues);
  }, [paramDialogProvider, paramDialogValues, startAuthFlow]);

  const handleDialogClose = useCallback(
    (open: boolean) => {
      if (!open) {
        stopPolling();
        setAuthenticating(false);
        setAuthDialogOpen(false);
      }
    },
    [stopPolling]
  );

  const handleCopyCode = useCallback((code: string) => {
    navigator.clipboard.writeText(code).then(
      () => toast.success("Code copied to clipboard"),
      () => toast.error("Failed to copy code")
    );
  }, []);

  useEffect(() => {
    return () => stopPolling();
  }, [stopPolling]);

  if (loading) {
    return (
      <div className="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-3">
        {Array.from({ length: 6 }).map((_, i) => (
          <Card key={i}>
            <CardHeader>
              <Skeleton className="h-5 w-28" />
              <Skeleton className="h-4 w-20" />
            </CardHeader>
            <CardContent>
              <Skeleton className="h-4 w-24" />
            </CardContent>
          </Card>
        ))}
      </div>
    );
  }

  if (providers.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center gap-2 rounded-lg border border-dashed p-12 text-center">
        <KeyRound className="size-10 text-muted-foreground/50" />
        <p className="text-sm text-muted-foreground">
          No OAuth providers available.
        </p>
      </div>
    );
  }

  return (
    <>
      <div className="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-3">
        {providers.map((provider) => {
          const hasAuth = providerHasAuth(provider.key);
          return (
            <Card key={provider.key} className={!provider.configured ? "opacity-60" : ""}>
              <CardHeader>
                <div className="flex items-center justify-between">
                  <CardTitle className="text-base">{provider.display_name}</CardTitle>
                  <div className="flex items-center gap-1.5">
                    {!provider.configured && (
                      <Badge variant="outline" className="text-xs">Not Configured</Badge>
                    )}
                    {flowTypeBadge(provider.flow_type)}
                  </div>
                </div>
                <CardDescription className="text-xs text-muted-foreground">
                  {provider.key}
                </CardDescription>
              </CardHeader>
              <CardContent>
                <div className="flex items-center justify-between">
                  {authStatusBadge(hasAuth)}
                  <Button
                    size="sm"
                    variant={provider.flow_type === "token" && !PROVIDER_AUTH_FN[provider.key] ? "outline" : hasAuth ? "outline" : "default"}
                    onClick={() => handleLogin(provider)}
                    disabled={authenticating && authDialogProvider === provider.key}
                  >
                    <ExternalLink />
                    {provider.flow_type === "token" && !PROVIDER_AUTH_FN[provider.key]
                      ? "CLI Setup"
                      : hasAuth
                        ? "Add Account"
                        : "Login"}
                  </Button>
                </div>
              </CardContent>
            </Card>
          );
        })}
      </div>

      <Dialog open={authDialogOpen} onOpenChange={handleDialogClose}>
        <DialogContent className="sm:max-w-md" onInteractOutside={(e) => e.preventDefault()}>
          <DialogHeader>
            <DialogTitle>Authenticating {authDialogDisplayName}</DialogTitle>
            <DialogDescription>
              {authDialogIsDeviceCode
                ? "Visit the verification URL and enter the code below."
                : "Waiting for authentication to complete..."}
            </DialogDescription>
          </DialogHeader>
          <div className="flex flex-col items-center gap-4 py-4">
            <Spinner className="size-8" />
            {authDialogIsDeviceCode ? (
              <div className="flex flex-col items-center gap-3">
                <div className="flex flex-col gap-1 text-center">
                  <span className="text-sm text-muted-foreground">Verification URL</span>
                  <a
                    href={authDialogVerificationUri}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="text-sm font-medium text-primary underline underline-offset-4"
                  >
                    {authDialogVerificationUri}
                  </a>
                </div>
                <div className="flex flex-col gap-1 text-center">
                  <span className="text-sm text-muted-foreground">Code</span>
                  <div className="flex items-center gap-2">
                    <code className="rounded-md bg-muted px-3 py-1.5 text-lg font-mono font-semibold tracking-wider">
                      {authDialogUserCode}
                    </code>
                    <Button
                      variant="outline"
                      size="icon-xs"
                      onClick={() => handleCopyCode(authDialogUserCode)}
                    >
                      <Copy className="size-3" />
                    </Button>
                  </div>
                </div>
              </div>
            ) : (
              <p className="text-sm text-muted-foreground">
                A browser tab has been opened. Complete the login there.
              </p>
            )}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => handleDialogClose(false)}>
              Cancel
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={paramDialogOpen} onOpenChange={setParamDialogOpen}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>Login to {paramDialogProvider?.display_name}</DialogTitle>
            <DialogDescription>
              This provider requires additional credentials to authenticate.
            </DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-4">
            {paramDialogProvider && PROVIDER_PARAMS[paramDialogProvider.key]?.map((param) => (
              <div key={param.key} className="flex flex-col gap-2">
                <Label htmlFor={`param-${param.key}`}>
                  {param.label}
                  {param.required && <span className="text-destructive ml-1">*</span>}
                </Label>
                <Input
                  id={`param-${param.key}`}
                  type={param.secret ? "password" : "text"}
                  placeholder={param.placeholder}
                  value={paramDialogValues[param.key] ?? ""}
                  onChange={(e) =>
                    setParamDialogValues((prev) => ({ ...prev, [param.key]: e.target.value }))
                  }
                />
              </div>
            ))}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setParamDialogOpen(false)}>
              Cancel
            </Button>
            <Button onClick={handleParamSubmit}>
              Continue
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

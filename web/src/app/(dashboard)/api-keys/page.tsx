"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import {
  api,
  type GeminiKey,
  type ClaudeKey,
  type CodexKey,
  type VertexKey,
  type OpenAICompatEntry,
  type OpenAICompatAPIKeyEntry,
  type AmpModelMapping,
  type AmpUpstreamAPIKeyEntry,
} from "@/lib/api";
import { toast } from "sonner";
import {
  Key,
  Eye,
  EyeOff,
  Plus,
  Trash2,
  Pencil,
  Upload,
  X,
  Globe,
  Bot,
  Sparkles,
  Cloud,
  Layers,
  Code2,
  ArrowRight,
  Save,
  Eraser,
} from "lucide-react";

import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from "@/components/ui/tabs";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";

function maskKey(key: string): string {
  if (!key) return "—";
  if (key.length <= 8) return "****";
  return key.slice(0, 4) + "****" + key.slice(-4);
}

function MaskedKeyCell({ value }: { value: string }) {
  const [revealed, setRevealed] = useState(false);
  return (
    <div className="flex items-center gap-2">
      <code className="text-xs font-mono">
        {revealed ? value : maskKey(value)}
      </code>
      <Button
        variant="ghost"
        size="icon-xs"
        onClick={() => setRevealed(!revealed)}
        aria-label={revealed ? "Hide key" : "Reveal key"}
      >
        {revealed ? <EyeOff /> : <Eye />}
      </Button>
    </div>
  );
}

function MaskedValue({ value }: { value: string }) {
  const [revealed, setRevealed] = useState(false);
  if (!value) return <span className="text-muted-foreground">—</span>;
  return (
    <div className="flex items-center gap-2">
      <code className="text-xs font-mono">
        {revealed ? value : maskKey(value)}
      </code>
      <Button
        variant="ghost"
        size="icon-xs"
        onClick={() => setRevealed(!revealed)}
        aria-label={revealed ? "Hide value" : "Reveal value"}
      >
        {revealed ? <EyeOff /> : <Eye />}
      </Button>
    </div>
  );
}

interface HeaderEntry {
  key: string;
  value: string;
}

interface ModelAliasEntry {
  name: string;
  alias: string;
}

interface ProviderKeyFormData {
  apiKey: string;
  prefix: string;
  baseUrl: string;
  proxyUrl: string;
  priority: string;
  models: ModelAliasEntry[];
  headers: HeaderEntry[];
  excludedModels: string;
  websockets: boolean;
}

function emptyProviderForm(): ProviderKeyFormData {
  return {
    apiKey: "",
    prefix: "",
    baseUrl: "",
    proxyUrl: "",
    priority: "",
    models: [],
    headers: [],
    excludedModels: "",
    websockets: false,
  };
}

function geminiToForm(k: GeminiKey): ProviderKeyFormData {
  return {
    apiKey: k["api-key"] ?? "",
    prefix: k.prefix ?? "",
    baseUrl: k["base-url"] ?? "",
    proxyUrl: k["proxy-url"] ?? "",
    priority: k.priority != null ? String(k.priority) : "",
    models: (k.models ?? []).map((m) => ({ name: m.name, alias: m.alias })),
    headers: k.headers
      ? Object.entries(k.headers).map(([key, value]) => ({ key, value }))
      : [],
    excludedModels: (k["excluded-models"] ?? []).join(", "),
    websockets: false,
  };
}

function claudeToForm(k: ClaudeKey): ProviderKeyFormData {
  return {
    apiKey: k["api-key"] ?? "",
    prefix: k.prefix ?? "",
    baseUrl: k["base-url"] ?? "",
    proxyUrl: k["proxy-url"] ?? "",
    priority: k.priority != null ? String(k.priority) : "",
    models: (k.models ?? []).map((m) => ({ name: m.name, alias: m.alias })),
    headers: k.headers
      ? Object.entries(k.headers).map(([key, value]) => ({ key, value }))
      : [],
    excludedModels: (k["excluded-models"] ?? []).join(", "),
    websockets: false,
  };
}

function codexToForm(k: CodexKey): ProviderKeyFormData {
  return {
    apiKey: k["api-key"] ?? "",
    prefix: k.prefix ?? "",
    baseUrl: k["base-url"] ?? "",
    proxyUrl: k["proxy-url"] ?? "",
    priority: k.priority != null ? String(k.priority) : "",
    models: (k.models ?? []).map((m) => ({ name: m.name, alias: m.alias })),
    headers: k.headers
      ? Object.entries(k.headers).map(([key, value]) => ({ key, value }))
      : [],
    excludedModels: (k["excluded-models"] ?? []).join(", "),
    websockets: k.websockets ?? false,
  };
}

function vertexToForm(k: VertexKey): ProviderKeyFormData {
  return {
    apiKey: k["api-key"] ?? "",
    prefix: k.prefix ?? "",
    baseUrl: k["base-url"] ?? "",
    proxyUrl: k["proxy-url"] ?? "",
    priority: k.priority != null ? String(k.priority) : "",
    models: (k.models ?? []).map((m) => ({ name: m.name, alias: m.alias })),
    headers: k.headers
      ? Object.entries(k.headers).map(([key, value]) => ({ key, value }))
      : [],
    excludedModels: (k["excluded-models"] ?? []).join(", "),
    websockets: false,
  };
}

function formToGemini(f: ProviderKeyFormData): Partial<GeminiKey> {
  const obj: Partial<GeminiKey> = { "api-key": f.apiKey };
  if (f.prefix) obj.prefix = f.prefix;
  if (f.baseUrl) obj["base-url"] = f.baseUrl;
  if (f.proxyUrl) obj["proxy-url"] = f.proxyUrl;
  if (f.priority) obj.priority = Number(f.priority);
  if (f.models.length > 0) obj.models = f.models.map((m) => ({ name: m.name, alias: m.alias }));
  if (f.headers.length > 0) {
    const h: Record<string, string> = {};
    for (const e of f.headers) {
      if (e.key.trim()) h[e.key] = e.value;
    }
    if (Object.keys(h).length > 0) obj.headers = h;
  }
  if (f.excludedModels.trim()) {
    obj["excluded-models"] = f.excludedModels.split(",").map((s) => s.trim()).filter(Boolean);
  }
  return obj;
}

function formToClaude(f: ProviderKeyFormData): Partial<ClaudeKey> {
  const obj: Partial<ClaudeKey> = { "api-key": f.apiKey };
  if (f.prefix) obj.prefix = f.prefix;
  if (f.baseUrl) obj["base-url"] = f.baseUrl;
  if (f.proxyUrl) obj["proxy-url"] = f.proxyUrl;
  if (f.priority) obj.priority = Number(f.priority);
  if (f.models.length > 0) obj.models = f.models.map((m) => ({ name: m.name, alias: m.alias }));
  if (f.headers.length > 0) {
    const h: Record<string, string> = {};
    for (const e of f.headers) {
      if (e.key.trim()) h[e.key] = e.value;
    }
    if (Object.keys(h).length > 0) obj.headers = h;
  }
  if (f.excludedModels.trim()) {
    obj["excluded-models"] = f.excludedModels.split(",").map((s) => s.trim()).filter(Boolean);
  }
  return obj;
}

function formToCodex(f: ProviderKeyFormData): Partial<CodexKey> {
  const obj: Partial<CodexKey> = { "api-key": f.apiKey, websockets: f.websockets };
  if (f.prefix) obj.prefix = f.prefix;
  if (f.baseUrl) obj["base-url"] = f.baseUrl;
  if (f.proxyUrl) obj["proxy-url"] = f.proxyUrl;
  if (f.priority) obj.priority = Number(f.priority);
  if (f.models.length > 0) obj.models = f.models.map((m) => ({ name: m.name, alias: m.alias }));
  if (f.headers.length > 0) {
    const h: Record<string, string> = {};
    for (const e of f.headers) {
      if (e.key.trim()) h[e.key] = e.value;
    }
    if (Object.keys(h).length > 0) obj.headers = h;
  }
  if (f.excludedModels.trim()) {
    obj["excluded-models"] = f.excludedModels.split(",").map((s) => s.trim()).filter(Boolean);
  }
  return obj;
}

function formToVertex(f: ProviderKeyFormData): Partial<VertexKey> {
  const obj: Partial<VertexKey> = { "api-key": f.apiKey };
  if (f.prefix) obj.prefix = f.prefix;
  if (f.baseUrl) obj["base-url"] = f.baseUrl;
  if (f.proxyUrl) obj["proxy-url"] = f.proxyUrl;
  if (f.priority) obj.priority = Number(f.priority);
  if (f.models.length > 0) obj.models = f.models.map((m) => ({ name: m.name, alias: m.alias }));
  if (f.headers.length > 0) {
    const h: Record<string, string> = {};
    for (const e of f.headers) {
      if (e.key.trim()) h[e.key] = e.value;
    }
    if (Object.keys(h).length > 0) obj.headers = h;
  }
  if (f.excludedModels.trim()) {
    obj["excluded-models"] = f.excludedModels.split(",").map((s) => s.trim()).filter(Boolean);
  }
  return obj;
}

interface OpenAICompatFormData {
  name: string;
  prefix: string;
  baseUrl: string;
  apiKeyEntries: { apiKey: string; proxyUrl: string }[];
  models: ModelAliasEntry[];
  headers: HeaderEntry[];
  priority: string;
}

function emptyOpenAICompatForm(): OpenAICompatFormData {
  return {
    name: "",
    prefix: "",
    baseUrl: "",
    apiKeyEntries: [{ apiKey: "", proxyUrl: "" }],
    models: [],
    headers: [],
    priority: "",
  };
}

function openAICompatToForm(e: OpenAICompatEntry): OpenAICompatFormData {
  return {
    name: e.name ?? "",
    prefix: e.prefix ?? "",
    baseUrl: e["base-url"] ?? "",
    apiKeyEntries:
      (e["api-key-entries"] ?? []).map((a) => ({
        apiKey: a["api-key"] ?? "",
        proxyUrl: a["proxy-url"] ?? "",
      })).length > 0
        ? (e["api-key-entries"] ?? []).map((a) => ({
            apiKey: a["api-key"] ?? "",
            proxyUrl: a["proxy-url"] ?? "",
          }))
        : [{ apiKey: "", proxyUrl: "" }],
    models: (e.models ?? []).map((m) => ({ name: m.name, alias: m.alias })),
    headers: e.headers
      ? Object.entries(e.headers).map(([key, value]) => ({ key, value }))
      : [],
    priority: e.priority != null ? String(e.priority) : "",
  };
}

function formToOpenAICompat(f: OpenAICompatFormData): Partial<OpenAICompatEntry> {
  const obj: Partial<OpenAICompatEntry> = { name: f.name, "base-url": f.baseUrl };
  if (f.prefix) obj.prefix = f.prefix;
  if (f.priority) obj.priority = Number(f.priority);
  const cleanEntries = f.apiKeyEntries.filter((a) => a.apiKey.trim() !== "");
  if (cleanEntries.length > 0) {
    obj["api-key-entries"] = cleanEntries.map((a) => {
      const entry: OpenAICompatAPIKeyEntry = { "api-key": a.apiKey };
      if (a.proxyUrl) entry["proxy-url"] = a.proxyUrl;
      return entry;
    });
  }
  if (f.models.length > 0) obj.models = f.models.map((m) => ({ name: m.name, alias: m.alias }));
  if (f.headers.length > 0) {
    const h: Record<string, string> = {};
    for (const e of f.headers) {
      if (e.key.trim()) h[e.key] = e.value;
    }
    if (Object.keys(h).length > 0) obj.headers = h;
  }
  return obj;
}

function ProviderKeyForm({
  form,
  setForm,
  showWebsockets,
}: {
  form: ProviderKeyFormData;
  setForm: React.Dispatch<React.SetStateAction<ProviderKeyFormData>>;
  showWebsockets?: boolean;
}) {
  const updateField = (field: keyof ProviderKeyFormData, value: string | boolean) => {
    setForm((prev) => ({ ...prev, [field]: value }));
  };

  const addModel = () => {
    setForm((prev) => ({
      ...prev,
      models: [...prev.models, { name: "", alias: "" }],
    }));
  };

  const removeModel = (index: number) => {
    setForm((prev) => ({
      ...prev,
      models: prev.models.filter((_, i) => i !== index),
    }));
  };

  const updateModel = (index: number, field: "name" | "alias", value: string) => {
    setForm((prev) => ({
      ...prev,
      models: prev.models.map((m, i) => (i === index ? { ...m, [field]: value } : m)),
    }));
  };

  const addHeader = () => {
    setForm((prev) => ({
      ...prev,
      headers: [...prev.headers, { key: "", value: "" }],
    }));
  };

  const removeHeader = (index: number) => {
    setForm((prev) => ({
      ...prev,
      headers: prev.headers.filter((_, i) => i !== index),
    }));
  };

  const updateHeader = (index: number, field: "key" | "value", value: string) => {
    setForm((prev) => ({
      ...prev,
      headers: prev.headers.map((h, i) => (i === index ? { ...h, [field]: value } : h)),
    }));
  };

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-col gap-2">
        <Label htmlFor="pk-api-key">API Key</Label>
        <Input
          id="pk-api-key"
          value={form.apiKey}
          onChange={(e) => updateField("apiKey", e.target.value)}
          placeholder="Enter API key"
        />
      </div>
      <div className="flex flex-col gap-2">
        <Label htmlFor="pk-prefix">Prefix</Label>
        <Input
          id="pk-prefix"
          value={form.prefix}
          onChange={(e) => updateField("prefix", e.target.value)}
          placeholder="Model prefix"
        />
      </div>
      <div className="flex flex-col gap-2">
        <Label htmlFor="pk-base-url">Base URL</Label>
        <Input
          id="pk-base-url"
          value={form.baseUrl}
          onChange={(e) => updateField("baseUrl", e.target.value)}
          placeholder="https://api.example.com"
        />
      </div>
      <div className="flex flex-col gap-2">
        <Label htmlFor="pk-proxy-url">Proxy URL</Label>
        <Input
          id="pk-proxy-url"
          value={form.proxyUrl}
          onChange={(e) => updateField("proxyUrl", e.target.value)}
          placeholder="https://proxy.example.com"
        />
      </div>
      <div className="flex flex-col gap-2">
        <Label htmlFor="pk-priority">Priority</Label>
        <Input
          id="pk-priority"
          type="number"
          value={form.priority}
          onChange={(e) => updateField("priority", e.target.value)}
          placeholder="0"
        />
      </div>
      {showWebsockets && (
        <div className="flex items-center gap-2">
          <Switch
            size="sm"
            checked={form.websockets}
            onCheckedChange={(checked) => updateField("websockets", checked)}
          />
          <Label>Use WebSockets</Label>
        </div>
      )}
      <div className="flex flex-col gap-2">
        <Label>Model Aliases</Label>
        <div className="flex flex-col gap-2">
          {form.models.map((m, i) => (
            <div key={i} className="flex items-center gap-2">
              <Input
                value={m.name}
                onChange={(e) => updateModel(i, "name", e.target.value)}
                placeholder="Model name"
                className="flex-1"
              />
              <Input
                value={m.alias}
                onChange={(e) => updateModel(i, "alias", e.target.value)}
                placeholder="Alias"
                className="flex-1"
              />
              <Button
                variant="ghost"
                size="icon-xs"
                onClick={() => removeModel(i)}
              >
                <X />
              </Button>
            </div>
          ))}
          <Button variant="outline" size="xs" onClick={addModel} className="self-start">
            <Plus />
            Add Model Alias
          </Button>
        </div>
      </div>
      <div className="flex flex-col gap-2">
        <Label>Headers</Label>
        <div className="flex flex-col gap-2">
          {form.headers.map((h, i) => (
            <div key={i} className="flex items-center gap-2">
              <Input
                value={h.key}
                onChange={(e) => updateHeader(i, "key", e.target.value)}
                placeholder="Header name"
                className="flex-1"
              />
              <Input
                value={h.value}
                onChange={(e) => updateHeader(i, "value", e.target.value)}
                placeholder="Header value"
                className="flex-1"
              />
              <Button
                variant="ghost"
                size="icon-xs"
                onClick={() => removeHeader(i)}
              >
                <X />
              </Button>
            </div>
          ))}
          <Button variant="outline" size="xs" onClick={addHeader} className="self-start">
            <Plus />
            Add Header
          </Button>
        </div>
      </div>
      <div className="flex flex-col gap-2">
        <Label htmlFor="pk-excluded-models">Excluded Models</Label>
        <Input
          id="pk-excluded-models"
          value={form.excludedModels}
          onChange={(e) => updateField("excludedModels", e.target.value)}
          placeholder="model1, model2, ..."
        />
      </div>
    </div>
  );
}

function OpenAICompatForm({
  form,
  setForm,
}: {
  form: OpenAICompatFormData;
  setForm: React.Dispatch<React.SetStateAction<OpenAICompatFormData>>;
}) {
  const updateField = (field: keyof OpenAICompatFormData, value: string) => {
    setForm((prev) => ({ ...prev, [field]: value }));
  };

  const addApiKeyEntry = () => {
    setForm((prev) => ({
      ...prev,
      apiKeyEntries: [...prev.apiKeyEntries, { apiKey: "", proxyUrl: "" }],
    }));
  };

  const removeApiKeyEntry = (index: number) => {
    setForm((prev) => ({
      ...prev,
      apiKeyEntries: prev.apiKeyEntries.filter((_, i) => i !== index),
    }));
  };

  const updateApiKeyEntry = (index: number, field: "apiKey" | "proxyUrl", value: string) => {
    setForm((prev) => ({
      ...prev,
      apiKeyEntries: prev.apiKeyEntries.map((e, i) =>
        i === index ? { ...e, [field]: value } : e
      ),
    }));
  };

  const addModel = () => {
    setForm((prev) => ({
      ...prev,
      models: [...prev.models, { name: "", alias: "" }],
    }));
  };

  const removeModel = (index: number) => {
    setForm((prev) => ({
      ...prev,
      models: prev.models.filter((_, i) => i !== index),
    }));
  };

  const updateModel = (index: number, field: "name" | "alias", value: string) => {
    setForm((prev) => ({
      ...prev,
      models: prev.models.map((m, i) => (i === index ? { ...m, [field]: value } : m)),
    }));
  };

  const addHeader = () => {
    setForm((prev) => ({
      ...prev,
      headers: [...prev.headers, { key: "", value: "" }],
    }));
  };

  const removeHeader = (index: number) => {
    setForm((prev) => ({
      ...prev,
      headers: prev.headers.filter((_, i) => i !== index),
    }));
  };

  const updateHeader = (index: number, field: "key" | "value", value: string) => {
    setForm((prev) => ({
      ...prev,
      headers: prev.headers.map((h, i) => (i === index ? { ...h, [field]: value } : h)),
    }));
  };

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-col gap-2">
        <Label htmlFor="oac-name">Name</Label>
        <Input
          id="oac-name"
          value={form.name}
          onChange={(e) => updateField("name", e.target.value)}
          placeholder="Provider name"
        />
      </div>
      <div className="flex flex-col gap-2">
        <Label htmlFor="oac-prefix">Prefix</Label>
        <Input
          id="oac-prefix"
          value={form.prefix}
          onChange={(e) => updateField("prefix", e.target.value)}
          placeholder="Model prefix"
        />
      </div>
      <div className="flex flex-col gap-2">
        <Label htmlFor="oac-base-url">Base URL</Label>
        <Input
          id="oac-base-url"
          value={form.baseUrl}
          onChange={(e) => updateField("baseUrl", e.target.value)}
          placeholder="https://api.example.com"
        />
      </div>
      <div className="flex flex-col gap-2">
        <Label htmlFor="oac-priority">Priority</Label>
        <Input
          id="oac-priority"
          type="number"
          value={form.priority}
          onChange={(e) => updateField("priority", e.target.value)}
          placeholder="0"
        />
      </div>
      <div className="flex flex-col gap-2">
        <Label>API Key Entries</Label>
        <div className="flex flex-col gap-2">
          {form.apiKeyEntries.map((entry, i) => (
            <div key={i} className="flex items-center gap-2">
              <Input
                value={entry.apiKey}
                onChange={(e) => updateApiKeyEntry(i, "apiKey", e.target.value)}
                placeholder="API key"
                className="flex-1"
              />
              <Input
                value={entry.proxyUrl}
                onChange={(e) => updateApiKeyEntry(i, "proxyUrl", e.target.value)}
                placeholder="Proxy URL (optional)"
                className="flex-1"
              />
              <Button
                variant="ghost"
                size="icon-xs"
                onClick={() => removeApiKeyEntry(i)}
                disabled={form.apiKeyEntries.length <= 1}
              >
                <X />
              </Button>
            </div>
          ))}
          <Button variant="outline" size="xs" onClick={addApiKeyEntry} className="self-start">
            <Plus />
            Add API Key Entry
          </Button>
        </div>
      </div>
      <div className="flex flex-col gap-2">
        <Label>Model Aliases</Label>
        <div className="flex flex-col gap-2">
          {form.models.map((m, i) => (
            <div key={i} className="flex items-center gap-2">
              <Input
                value={m.name}
                onChange={(e) => updateModel(i, "name", e.target.value)}
                placeholder="Model name"
                className="flex-1"
              />
              <Input
                value={m.alias}
                onChange={(e) => updateModel(i, "alias", e.target.value)}
                placeholder="Alias"
                className="flex-1"
              />
              <Button
                variant="ghost"
                size="icon-xs"
                onClick={() => removeModel(i)}
              >
                <X />
              </Button>
            </div>
          ))}
          <Button variant="outline" size="xs" onClick={addModel} className="self-start">
            <Plus />
            Add Model Alias
          </Button>
        </div>
      </div>
      <div className="flex flex-col gap-2">
        <Label>Headers</Label>
        <div className="flex flex-col gap-2">
          {form.headers.map((h, i) => (
            <div key={i} className="flex items-center gap-2">
              <Input
                value={h.key}
                onChange={(e) => updateHeader(i, "key", e.target.value)}
                placeholder="Header name"
                className="flex-1"
              />
              <Input
                value={h.value}
                onChange={(e) => updateHeader(i, "value", e.target.value)}
                placeholder="Header value"
                className="flex-1"
              />
              <Button
                variant="ghost"
                size="icon-xs"
                onClick={() => removeHeader(i)}
              >
                <X />
              </Button>
            </div>
          ))}
          <Button variant="outline" size="xs" onClick={addHeader} className="self-start">
            <Plus />
            Add Header
          </Button>
        </div>
      </div>
    </div>
  );
}

function TableSkeleton({ cols }: { cols: number }) {
  return (
    <>
      {Array.from({ length: 3 }).map((_, i) => (
        <TableRow key={i}>
          {Array.from({ length: cols }).map((_, j) => (
            <TableCell key={j}>
              <Skeleton className="h-5 w-20" />
            </TableCell>
          ))}
        </TableRow>
      ))}
    </>
  );
}

function EmptyState({ icon, message }: { icon: React.ReactNode; message: string }) {
  return (
    <div className="flex flex-col items-center justify-center gap-2 rounded-lg border border-dashed p-12 text-center">
      <div className="text-muted-foreground/50">{icon}</div>
      <p className="text-sm text-muted-foreground">{message}</p>
    </div>
  );
}

const DELETE_ACTION_CLASS =
  "bg-destructive/10 text-destructive hover:bg-destructive/20 focus-visible:border-destructive/40 focus-visible:ring-destructive/20 dark:bg-destructive/20 dark:hover:bg-destructive/30 dark:focus-visible:ring-destructive/40";

export default function APIKeysPage() {
  // ─── Tab 1: API Keys ───
  const [apiKeys, setApiKeys] = useState<string[]>([]);
  const [apiKeysLoading, setApiKeysLoading] = useState(true);
  const [addKeyOpen, setAddKeyOpen] = useState(false);
  const [newKeyValue, setNewKeyValue] = useState("");
  const [addKeySaving, setAddKeySaving] = useState(false);
  const [deleteKeyTarget, setDeleteKeyTarget] = useState<{ index: number; value: string } | null>(null);
  const [deleteKeySaving, setDeleteKeySaving] = useState(false);

  const fetchAPIKeys = useCallback(async () => {
    try {
      const data = await api.apiKeys.getAPIKeys();
      setApiKeys(data);
    } catch (err) {
      toast.error("Failed to load API keys", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setApiKeysLoading(false);
    }
  }, []);

  const handleAddKey = async () => {
    if (!newKeyValue.trim()) return;
    setAddKeySaving(true);
    try {
      await api.apiKeys.patchAPIKeys({ value: newKeyValue.trim() });
      toast.success("API key added");
      setNewKeyValue("");
      setAddKeyOpen(false);
      await fetchAPIKeys();
    } catch (err) {
      toast.error("Failed to add API key", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setAddKeySaving(false);
    }
  };

  const handleDeleteKey = async () => {
    if (!deleteKeyTarget) return;
    setDeleteKeySaving(true);
    try {
      await api.apiKeys.deleteAPIKeys({ index: deleteKeyTarget.index });
      toast.success("API key deleted");
      setDeleteKeyTarget(null);
      await fetchAPIKeys();
    } catch (err) {
      toast.error("Failed to delete API key", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setDeleteKeySaving(false);
    }
  };

  // ─── Tab 2: Gemini Keys ───
  const [geminiKeys, setGeminiKeys] = useState<GeminiKey[]>([]);
  const [geminiLoading, setGeminiLoading] = useState(true);
  const [geminiFormOpen, setGeminiFormOpen] = useState(false);
  const [geminiEditIndex, setGeminiEditIndex] = useState<number | null>(null);
  const [geminiForm, setGeminiForm] = useState<ProviderKeyFormData>(emptyProviderForm());
  const [geminiSaving, setGeminiSaving] = useState(false);
  const [deleteGeminiTarget, setDeleteGeminiTarget] = useState<{ index: number; key: GeminiKey } | null>(null);
  const [deleteGeminiSaving, setDeleteGeminiSaving] = useState(false);

  const fetchGeminiKeys = useCallback(async () => {
    try {
      const data = await api.geminiKeys.getGeminiKeys();
      setGeminiKeys(data);
    } catch (err) {
      toast.error("Failed to load Gemini keys", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setGeminiLoading(false);
    }
  }, []);

  const openGeminiAdd = () => {
    setGeminiEditIndex(null);
    setGeminiForm(emptyProviderForm());
    setGeminiFormOpen(true);
  };

  const openGeminiEdit = (index: number, key: GeminiKey) => {
    setGeminiEditIndex(index);
    setGeminiForm(geminiToForm(key));
    setGeminiFormOpen(true);
  };

  const handleGeminiSave = async () => {
    if (!geminiForm.apiKey.trim()) {
      toast.error("API key is required");
      return;
    }
    setGeminiSaving(true);
    try {
      const value = formToGemini(geminiForm) as GeminiKey;
      if (geminiEditIndex !== null) {
        await api.geminiKeys.patchGeminiKey({ index: geminiEditIndex, value });
        toast.success("Gemini key updated");
      } else {
        const current = await api.geminiKeys.getGeminiKeys();
        await api.geminiKeys.putGeminiKeys([...current, value]);
        toast.success("Gemini key added");
      }
      setGeminiFormOpen(false);
      await fetchGeminiKeys();
    } catch (err) {
      toast.error("Failed to save Gemini key", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setGeminiSaving(false);
    }
  };

  const handleDeleteGemini = async () => {
    if (!deleteGeminiTarget) return;
    setDeleteGeminiSaving(true);
    try {
      await api.geminiKeys.deleteGeminiKey({ index: deleteGeminiTarget.index });
      toast.success("Gemini key deleted");
      setDeleteGeminiTarget(null);
      await fetchGeminiKeys();
    } catch (err) {
      toast.error("Failed to delete Gemini key", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setDeleteGeminiSaving(false);
    }
  };

  // ─── Tab 3: Claude Keys ───
  const [claudeKeys, setClaudeKeys] = useState<ClaudeKey[]>([]);
  const [claudeLoading, setClaudeLoading] = useState(true);
  const [claudeFormOpen, setClaudeFormOpen] = useState(false);
  const [claudeEditIndex, setClaudeEditIndex] = useState<number | null>(null);
  const [claudeForm, setClaudeForm] = useState<ProviderKeyFormData>(emptyProviderForm());
  const [claudeSaving, setClaudeSaving] = useState(false);
  const [deleteClaudeTarget, setDeleteClaudeTarget] = useState<{ index: number; key: ClaudeKey } | null>(null);
  const [deleteClaudeSaving, setDeleteClaudeSaving] = useState(false);

  const fetchClaudeKeys = useCallback(async () => {
    try {
      const data = await api.claudeKeys.getClaudeKeys();
      setClaudeKeys(data);
    } catch (err) {
      toast.error("Failed to load Claude keys", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setClaudeLoading(false);
    }
  }, []);

  const openClaudeAdd = () => {
    setClaudeEditIndex(null);
    setClaudeForm(emptyProviderForm());
    setClaudeFormOpen(true);
  };

  const openClaudeEdit = (index: number, key: ClaudeKey) => {
    setClaudeEditIndex(index);
    setClaudeForm(claudeToForm(key));
    setClaudeFormOpen(true);
  };

  const handleClaudeSave = async () => {
    if (!claudeForm.apiKey.trim()) {
      toast.error("API key is required");
      return;
    }
    setClaudeSaving(true);
    try {
      const value = formToClaude(claudeForm) as ClaudeKey;
      if (claudeEditIndex !== null) {
        await api.claudeKeys.patchClaudeKey({ index: claudeEditIndex, value });
        toast.success("Claude key updated");
      } else {
        const current = await api.claudeKeys.getClaudeKeys();
        await api.claudeKeys.putClaudeKeys([...current, value]);
        toast.success("Claude key added");
      }
      setClaudeFormOpen(false);
      await fetchClaudeKeys();
    } catch (err) {
      toast.error("Failed to save Claude key", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setClaudeSaving(false);
    }
  };

  const handleDeleteClaude = async () => {
    if (!deleteClaudeTarget) return;
    setDeleteClaudeSaving(true);
    try {
      await api.claudeKeys.deleteClaudeKey({ index: deleteClaudeTarget.index });
      toast.success("Claude key deleted");
      setDeleteClaudeTarget(null);
      await fetchClaudeKeys();
    } catch (err) {
      toast.error("Failed to delete Claude key", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setDeleteClaudeSaving(false);
    }
  };

  // ─── Tab 4: Codex Keys ───
  const [codexKeys, setCodexKeys] = useState<CodexKey[]>([]);
  const [codexLoading, setCodexLoading] = useState(true);
  const [codexFormOpen, setCodexFormOpen] = useState(false);
  const [codexEditIndex, setCodexEditIndex] = useState<number | null>(null);
  const [codexForm, setCodexForm] = useState<ProviderKeyFormData>(emptyProviderForm());
  const [codexSaving, setCodexSaving] = useState(false);
  const [deleteCodexTarget, setDeleteCodexTarget] = useState<{ index: number; key: CodexKey } | null>(null);
  const [deleteCodexSaving, setDeleteCodexSaving] = useState(false);

  const fetchCodexKeys = useCallback(async () => {
    try {
      const data = await api.codexKeys.getCodexKeys();
      setCodexKeys(data);
    } catch (err) {
      toast.error("Failed to load Codex keys", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setCodexLoading(false);
    }
  }, []);

  const openCodexAdd = () => {
    setCodexEditIndex(null);
    setCodexForm(emptyProviderForm());
    setCodexFormOpen(true);
  };

  const openCodexEdit = (index: number, key: CodexKey) => {
    setCodexEditIndex(index);
    setCodexForm(codexToForm(key));
    setCodexFormOpen(true);
  };

  const handleCodexSave = async () => {
    if (!codexForm.apiKey.trim()) {
      toast.error("API key is required");
      return;
    }
    setCodexSaving(true);
    try {
      const value = formToCodex(codexForm) as CodexKey;
      if (codexEditIndex !== null) {
        await api.codexKeys.patchCodexKey({ index: codexEditIndex, value });
        toast.success("Codex key updated");
      } else {
        const current = await api.codexKeys.getCodexKeys();
        await api.codexKeys.putCodexKeys([...current, value]);
        toast.success("Codex key added");
      }
      setCodexFormOpen(false);
      await fetchCodexKeys();
    } catch (err) {
      toast.error("Failed to save Codex key", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setCodexSaving(false);
    }
  };

  const handleDeleteCodex = async () => {
    if (!deleteCodexTarget) return;
    setDeleteCodexSaving(true);
    try {
      await api.codexKeys.deleteCodexKey({ index: deleteCodexTarget.index });
      toast.success("Codex key deleted");
      setDeleteCodexTarget(null);
      await fetchCodexKeys();
    } catch (err) {
      toast.error("Failed to delete Codex key", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setDeleteCodexSaving(false);
    }
  };

  // ─── Tab 5: Vertex Keys ───
  const [vertexKeys, setVertexKeys] = useState<VertexKey[]>([]);
  const [vertexLoading, setVertexLoading] = useState(true);
  const [vertexFormOpen, setVertexFormOpen] = useState(false);
  const [vertexEditIndex, setVertexEditIndex] = useState<number | null>(null);
  const [vertexForm, setVertexForm] = useState<ProviderKeyFormData>(emptyProviderForm());
  const [vertexSaving, setVertexSaving] = useState(false);
  const [deleteVertexTarget, setDeleteVertexTarget] = useState<{ index: number; key: VertexKey } | null>(null);
  const [deleteVertexSaving, setDeleteVertexSaving] = useState(false);
  const [vertexImportOpen, setVertexImportOpen] = useState(false);
  const [vertexImporting, setVertexImporting] = useState(false);
  const vertexFileRef = useRef<HTMLInputElement>(null);

  const fetchVertexKeys = useCallback(async () => {
    try {
      const data = await api.vertexKeys.getVertexKeys();
      setVertexKeys(data);
    } catch (err) {
      toast.error("Failed to load Vertex keys", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setVertexLoading(false);
    }
  }, []);

  const openVertexAdd = () => {
    setVertexEditIndex(null);
    setVertexForm(emptyProviderForm());
    setVertexFormOpen(true);
  };

  const openVertexEdit = (index: number, key: VertexKey) => {
    setVertexEditIndex(index);
    setVertexForm(vertexToForm(key));
    setVertexFormOpen(true);
  };

  const handleVertexSave = async () => {
    if (!vertexForm.apiKey.trim()) {
      toast.error("API key is required");
      return;
    }
    setVertexSaving(true);
    try {
      const value = formToVertex(vertexForm) as VertexKey;
      if (vertexEditIndex !== null) {
        await api.vertexKeys.patchVertexKey({ index: vertexEditIndex, value });
        toast.success("Vertex key updated");
      } else {
        const current = await api.vertexKeys.getVertexKeys();
        await api.vertexKeys.putVertexKeys([...current, value]);
        toast.success("Vertex key added");
      }
      setVertexFormOpen(false);
      await fetchVertexKeys();
    } catch (err) {
      toast.error("Failed to save Vertex key", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setVertexSaving(false);
    }
  };

  const handleDeleteVertex = async () => {
    if (!deleteVertexTarget) return;
    setDeleteVertexSaving(true);
    try {
      await api.vertexKeys.deleteVertexKey({ index: deleteVertexTarget.index });
      toast.success("Vertex key deleted");
      setDeleteVertexTarget(null);
      await fetchVertexKeys();
    } catch (err) {
      toast.error("Failed to delete Vertex key", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setDeleteVertexSaving(false);
    }
  };

  const handleVertexImport = async () => {
    const file = vertexFileRef.current?.files?.[0];
    if (!file) return;
    setVertexImporting(true);
    try {
      const text = await file.text();
      const json = JSON.parse(text) as Record<string, unknown>;
      const projectId = json.project_id as string | undefined;
      const privateKey = json.private_key as string | undefined;
      const clientEmail = json.client_email as string | undefined;
      if (!projectId || !privateKey || !clientEmail) {
        toast.error("Invalid service account file: missing project_id, private_key, or client_email");
        return;
      }
      await api.vertexImport({ project_id: projectId, private_key: privateKey, client_email: clientEmail });
      toast.success("Vertex credentials imported");
      setVertexImportOpen(false);
      if (vertexFileRef.current) vertexFileRef.current.value = "";
      await fetchVertexKeys();
    } catch (err) {
      toast.error("Vertex import failed", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setVertexImporting(false);
    }
  };

  // ─── Tab 6: OpenAI Compatibility ───
  const [openAICompat, setOpenAICompat] = useState<OpenAICompatEntry[]>([]);
  const [oacLoading, setOACLoading] = useState(true);
  const [oacFormOpen, setOACFormOpen] = useState(false);
  const [oacEditIndex, setOACEditIndex] = useState<number | null>(null);
  const [oacForm, setOACForm] = useState<OpenAICompatFormData>(emptyOpenAICompatForm());
  const [oacSaving, setOACSaving] = useState(false);
  const [deleteOACTarget, setDeleteOACTarget] = useState<{ index: number; entry: OpenAICompatEntry } | null>(null);
  const [deleteOACSaving, setDeleteOACSaving] = useState(false);

  const fetchOpenAICompat = useCallback(async () => {
    try {
      const data = await api.openAICompat.getOpenAICompat();
      setOpenAICompat(data);
    } catch (err) {
      toast.error("Failed to load OpenAI compatibility entries", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setOACLoading(false);
    }
  }, []);

  const openOACAdd = () => {
    setOACEditIndex(null);
    setOACForm(emptyOpenAICompatForm());
    setOACFormOpen(true);
  };

  const openOACEdit = (index: number, entry: OpenAICompatEntry) => {
    setOACEditIndex(index);
    setOACForm(openAICompatToForm(entry));
    setOACFormOpen(true);
  };

  const handleOACSave = async () => {
    if (!oacForm.name.trim()) {
      toast.error("Name is required");
      return;
    }
    if (!oacForm.baseUrl.trim()) {
      toast.error("Base URL is required");
      return;
    }
    setOACSaving(true);
    try {
      const value = formToOpenAICompat(oacForm) as OpenAICompatEntry;
      if (oacEditIndex !== null) {
        await api.openAICompat.patchOpenAICompat({ index: oacEditIndex, value });
        toast.success("OpenAI compatibility entry updated");
      } else {
        const current = await api.openAICompat.getOpenAICompat();
        await api.openAICompat.putOpenAICompat([...current, value]);
        toast.success("OpenAI compatibility entry added");
      }
      setOACFormOpen(false);
      await fetchOpenAICompat();
    } catch (err) {
      toast.error("Failed to save OpenAI compatibility entry", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setOACSaving(false);
    }
  };

  const handleDeleteOAC = async () => {
    if (!deleteOACTarget) return;
    setDeleteOACSaving(true);
    try {
      await api.openAICompat.deleteOpenAICompat({ index: deleteOACTarget.index });
      toast.success("OpenAI compatibility entry deleted");
      setDeleteOACTarget(null);
      await fetchOpenAICompat();
    } catch (err) {
      toast.error("Failed to delete OpenAI compatibility entry", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setDeleteOACSaving(false);
    }
  };

  // ─── Tab 7: AmpCode ───
  const [ampUpstreamURL, setAmpUpstreamURL] = useState("");
  const [ampUpstreamAPIKey, setAmpUpstreamAPIKey] = useState("");
  const [ampRestrictLocalhost, setAmpRestrictLocalhost] = useState(false);
  const [ampForceMappings, setAmpForceMappings] = useState(false);
  const [ampModelMappings, setAmpModelMappings] = useState<AmpModelMapping[]>([]);
  const [ampUpstreamAPIKeys, setAmpUpstreamAPIKeys] = useState<AmpUpstreamAPIKeyEntry[]>([]);
  const [ampLoading, setAmpLoading] = useState(true);
  const [ampUpstreamURLEdit, setAmpUpstreamURLEdit] = useState("");
  const [ampUpstreamAPIKeyEdit, setAmpUpstreamAPIKeyEdit] = useState("");
  const [ampSavingURL, setAmpSavingURL] = useState(false);
  const [ampSavingAPIKey, setAmpSavingAPIKey] = useState(false);
  const [ampSavingSwitch, setAmpSavingSwitch] = useState(false);
  const [ampMappingFormOpen, setAmpMappingFormOpen] = useState(false);
  const [ampMappingEditIndex, setAmpMappingEditIndex] = useState<number | null>(null);
  const [ampMappingFrom, setAmpMappingFrom] = useState("");
  const [ampMappingTo, setAmpMappingTo] = useState("");
  const [ampMappingSaving, setAmpMappingSaving] = useState(false);
  const [deleteAmpMappingTarget, setDeleteAmpMappingTarget] = useState<number | null>(null);
  const [deleteAmpMappingSaving, setDeleteAmpMappingSaving] = useState(false);
  const [ampUpstreamKeyFormOpen, setAmpUpstreamKeyFormOpen] = useState(false);
  const [ampUpstreamKeyEditIndex, setAmpUpstreamKeyEditIndex] = useState<number | null>(null);
  const [ampUpstreamKeyValue, setAmpUpstreamKeyValue] = useState("");
  const [ampUpstreamKeyApiKeys, setAmpUpstreamKeyApiKeys] = useState("");
  const [ampUpstreamKeySaving, setAmpUpstreamKeySaving] = useState(false);
  const [deleteAmpUpstreamKeyTarget, setDeleteAmpUpstreamKeyTarget] = useState<number | null>(null);
  const [deleteAmpUpstreamKeySaving, setDeleteAmpUpstreamKeySaving] = useState(false);

  const fetchAmpCode = useCallback(async () => {
    try {
      const [url, apiKey, restrict, force, mappings, upstreamKeys] = await Promise.all([
        api.ampCode.getAmpUpstreamURL(),
        api.ampCode.getAmpUpstreamAPIKey(),
        api.ampCode.getAmpRestrictManagementToLocalhost(),
        api.ampCode.getAmpForceModelMappings(),
        api.ampCode.getAmpModelMappings(),
        api.ampCode.getAmpUpstreamAPIKeys(),
      ]);
      setAmpUpstreamURL(url);
      setAmpUpstreamURLEdit(url);
      setAmpUpstreamAPIKey(apiKey);
      setAmpUpstreamAPIKeyEdit(apiKey);
      setAmpRestrictLocalhost(restrict);
      setAmpForceMappings(force);
      setAmpModelMappings(mappings);
      setAmpUpstreamAPIKeys(upstreamKeys);
    } catch (err) {
      toast.error("Failed to load AmpCode settings", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setAmpLoading(false);
    }
  }, []);

  const handleSaveAmpURL = async () => {
    setAmpSavingURL(true);
    try {
      if (ampUpstreamURLEdit.trim()) {
        await api.ampCode.putAmpUpstreamURL(ampUpstreamURLEdit.trim());
      } else {
        await api.ampCode.deleteAmpUpstreamURL();
      }
      setAmpUpstreamURL(ampUpstreamURLEdit.trim());
      toast.success("Upstream URL saved");
    } catch (err) {
      toast.error("Failed to save upstream URL", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setAmpSavingURL(false);
    }
  };

  const handleClearAmpURL = async () => {
    setAmpSavingURL(true);
    try {
      await api.ampCode.deleteAmpUpstreamURL();
      setAmpUpstreamURL("");
      setAmpUpstreamURLEdit("");
      toast.success("Upstream URL cleared");
    } catch (err) {
      toast.error("Failed to clear upstream URL", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setAmpSavingURL(false);
    }
  };

  const handleSaveAmpAPIKey = async () => {
    setAmpSavingAPIKey(true);
    try {
      if (ampUpstreamAPIKeyEdit.trim()) {
        await api.ampCode.putAmpUpstreamAPIKey(ampUpstreamAPIKeyEdit.trim());
      } else {
        await api.ampCode.deleteAmpUpstreamAPIKey();
      }
      setAmpUpstreamAPIKey(ampUpstreamAPIKeyEdit.trim());
      toast.success("Upstream API key saved");
    } catch (err) {
      toast.error("Failed to save upstream API key", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setAmpSavingAPIKey(false);
    }
  };

  const handleClearAmpAPIKey = async () => {
    setAmpSavingAPIKey(true);
    try {
      await api.ampCode.deleteAmpUpstreamAPIKey();
      setAmpUpstreamAPIKey("");
      setAmpUpstreamAPIKeyEdit("");
      toast.success("Upstream API key cleared");
    } catch (err) {
      toast.error("Failed to clear upstream API key", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setAmpSavingAPIKey(false);
    }
  };

  const handleAmpRestrictToggle = async (checked: boolean) => {
    const prev = ampRestrictLocalhost;
    setAmpRestrictLocalhost(checked);
    setAmpSavingSwitch(true);
    try {
      await api.ampCode.putAmpRestrictManagementToLocalhost(checked);
      toast.success(checked ? "Restrict management to localhost enabled" : "Restrict management to localhost disabled");
    } catch (err) {
      setAmpRestrictLocalhost(prev);
      toast.error("Failed to toggle setting", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setAmpSavingSwitch(false);
    }
  };

  const handleAmpForceMappingsToggle = async (checked: boolean) => {
    const prev = ampForceMappings;
    setAmpForceMappings(checked);
    setAmpSavingSwitch(true);
    try {
      await api.ampCode.putAmpForceModelMappings(checked);
      toast.success(checked ? "Force model mappings enabled" : "Force model mappings disabled");
    } catch (err) {
      setAmpForceMappings(prev);
      toast.error("Failed to toggle setting", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setAmpSavingSwitch(false);
    }
  };

  const openAmpMappingAdd = () => {
    setAmpMappingEditIndex(null);
    setAmpMappingFrom("");
    setAmpMappingTo("");
    setAmpMappingFormOpen(true);
  };

  const openAmpMappingEdit = (index: number) => {
    setAmpMappingEditIndex(index);
    setAmpMappingFrom(ampModelMappings[index].from);
    setAmpMappingTo(ampModelMappings[index].to);
    setAmpMappingFormOpen(true);
  };

  const handleAmpMappingSave = async () => {
    if (!ampMappingFrom.trim() || !ampMappingTo.trim()) {
      toast.error("Both from and to fields are required");
      return;
    }
    setAmpMappingSaving(true);
    try {
      let newMappings: AmpModelMapping[];
      if (ampMappingEditIndex !== null) {
        newMappings = ampModelMappings.map((m, i) =>
          i === ampMappingEditIndex ? { from: ampMappingFrom.trim(), to: ampMappingTo.trim() } : m
        );
      } else {
        newMappings = [...ampModelMappings, { from: ampMappingFrom.trim(), to: ampMappingTo.trim() }];
      }
      await api.ampCode.putAmpModelMappings(newMappings);
      setAmpModelMappings(newMappings);
      toast.success(ampMappingEditIndex !== null ? "Model mapping updated" : "Model mapping added");
      setAmpMappingFormOpen(false);
    } catch (err) {
      toast.error("Failed to save model mapping", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setAmpMappingSaving(false);
    }
  };

  const handleDeleteAmpMapping = async () => {
    if (deleteAmpMappingTarget === null) return;
    setDeleteAmpMappingSaving(true);
    try {
      const fromKey = ampModelMappings[deleteAmpMappingTarget].from;
      await api.ampCode.deleteAmpModelMappings([fromKey]);
      setAmpModelMappings(ampModelMappings.filter((_, i) => i !== deleteAmpMappingTarget));
      toast.success("Model mapping deleted");
      setDeleteAmpMappingTarget(null);
    } catch (err) {
      toast.error("Failed to delete model mapping", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setDeleteAmpMappingSaving(false);
    }
  };

  const openAmpUpstreamKeyAdd = () => {
    setAmpUpstreamKeyEditIndex(null);
    setAmpUpstreamKeyValue("");
    setAmpUpstreamKeyApiKeys("");
    setAmpUpstreamKeyFormOpen(true);
  };

  const openAmpUpstreamKeyEdit = (index: number) => {
    setAmpUpstreamKeyEditIndex(index);
    setAmpUpstreamKeyValue(ampUpstreamAPIKeys[index]["upstream-api-key"]);
    setAmpUpstreamKeyApiKeys(ampUpstreamAPIKeys[index]["api-keys"].join(", "));
    setAmpUpstreamKeyFormOpen(true);
  };

  const handleAmpUpstreamKeySave = async () => {
    if (!ampUpstreamKeyValue.trim()) {
      toast.error("Upstream API key is required");
      return;
    }
    setAmpUpstreamKeySaving(true);
    try {
      const apiKeysList = ampUpstreamKeyApiKeys
        .split(",")
        .map((s) => s.trim())
        .filter(Boolean);
      const entry: AmpUpstreamAPIKeyEntry = {
        "upstream-api-key": ampUpstreamKeyValue.trim(),
        "api-keys": apiKeysList,
      };
      let newKeys: AmpUpstreamAPIKeyEntry[];
      if (ampUpstreamKeyEditIndex !== null) {
        newKeys = ampUpstreamAPIKeys.map((k, i) =>
          i === ampUpstreamKeyEditIndex ? entry : k
        );
      } else {
        newKeys = [...ampUpstreamAPIKeys, entry];
      }
      await api.ampCode.putAmpUpstreamAPIKeys(newKeys);
      setAmpUpstreamAPIKeys(newKeys);
      toast.success(ampUpstreamKeyEditIndex !== null ? "Upstream API key updated" : "Upstream API key added");
      setAmpUpstreamKeyFormOpen(false);
    } catch (err) {
      toast.error("Failed to save upstream API key", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setAmpUpstreamKeySaving(false);
    }
  };

  const handleDeleteAmpUpstreamKey = async () => {
    if (deleteAmpUpstreamKeyTarget === null) return;
    setDeleteAmpUpstreamKeySaving(true);
    try {
      const key = ampUpstreamAPIKeys[deleteAmpUpstreamKeyTarget]["upstream-api-key"];
      await api.ampCode.deleteAmpUpstreamAPIKeys([key]);
      setAmpUpstreamAPIKeys(ampUpstreamAPIKeys.filter((_, i) => i !== deleteAmpUpstreamKeyTarget));
      toast.success("Upstream API key deleted");
      setDeleteAmpUpstreamKeyTarget(null);
    } catch (err) {
      toast.error("Failed to delete upstream API key", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setDeleteAmpUpstreamKeySaving(false);
    }
  };

  // ─── Initial data fetch ───
  useEffect(() => {
    fetchAPIKeys();
    fetchGeminiKeys();
    fetchClaudeKeys();
    fetchCodexKeys();
    fetchVertexKeys();
    fetchOpenAICompat();
    fetchAmpCode();
  }, [fetchAPIKeys, fetchGeminiKeys, fetchClaudeKeys, fetchCodexKeys, fetchVertexKeys, fetchOpenAICompat, fetchAmpCode]);

  return (
    <div className="flex flex-col gap-6">
      <div className="flex items-center gap-2">
        <Key className="size-5 text-muted-foreground" />
        <h1 className="text-lg font-semibold">API Keys</h1>
      </div>

      <Tabs defaultValue="api-keys" className="w-full">
        <TabsList className="flex flex-wrap gap-1">
          <TabsTrigger value="api-keys" className="gap-1.5">
            <Key className="size-3.5" />
            API Keys
          </TabsTrigger>
          <TabsTrigger value="gemini" className="gap-1.5">
            <Sparkles className="size-3.5" />
            Gemini
          </TabsTrigger>
          <TabsTrigger value="claude" className="gap-1.5">
            <Bot className="size-3.5" />
            Claude
          </TabsTrigger>
          <TabsTrigger value="codex" className="gap-1.5">
            <Code2 className="size-3.5" />
            Codex
          </TabsTrigger>
          <TabsTrigger value="vertex" className="gap-1.5">
            <Cloud className="size-3.5" />
            Vertex
          </TabsTrigger>
          <TabsTrigger value="openai-compat" className="gap-1.5">
            <Layers className="size-3.5" />
            OpenAI Compat
          </TabsTrigger>
          <TabsTrigger value="ampcode" className="gap-1.5">
            <Globe className="size-3.5" />
            AmpCode
          </TabsTrigger>
        </TabsList>

        {/* ─── Tab 1: API Keys ─── */}
        <TabsContent value="api-keys" className="flex flex-col gap-4">
          <div className="flex items-center justify-between">
            <p className="text-sm text-muted-foreground">
              Simple API key strings for authentication
            </p>
            <Button size="sm" onClick={() => setAddKeyOpen(true)}>
              <Plus />
              Add Key
            </Button>
          </div>

          {apiKeysLoading ? (
            <div className="rounded-lg border">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead className="w-12">#</TableHead>
                    <TableHead>Key</TableHead>
                    <TableHead className="w-16" />
                  </TableRow>
                </TableHeader>
                <TableBody>
                  <TableSkeleton cols={3} />
                </TableBody>
              </Table>
            </div>
          ) : apiKeys.length === 0 ? (
            <EmptyState
              icon={<Key className="size-10" />}
              message="No API keys configured. Add a key to get started."
            />
          ) : (
            <div className="rounded-lg border">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead className="w-12">#</TableHead>
                    <TableHead>Key</TableHead>
                    <TableHead className="w-16" />
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {apiKeys.map((key, i) => (
                    <TableRow key={i}>
                      <TableCell className="text-muted-foreground">{i + 1}</TableCell>
                      <TableCell>
                        <MaskedKeyCell value={key} />
                      </TableCell>
                      <TableCell>
                        <Button
                          variant="ghost"
                          size="icon-xs"
                          onClick={() => setDeleteKeyTarget({ index: i, value: key })}
                          aria-label="Delete key"
                        >
                          <Trash2 className="text-destructive" />
                        </Button>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          )}
        </TabsContent>

        {/* ─── Tab 2: Gemini Keys ─── */}
        <TabsContent value="gemini" className="flex flex-col gap-4">
          <div className="flex items-center justify-between">
            <p className="text-sm text-muted-foreground">
              Gemini API key entries with optional prefix and proxy
            </p>
            <Button size="sm" onClick={openGeminiAdd}>
              <Plus />
              Add Key
            </Button>
          </div>

          {geminiLoading ? (
            <div className="rounded-lg border">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>API Key</TableHead>
                    <TableHead>Prefix</TableHead>
                    <TableHead>Base URL</TableHead>
                    <TableHead>Proxy URL</TableHead>
                    <TableHead className="w-20" />
                  </TableRow>
                </TableHeader>
                <TableBody>
                  <TableSkeleton cols={5} />
                </TableBody>
              </Table>
            </div>
          ) : geminiKeys.length === 0 ? (
            <EmptyState
              icon={<Sparkles className="size-10" />}
              message="No Gemini keys configured. Add a key to get started."
            />
          ) : (
            <div className="rounded-lg border">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>API Key</TableHead>
                    <TableHead>Prefix</TableHead>
                    <TableHead>Base URL</TableHead>
                    <TableHead>Proxy URL</TableHead>
                    <TableHead className="w-20" />
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {geminiKeys.map((key, i) => (
                    <TableRow key={i}>
                      <TableCell>
                        <MaskedKeyCell value={key["api-key"]} />
                      </TableCell>
                      <TableCell>
                        {key.prefix ? (
                          <Badge variant="outline">{key.prefix}</Badge>
                        ) : (
                          <span className="text-muted-foreground">—</span>
                        )}
                      </TableCell>
                      <TableCell className="max-w-[200px] truncate text-muted-foreground">
                        {key["base-url"] || "—"}
                      </TableCell>
                      <TableCell className="max-w-[200px] truncate text-muted-foreground">
                        {key["proxy-url"] || "—"}
                      </TableCell>
                      <TableCell>
                        <div className="flex items-center gap-1">
                          <Button
                            variant="ghost"
                            size="icon-xs"
                            onClick={() => openGeminiEdit(i, key)}
                            aria-label="Edit key"
                          >
                            <Pencil />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon-xs"
                            onClick={() => setDeleteGeminiTarget({ index: i, key })}
                            aria-label="Delete key"
                          >
                            <Trash2 className="text-destructive" />
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          )}
        </TabsContent>

        {/* ─── Tab 3: Claude Keys ─── */}
        <TabsContent value="claude" className="flex flex-col gap-4">
          <div className="flex items-center justify-between">
            <p className="text-sm text-muted-foreground">
              Claude API key entries with model aliases
            </p>
            <Button size="sm" onClick={openClaudeAdd}>
              <Plus />
              Add Key
            </Button>
          </div>

          {claudeLoading ? (
            <div className="rounded-lg border">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>API Key</TableHead>
                    <TableHead>Prefix</TableHead>
                    <TableHead>Base URL</TableHead>
                    <TableHead>Models</TableHead>
                    <TableHead className="w-20" />
                  </TableRow>
                </TableHeader>
                <TableBody>
                  <TableSkeleton cols={5} />
                </TableBody>
              </Table>
            </div>
          ) : claudeKeys.length === 0 ? (
            <EmptyState
              icon={<Bot className="size-10" />}
              message="No Claude keys configured. Add a key to get started."
            />
          ) : (
            <div className="rounded-lg border">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>API Key</TableHead>
                    <TableHead>Prefix</TableHead>
                    <TableHead>Base URL</TableHead>
                    <TableHead>Models</TableHead>
                    <TableHead className="w-20" />
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {claudeKeys.map((key, i) => (
                    <TableRow key={i}>
                      <TableCell>
                        <MaskedKeyCell value={key["api-key"]} />
                      </TableCell>
                      <TableCell>
                        {key.prefix ? (
                          <Badge variant="outline">{key.prefix}</Badge>
                        ) : (
                          <span className="text-muted-foreground">—</span>
                        )}
                      </TableCell>
                      <TableCell className="max-w-[200px] truncate text-muted-foreground">
                        {key["base-url"] || "—"}
                      </TableCell>
                      <TableCell>
                        {key.models && key.models.length > 0 ? (
                          <Badge variant="secondary">{key.models.length}</Badge>
                        ) : (
                          <span className="text-muted-foreground">0</span>
                        )}
                      </TableCell>
                      <TableCell>
                        <div className="flex items-center gap-1">
                          <Button
                            variant="ghost"
                            size="icon-xs"
                            onClick={() => openClaudeEdit(i, key)}
                            aria-label="Edit key"
                          >
                            <Pencil />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon-xs"
                            onClick={() => setDeleteClaudeTarget({ index: i, key })}
                            aria-label="Delete key"
                          >
                            <Trash2 className="text-destructive" />
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          )}
        </TabsContent>

        {/* ─── Tab 4: Codex Keys ─── */}
        <TabsContent value="codex" className="flex flex-col gap-4">
          <div className="flex items-center justify-between">
            <p className="text-sm text-muted-foreground">
              Codex API key entries with WebSocket support
            </p>
            <Button size="sm" onClick={openCodexAdd}>
              <Plus />
              Add Key
            </Button>
          </div>

          {codexLoading ? (
            <div className="rounded-lg border">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>API Key</TableHead>
                    <TableHead>Prefix</TableHead>
                    <TableHead>Base URL</TableHead>
                    <TableHead>Models</TableHead>
                    <TableHead>WebSocket</TableHead>
                    <TableHead className="w-20" />
                  </TableRow>
                </TableHeader>
                <TableBody>
                  <TableSkeleton cols={6} />
                </TableBody>
              </Table>
            </div>
          ) : codexKeys.length === 0 ? (
            <EmptyState
              icon={<Code2 className="size-10" />}
              message="No Codex keys configured. Add a key to get started."
            />
          ) : (
            <div className="rounded-lg border">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>API Key</TableHead>
                    <TableHead>Prefix</TableHead>
                    <TableHead>Base URL</TableHead>
                    <TableHead>Models</TableHead>
                    <TableHead>WebSocket</TableHead>
                    <TableHead className="w-20" />
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {codexKeys.map((key, i) => (
                    <TableRow key={i}>
                      <TableCell>
                        <MaskedKeyCell value={key["api-key"]} />
                      </TableCell>
                      <TableCell>
                        {key.prefix ? (
                          <Badge variant="outline">{key.prefix}</Badge>
                        ) : (
                          <span className="text-muted-foreground">—</span>
                        )}
                      </TableCell>
                      <TableCell className="max-w-[200px] truncate text-muted-foreground">
                        {key["base-url"] || "—"}
                      </TableCell>
                      <TableCell>
                        {key.models && key.models.length > 0 ? (
                          <Badge variant="secondary">{key.models.length}</Badge>
                        ) : (
                          <span className="text-muted-foreground">0</span>
                        )}
                      </TableCell>
                      <TableCell>
                        {key.websockets ? (
                          <Badge className="bg-emerald-500/10 text-emerald-600 dark:text-emerald-400">
                            On
                          </Badge>
                        ) : (
                          <span className="text-muted-foreground">Off</span>
                        )}
                      </TableCell>
                      <TableCell>
                        <div className="flex items-center gap-1">
                          <Button
                            variant="ghost"
                            size="icon-xs"
                            onClick={() => openCodexEdit(i, key)}
                            aria-label="Edit key"
                          >
                            <Pencil />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon-xs"
                            onClick={() => setDeleteCodexTarget({ index: i, key })}
                            aria-label="Delete key"
                          >
                            <Trash2 className="text-destructive" />
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          )}
        </TabsContent>

        {/* ─── Tab 5: Vertex Keys ─── */}
        <TabsContent value="vertex" className="flex flex-col gap-4">
          <div className="flex items-center justify-between">
            <p className="text-sm text-muted-foreground">
              Vertex AI API key entries with service account import
            </p>
            <div className="flex items-center gap-2">
              <Button variant="outline" size="sm" onClick={() => setVertexImportOpen(true)}>
                <Upload />
                Import Credential
              </Button>
              <Button size="sm" onClick={openVertexAdd}>
                <Plus />
                Add Key
              </Button>
            </div>
          </div>

          {vertexLoading ? (
            <div className="rounded-lg border">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>API Key</TableHead>
                    <TableHead>Prefix</TableHead>
                    <TableHead>Base URL</TableHead>
                    <TableHead>Models</TableHead>
                    <TableHead className="w-20" />
                  </TableRow>
                </TableHeader>
                <TableBody>
                  <TableSkeleton cols={5} />
                </TableBody>
              </Table>
            </div>
          ) : vertexKeys.length === 0 ? (
            <EmptyState
              icon={<Cloud className="size-10" />}
              message="No Vertex keys configured. Add a key or import credentials to get started."
            />
          ) : (
            <div className="rounded-lg border">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>API Key</TableHead>
                    <TableHead>Prefix</TableHead>
                    <TableHead>Base URL</TableHead>
                    <TableHead>Models</TableHead>
                    <TableHead className="w-20" />
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {vertexKeys.map((key, i) => (
                    <TableRow key={i}>
                      <TableCell>
                        <MaskedKeyCell value={key["api-key"]} />
                      </TableCell>
                      <TableCell>
                        {key.prefix ? (
                          <Badge variant="outline">{key.prefix}</Badge>
                        ) : (
                          <span className="text-muted-foreground">—</span>
                        )}
                      </TableCell>
                      <TableCell className="max-w-[200px] truncate text-muted-foreground">
                        {key["base-url"] || "—"}
                      </TableCell>
                      <TableCell>
                        {key.models && key.models.length > 0 ? (
                          <Badge variant="secondary">{key.models.length}</Badge>
                        ) : (
                          <span className="text-muted-foreground">0</span>
                        )}
                      </TableCell>
                      <TableCell>
                        <div className="flex items-center gap-1">
                          <Button
                            variant="ghost"
                            size="icon-xs"
                            onClick={() => openVertexEdit(i, key)}
                            aria-label="Edit key"
                          >
                            <Pencil />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon-xs"
                            onClick={() => setDeleteVertexTarget({ index: i, key })}
                            aria-label="Delete key"
                          >
                            <Trash2 className="text-destructive" />
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          )}
        </TabsContent>

        {/* ─── Tab 6: OpenAI Compatibility ─── */}
        <TabsContent value="openai-compat" className="flex flex-col gap-4">
          <div className="flex items-center justify-between">
            <p className="text-sm text-muted-foreground">
              OpenAI-compatible provider entries
            </p>
            <Button size="sm" onClick={openOACAdd}>
              <Plus />
              Add Entry
            </Button>
          </div>

          {oacLoading ? (
            <div className="rounded-lg border">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Name</TableHead>
                    <TableHead>Prefix</TableHead>
                    <TableHead>Base URL</TableHead>
                    <TableHead>API Keys</TableHead>
                    <TableHead className="w-20" />
                  </TableRow>
                </TableHeader>
                <TableBody>
                  <TableSkeleton cols={5} />
                </TableBody>
              </Table>
            </div>
          ) : openAICompat.length === 0 ? (
            <EmptyState
              icon={<Layers className="size-10" />}
              message="No OpenAI compatibility entries. Add an entry to get started."
            />
          ) : (
            <div className="rounded-lg border">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Name</TableHead>
                    <TableHead>Prefix</TableHead>
                    <TableHead>Base URL</TableHead>
                    <TableHead>API Keys</TableHead>
                    <TableHead className="w-20" />
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {openAICompat.map((entry, i) => (
                    <TableRow key={i}>
                      <TableCell className="font-medium">{entry.name}</TableCell>
                      <TableCell>
                        {entry.prefix ? (
                          <Badge variant="outline">{entry.prefix}</Badge>
                        ) : (
                          <span className="text-muted-foreground">—</span>
                        )}
                      </TableCell>
                      <TableCell className="max-w-[200px] truncate text-muted-foreground">
                        {entry["base-url"]}
                      </TableCell>
                      <TableCell>
                        {entry["api-key-entries"] && entry["api-key-entries"].length > 0 ? (
                          <Badge variant="secondary">{entry["api-key-entries"].length}</Badge>
                        ) : (
                          <span className="text-muted-foreground">0</span>
                        )}
                      </TableCell>
                      <TableCell>
                        <div className="flex items-center gap-1">
                          <Button
                            variant="ghost"
                            size="icon-xs"
                            onClick={() => openOACEdit(i, entry)}
                            aria-label="Edit entry"
                          >
                            <Pencil />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon-xs"
                            onClick={() => setDeleteOACTarget({ index: i, entry })}
                            aria-label="Delete entry"
                          >
                            <Trash2 className="text-destructive" />
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          )}
        </TabsContent>

        {/* ─── Tab 7: AmpCode ─── */}
        <TabsContent value="ampcode" className="flex flex-col gap-4">
          {ampLoading ? (
            <div className="flex flex-col gap-4">
              {Array.from({ length: 4 }).map((_, i) => (
                <Card key={i}>
                  <CardHeader>
                    <Skeleton className="h-5 w-32" />
                    <Skeleton className="h-4 w-48" />
                  </CardHeader>
                  <CardContent>
                    <Skeleton className="h-9 w-full" />
                  </CardContent>
                </Card>
              ))}
            </div>
          ) : (
            <>
              <Card>
                <CardHeader>
                  <CardTitle className="flex items-center gap-2">
                    <Globe className="size-4" />
                    Upstream URL
                  </CardTitle>
                  <CardDescription>
                    The upstream proxy URL for AmpCode requests
                  </CardDescription>
                </CardHeader>
                <CardContent>
                  <div className="flex items-center gap-2">
                    <Input
                      value={ampUpstreamURLEdit}
                      onChange={(e) => setAmpUpstreamURLEdit(e.target.value)}
                      placeholder="https://upstream.example.com"
                      disabled={ampSavingURL}
                    />
                    <Button
                      size="sm"
                      onClick={handleSaveAmpURL}
                      disabled={ampSavingURL}
                    >
                      <Save />
                      Save
                    </Button>
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={handleClearAmpURL}
                      disabled={ampSavingURL || !ampUpstreamURL}
                    >
                      <Eraser />
                      Clear
                    </Button>
                  </div>
                </CardContent>
              </Card>

              <Card>
                <CardHeader>
                  <CardTitle className="flex items-center gap-2">
                    <Key className="size-4" />
                    Upstream API Key
                  </CardTitle>
                  <CardDescription>
                    The API key for upstream AmpCode authentication
                  </CardDescription>
                </CardHeader>
                <CardContent>
                  <div className="flex items-center gap-2">
                    <Input
                      type="password"
                      value={ampUpstreamAPIKeyEdit}
                      onChange={(e) => setAmpUpstreamAPIKeyEdit(e.target.value)}
                      placeholder="Enter upstream API key"
                      disabled={ampSavingAPIKey}
                    />
                    <Button
                      size="sm"
                      onClick={handleSaveAmpAPIKey}
                      disabled={ampSavingAPIKey}
                    >
                      <Save />
                      Save
                    </Button>
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={handleClearAmpAPIKey}
                      disabled={ampSavingAPIKey || !ampUpstreamAPIKey}
                    >
                      <Eraser />
                      Clear
                    </Button>
                  </div>
                </CardContent>
              </Card>

              <Card>
                <CardHeader>
                  <CardTitle>Restrict Management to Localhost</CardTitle>
                  <CardDescription>
                    Only allow management API access from localhost
                  </CardDescription>
                </CardHeader>
                <CardContent>
                  <div className="flex items-center gap-2">
                    <Switch
                      size="sm"
                      checked={ampRestrictLocalhost}
                      onCheckedChange={handleAmpRestrictToggle}
                      disabled={ampSavingSwitch}
                    />
                    <span className="text-sm text-muted-foreground">
                      {ampRestrictLocalhost ? "Enabled" : "Disabled"}
                    </span>
                  </div>
                </CardContent>
              </Card>

              <Card>
                <CardHeader>
                  <CardTitle>Force Model Mappings</CardTitle>
                  <CardDescription>
                    Force all model requests through the defined mappings
                  </CardDescription>
                </CardHeader>
                <CardContent>
                  <div className="flex items-center gap-2">
                    <Switch
                      size="sm"
                      checked={ampForceMappings}
                      onCheckedChange={handleAmpForceMappingsToggle}
                      disabled={ampSavingSwitch}
                    />
                    <span className="text-sm text-muted-foreground">
                      {ampForceMappings ? "Enabled" : "Disabled"}
                    </span>
                  </div>
                </CardContent>
              </Card>

              <Card>
                <CardHeader>
                  <div className="flex items-center justify-between">
                    <div>
                      <CardTitle className="flex items-center gap-2">
                        <ArrowRight className="size-4" />
                        Model Mappings
                      </CardTitle>
                      <CardDescription>
                        Map model names from source to target
                      </CardDescription>
                    </div>
                    <Button size="sm" onClick={openAmpMappingAdd}>
                      <Plus />
                      Add Mapping
                    </Button>
                  </div>
                </CardHeader>
                <CardContent>
                  {ampModelMappings.length === 0 ? (
                    <p className="text-sm text-muted-foreground text-center py-4">
                      No model mappings configured
                    </p>
                  ) : (
                    <div className="rounded-lg border">
                      <Table>
                        <TableHeader>
                          <TableRow>
                            <TableHead>From</TableHead>
                            <TableHead>To</TableHead>
                            <TableHead className="w-20" />
                          </TableRow>
                        </TableHeader>
                        <TableBody>
                          {ampModelMappings.map((m, i) => (
                            <TableRow key={i}>
                              <TableCell className="font-mono text-sm">{m.from}</TableCell>
                              <TableCell className="font-mono text-sm">{m.to}</TableCell>
                              <TableCell>
                                <div className="flex items-center gap-1">
                                  <Button
                                    variant="ghost"
                                    size="icon-xs"
                                    onClick={() => openAmpMappingEdit(i)}
                                    aria-label="Edit mapping"
                                  >
                                    <Pencil />
                                  </Button>
                                  <Button
                                    variant="ghost"
                                    size="icon-xs"
                                    onClick={() => setDeleteAmpMappingTarget(i)}
                                    aria-label="Delete mapping"
                                  >
                                    <Trash2 className="text-destructive" />
                                  </Button>
                                </div>
                              </TableCell>
                            </TableRow>
                          ))}
                        </TableBody>
                      </Table>
                    </div>
                  )}
                </CardContent>
              </Card>

              <Card>
                <CardHeader>
                  <div className="flex items-center justify-between">
                    <div>
                      <CardTitle className="flex items-center gap-2">
                        <Key className="size-4" />
                        Upstream API Keys
                      </CardTitle>
                      <CardDescription>
                        Upstream API key entries with associated local API keys
                      </CardDescription>
                    </div>
                    <Button size="sm" onClick={openAmpUpstreamKeyAdd}>
                      <Plus />
                      Add Key
                    </Button>
                  </div>
                </CardHeader>
                <CardContent>
                  {ampUpstreamAPIKeys.length === 0 ? (
                    <p className="text-sm text-muted-foreground text-center py-4">
                      No upstream API keys configured
                    </p>
                  ) : (
                    <div className="rounded-lg border">
                      <Table>
                        <TableHeader>
                          <TableRow>
                            <TableHead>Upstream Key</TableHead>
                            <TableHead>API Keys</TableHead>
                            <TableHead className="w-20" />
                          </TableRow>
                        </TableHeader>
                        <TableBody>
                          {ampUpstreamAPIKeys.map((entry, i) => (
                            <TableRow key={i}>
                              <TableCell>
                                <MaskedValue value={entry["upstream-api-key"]} />
                              </TableCell>
                              <TableCell>
                                <div className="flex flex-wrap gap-1">
                                  {entry["api-keys"].length > 0 ? (
                                    entry["api-keys"].map((k, j) => (
                                      <Badge key={j} variant="secondary" className="text-xs">
                                        {maskKey(k)}
                                      </Badge>
                                    ))
                                  ) : (
                                    <span className="text-muted-foreground">—</span>
                                  )}
                                </div>
                              </TableCell>
                              <TableCell>
                                <div className="flex items-center gap-1">
                                  <Button
                                    variant="ghost"
                                    size="icon-xs"
                                    onClick={() => openAmpUpstreamKeyEdit(i)}
                                    aria-label="Edit key"
                                  >
                                    <Pencil />
                                  </Button>
                                  <Button
                                    variant="ghost"
                                    size="icon-xs"
                                    onClick={() => setDeleteAmpUpstreamKeyTarget(i)}
                                    aria-label="Delete key"
                                  >
                                    <Trash2 className="text-destructive" />
                                  </Button>
                                </div>
                              </TableCell>
                            </TableRow>
                          ))}
                        </TableBody>
                      </Table>
                    </div>
                  )}
                </CardContent>
              </Card>
            </>
          )}
        </TabsContent>
      </Tabs>

      {/* ─── Dialogs ─── */}

      {/* Add API Key Dialog */}
      <Dialog open={addKeyOpen} onOpenChange={setAddKeyOpen}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>Add API Key</DialogTitle>
            <DialogDescription>
              Enter a new API key to add to the configuration.
            </DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-4">
            <div className="flex flex-col gap-2">
              <Label htmlFor="new-api-key">API Key</Label>
              <Input
                id="new-api-key"
                value={newKeyValue}
                onChange={(e) => setNewKeyValue(e.target.value)}
                placeholder="Enter API key"
              />
            </div>
          </div>
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => setAddKeyOpen(false)}
              disabled={addKeySaving}
            >
              Cancel
            </Button>
            <Button onClick={handleAddKey} disabled={addKeySaving || !newKeyValue.trim()}>
              {addKeySaving ? "Adding..." : "Add Key"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete API Key Confirmation */}
      <AlertDialog
        open={deleteKeyTarget !== null}
        onOpenChange={(open) => {
          if (!open) setDeleteKeyTarget(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete API Key</AlertDialogTitle>
            <AlertDialogDescription>
              Are you sure you want to delete this API key? This action cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleteKeySaving}>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={handleDeleteKey}
              disabled={deleteKeySaving}
              className={DELETE_ACTION_CLASS}
            >
              {deleteKeySaving ? "Deleting..." : "Delete"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* Gemini Key Form Dialog */}
      <Dialog open={geminiFormOpen} onOpenChange={setGeminiFormOpen}>
        <DialogContent className="sm:max-w-lg max-h-[85vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>
              {geminiEditIndex !== null ? "Edit Gemini Key" : "Add Gemini Key"}
            </DialogTitle>
            <DialogDescription>
              {geminiEditIndex !== null
                ? "Update the Gemini API key configuration."
                : "Add a new Gemini API key entry."}
            </DialogDescription>
          </DialogHeader>
          <ProviderKeyForm form={geminiForm} setForm={setGeminiForm} />
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => setGeminiFormOpen(false)}
              disabled={geminiSaving}
            >
              Cancel
            </Button>
            <Button onClick={handleGeminiSave} disabled={geminiSaving}>
              {geminiSaving ? "Saving..." : "Save"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete Gemini Key Confirmation */}
      <AlertDialog
        open={deleteGeminiTarget !== null}
        onOpenChange={(open) => {
          if (!open) setDeleteGeminiTarget(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete Gemini Key</AlertDialogTitle>
            <AlertDialogDescription>
              Are you sure you want to delete this Gemini key? This action cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleteGeminiSaving}>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={handleDeleteGemini}
              disabled={deleteGeminiSaving}
              className={DELETE_ACTION_CLASS}
            >
              {deleteGeminiSaving ? "Deleting..." : "Delete"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* Claude Key Form Dialog */}
      <Dialog open={claudeFormOpen} onOpenChange={setClaudeFormOpen}>
        <DialogContent className="sm:max-w-lg max-h-[85vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>
              {claudeEditIndex !== null ? "Edit Claude Key" : "Add Claude Key"}
            </DialogTitle>
            <DialogDescription>
              {claudeEditIndex !== null
                ? "Update the Claude API key configuration."
                : "Add a new Claude API key entry."}
            </DialogDescription>
          </DialogHeader>
          <ProviderKeyForm form={claudeForm} setForm={setClaudeForm} />
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => setClaudeFormOpen(false)}
              disabled={claudeSaving}
            >
              Cancel
            </Button>
            <Button onClick={handleClaudeSave} disabled={claudeSaving}>
              {claudeSaving ? "Saving..." : "Save"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete Claude Key Confirmation */}
      <AlertDialog
        open={deleteClaudeTarget !== null}
        onOpenChange={(open) => {
          if (!open) setDeleteClaudeTarget(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete Claude Key</AlertDialogTitle>
            <AlertDialogDescription>
              Are you sure you want to delete this Claude key? This action cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleteClaudeSaving}>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={handleDeleteClaude}
              disabled={deleteClaudeSaving}
              className={DELETE_ACTION_CLASS}
            >
              {deleteClaudeSaving ? "Deleting..." : "Delete"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* Codex Key Form Dialog */}
      <Dialog open={codexFormOpen} onOpenChange={setCodexFormOpen}>
        <DialogContent className="sm:max-w-lg max-h-[85vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>
              {codexEditIndex !== null ? "Edit Codex Key" : "Add Codex Key"}
            </DialogTitle>
            <DialogDescription>
              {codexEditIndex !== null
                ? "Update the Codex API key configuration."
                : "Add a new Codex API key entry."}
            </DialogDescription>
          </DialogHeader>
          <ProviderKeyForm form={codexForm} setForm={setCodexForm} showWebsockets />
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => setCodexFormOpen(false)}
              disabled={codexSaving}
            >
              Cancel
            </Button>
            <Button onClick={handleCodexSave} disabled={codexSaving}>
              {codexSaving ? "Saving..." : "Save"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete Codex Key Confirmation */}
      <AlertDialog
        open={deleteCodexTarget !== null}
        onOpenChange={(open) => {
          if (!open) setDeleteCodexTarget(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete Codex Key</AlertDialogTitle>
            <AlertDialogDescription>
              Are you sure you want to delete this Codex key? This action cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleteCodexSaving}>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={handleDeleteCodex}
              disabled={deleteCodexSaving}
              className={DELETE_ACTION_CLASS}
            >
              {deleteCodexSaving ? "Deleting..." : "Delete"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* Vertex Key Form Dialog */}
      <Dialog open={vertexFormOpen} onOpenChange={setVertexFormOpen}>
        <DialogContent className="sm:max-w-lg max-h-[85vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>
              {vertexEditIndex !== null ? "Edit Vertex Key" : "Add Vertex Key"}
            </DialogTitle>
            <DialogDescription>
              {vertexEditIndex !== null
                ? "Update the Vertex API key configuration."
                : "Add a new Vertex API key entry."}
            </DialogDescription>
          </DialogHeader>
          <ProviderKeyForm form={vertexForm} setForm={setVertexForm} />
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => setVertexFormOpen(false)}
              disabled={vertexSaving}
            >
              Cancel
            </Button>
            <Button onClick={handleVertexSave} disabled={vertexSaving}>
              {vertexSaving ? "Saving..." : "Save"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete Vertex Key Confirmation */}
      <AlertDialog
        open={deleteVertexTarget !== null}
        onOpenChange={(open) => {
          if (!open) setDeleteVertexTarget(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete Vertex Key</AlertDialogTitle>
            <AlertDialogDescription>
              Are you sure you want to delete this Vertex key? This action cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleteVertexSaving}>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={handleDeleteVertex}
              disabled={deleteVertexSaving}
              className={DELETE_ACTION_CLASS}
            >
              {deleteVertexSaving ? "Deleting..." : "Delete"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* Vertex Import Dialog */}
      <Dialog open={vertexImportOpen} onOpenChange={setVertexImportOpen}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>Import Vertex Credentials</DialogTitle>
            <DialogDescription>
              Upload a Google Cloud service account JSON file to import Vertex AI credentials.
            </DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-4">
            <Input
              ref={vertexFileRef}
              type="file"
              accept=".json"
              disabled={vertexImporting}
            />
          </div>
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => setVertexImportOpen(false)}
              disabled={vertexImporting}
            >
              Cancel
            </Button>
            <Button onClick={handleVertexImport} disabled={vertexImporting}>
              {vertexImporting ? "Importing..." : "Import"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* OpenAI Compat Form Dialog */}
      <Dialog open={oacFormOpen} onOpenChange={setOACFormOpen}>
        <DialogContent className="sm:max-w-lg max-h-[85vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>
              {oacEditIndex !== null ? "Edit OpenAI Compatibility Entry" : "Add OpenAI Compatibility Entry"}
            </DialogTitle>
            <DialogDescription>
              {oacEditIndex !== null
                ? "Update the OpenAI compatibility entry configuration."
                : "Add a new OpenAI-compatible provider entry."}
            </DialogDescription>
          </DialogHeader>
          <OpenAICompatForm form={oacForm} setForm={setOACForm} />
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => setOACFormOpen(false)}
              disabled={oacSaving}
            >
              Cancel
            </Button>
            <Button onClick={handleOACSave} disabled={oacSaving}>
              {oacSaving ? "Saving..." : "Save"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete OpenAI Compat Confirmation */}
      <AlertDialog
        open={deleteOACTarget !== null}
        onOpenChange={(open) => {
          if (!open) setDeleteOACTarget(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete OpenAI Compatibility Entry</AlertDialogTitle>
            <AlertDialogDescription>
              Are you sure you want to delete{" "}
              <span className="font-medium text-foreground">
                {deleteOACTarget?.entry.name}
              </span>
              ? This action cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleteOACSaving}>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={handleDeleteOAC}
              disabled={deleteOACSaving}
              className={DELETE_ACTION_CLASS}
            >
              {deleteOACSaving ? "Deleting..." : "Delete"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* AmpCode Model Mapping Form Dialog */}
      <Dialog open={ampMappingFormOpen} onOpenChange={setAmpMappingFormOpen}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>
              {ampMappingEditIndex !== null ? "Edit Model Mapping" : "Add Model Mapping"}
            </DialogTitle>
            <DialogDescription>
              Map a source model name to a target model name.
            </DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-4">
            <div className="flex flex-col gap-2">
              <Label htmlFor="amp-mapping-from">From</Label>
              <Input
                id="amp-mapping-from"
                value={ampMappingFrom}
                onChange={(e) => setAmpMappingFrom(e.target.value)}
                placeholder="Source model name"
              />
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="amp-mapping-to">To</Label>
              <Input
                id="amp-mapping-to"
                value={ampMappingTo}
                onChange={(e) => setAmpMappingTo(e.target.value)}
                placeholder="Target model name"
              />
            </div>
          </div>
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => setAmpMappingFormOpen(false)}
              disabled={ampMappingSaving}
            >
              Cancel
            </Button>
            <Button onClick={handleAmpMappingSave} disabled={ampMappingSaving}>
              {ampMappingSaving ? "Saving..." : "Save"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete AmpCode Model Mapping Confirmation */}
      <AlertDialog
        open={deleteAmpMappingTarget !== null}
        onOpenChange={(open) => {
          if (!open) setDeleteAmpMappingTarget(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete Model Mapping</AlertDialogTitle>
            <AlertDialogDescription>
              Are you sure you want to delete the mapping from{" "}
              <span className="font-medium text-foreground">
                {deleteAmpMappingTarget !== null ? ampModelMappings[deleteAmpMappingTarget]?.from : ""}
              </span>{" "}
              to{" "}
              <span className="font-medium text-foreground">
                {deleteAmpMappingTarget !== null ? ampModelMappings[deleteAmpMappingTarget]?.to : ""}
              </span>
              ? This action cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleteAmpMappingSaving}>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={handleDeleteAmpMapping}
              disabled={deleteAmpMappingSaving}
              className={DELETE_ACTION_CLASS}
            >
              {deleteAmpMappingSaving ? "Deleting..." : "Delete"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* AmpCode Upstream API Key Form Dialog */}
      <Dialog open={ampUpstreamKeyFormOpen} onOpenChange={setAmpUpstreamKeyFormOpen}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>
              {ampUpstreamKeyEditIndex !== null ? "Edit Upstream API Key" : "Add Upstream API Key"}
            </DialogTitle>
            <DialogDescription>
              Configure an upstream API key with associated local API keys.
            </DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-4">
            <div className="flex flex-col gap-2">
              <Label htmlFor="amp-upstream-key">Upstream API Key</Label>
              <Input
                id="amp-upstream-key"
                value={ampUpstreamKeyValue}
                onChange={(e) => setAmpUpstreamKeyValue(e.target.value)}
                placeholder="Enter upstream API key"
              />
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="amp-upstream-api-keys">API Keys</Label>
              <Input
                id="amp-upstream-api-keys"
                value={ampUpstreamKeyApiKeys}
                onChange={(e) => setAmpUpstreamKeyApiKeys(e.target.value)}
                placeholder="key1, key2, key3 (comma-separated)"
              />
            </div>
          </div>
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => setAmpUpstreamKeyFormOpen(false)}
              disabled={ampUpstreamKeySaving}
            >
              Cancel
            </Button>
            <Button onClick={handleAmpUpstreamKeySave} disabled={ampUpstreamKeySaving}>
              {ampUpstreamKeySaving ? "Saving..." : "Save"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete AmpCode Upstream API Key Confirmation */}
      <AlertDialog
        open={deleteAmpUpstreamKeyTarget !== null}
        onOpenChange={(open) => {
          if (!open) setDeleteAmpUpstreamKeyTarget(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete Upstream API Key</AlertDialogTitle>
            <AlertDialogDescription>
              Are you sure you want to delete this upstream API key? This action cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleteAmpUpstreamKeySaving}>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={handleDeleteAmpUpstreamKey}
              disabled={deleteAmpUpstreamKeySaving}
              className={DELETE_ACTION_CLASS}
            >
              {deleteAmpUpstreamKeySaving ? "Deleting..." : "Delete"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

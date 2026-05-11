"use client";

import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import { toast } from "sonner";

import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Switch } from "@/components/ui/switch";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { Skeleton } from "@/components/ui/skeleton";
import { Spinner } from "@/components/ui/spinner";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

import {
  Bug,
  FileText,
  Globe,
  LogOut,
  BarChart3,
  Shield,
  Hash,
  Route,
  Save,
  Trash2,
  Settings,
  FolderOpen,
} from "lucide-react";

function BooleanSetting({
  label,
  icon: Icon,
  getter,
  setter,
}: {
  label: string;
  icon: React.ComponentType<{ className?: string }>;
  getter: () => Promise<boolean>;
  setter: (value: boolean) => Promise<unknown>;
}) {
  const [value, setValue] = useState<boolean | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const [isSaving, setIsSaving] = useState(false);

  useEffect(() => {
    getter()
      .then((data) => setValue(data))
      .catch((err) => {
        toast.error(`Failed to load ${label}`, {
          description: err instanceof Error ? err.message : "Unknown error",
        });
      })
      .finally(() => setIsLoading(false));
  }, [getter, label]);

  const handleToggle = useCallback(
    async (checked: boolean) => {
      setIsSaving(true);
      try {
        await setter(checked);
        setValue(checked);
        toast.success(`${label} ${checked ? "enabled" : "disabled"}`);
      } catch (err) {
        toast.error(`Failed to update ${label}`, {
          description: err instanceof Error ? err.message : "Unknown error",
        });
      } finally {
        setIsSaving(false);
      }
    },
    [setter, label]
  );

  if (isLoading) {
    return (
      <div className="flex items-center justify-between py-2">
        <div className="flex items-center gap-2">
          <Icon className="size-4 text-muted-foreground" />
          <span className="text-sm">{label}</span>
        </div>
        <Skeleton className="size-8 rounded-full" />
      </div>
    );
  }

  return (
    <div className="flex items-center justify-between py-2">
      <div className="flex items-center gap-2">
        <Icon className="size-4 text-muted-foreground" />
        <span className="text-sm">{label}</span>
      </div>
      <div className="flex items-center gap-2">
        {isSaving && <Spinner className="size-3" />}
        <Switch
          checked={value ?? false}
          onCheckedChange={handleToggle}
          disabled={isSaving}
        />
      </div>
    </div>
  );
}

function NumberSetting({
  label,
  icon: Icon,
  getter,
  setter,
  placeholder,
}: {
  label: string;
  icon: React.ComponentType<{ className?: string }>;
  getter: () => Promise<number>;
  setter: (value: number) => Promise<unknown>;
  placeholder?: string;
}) {
  const [currentValue, setCurrentValue] = useState<number | null>(null);
  const [inputValue, setInputValue] = useState("");
  const [isLoading, setIsLoading] = useState(true);
  const [isSaving, setIsSaving] = useState(false);

  useEffect(() => {
    getter()
      .then((data) => {
        setCurrentValue(data);
        setInputValue(String(data));
      })
      .catch((err) => {
        toast.error(`Failed to load ${label}`, {
          description: err instanceof Error ? err.message : "Unknown error",
        });
      })
      .finally(() => setIsLoading(false));
  }, [getter, label]);

  const handleSave = useCallback(async () => {
    const num = parseInt(inputValue, 10);
    if (isNaN(num)) {
      toast.error(`Invalid number for ${label}`);
      return;
    }
    setIsSaving(true);
    try {
      await setter(num);
      setCurrentValue(num);
      toast.success(`${label} updated`);
    } catch (err) {
      toast.error(`Failed to update ${label}`, {
        description: err instanceof Error ? err.message : "Unknown error",
      });
    } finally {
      setIsSaving(false);
    }
  }, [setter, label, inputValue]);

  if (isLoading) {
    return (
      <div className="flex items-center justify-between gap-4 py-2">
        <div className="flex items-center gap-2">
          <Icon className="size-4 text-muted-foreground" />
          <span className="text-sm">{label}</span>
        </div>
        <Skeleton className="h-8 w-32" />
      </div>
    );
  }

  return (
    <div className="flex items-center justify-between gap-4 py-2">
      <div className="flex items-center gap-2">
        <Icon className="size-4 text-muted-foreground" />
        <span className="text-sm">{label}</span>
      </div>
      <div className="flex items-center gap-2">
        <Input
          type="number"
          value={inputValue}
          onChange={(e) => setInputValue(e.target.value)}
          placeholder={placeholder}
          className="w-32"
          onKeyDown={(e) => {
            if (e.key === "Enter") handleSave();
          }}
        />
        <Button
          variant="outline"
          size="sm"
          onClick={handleSave}
          disabled={isSaving || inputValue === String(currentValue)}
        >
          {isSaving ? <Spinner className="size-3" /> : <Save className="size-3" />}
          Save
        </Button>
      </div>
    </div>
  );
}

function ProxyURLSetting() {
  const [currentValue, setCurrentValue] = useState<string | null>(null);
  const [inputValue, setInputValue] = useState("");
  const [isLoading, setIsLoading] = useState(true);
  const [isSaving, setIsSaving] = useState(false);
  const [isClearing, setIsClearing] = useState(false);

  useEffect(() => {
    api.string
      .getProxyURL()
      .then((data) => {
        setCurrentValue(data);
        setInputValue(data);
      })
      .catch((err) => {
        toast.error("Failed to load Proxy URL", {
          description: err instanceof Error ? err.message : "Unknown error",
        });
      })
      .finally(() => setIsLoading(false));
  }, []);

  const handleSave = useCallback(async () => {
    setIsSaving(true);
    try {
      await api.string.putProxyURL(inputValue);
      setCurrentValue(inputValue);
      toast.success("Proxy URL updated");
    } catch (err) {
      toast.error("Failed to update Proxy URL", {
        description: err instanceof Error ? err.message : "Unknown error",
      });
    } finally {
      setIsSaving(false);
    }
  }, [inputValue]);

  const handleClear = useCallback(async () => {
    setIsClearing(true);
    try {
      await api.string.deleteProxyURL();
      setCurrentValue("");
      setInputValue("");
      toast.success("Proxy URL cleared");
    } catch (err) {
      toast.error("Failed to clear Proxy URL", {
        description: err instanceof Error ? err.message : "Unknown error",
      });
    } finally {
      setIsClearing(false);
    }
  }, []);

  if (isLoading) {
    return (
      <div className="flex items-center justify-between gap-4 py-2">
        <div className="flex items-center gap-2">
          <Globe className="size-4 text-muted-foreground" />
          <span className="text-sm">Proxy URL</span>
        </div>
        <Skeleton className="h-8 w-64" />
      </div>
    );
  }

  return (
    <div className="flex items-center justify-between gap-4 py-2">
      <div className="flex items-center gap-2">
        <Globe className="size-4 text-muted-foreground" />
        <span className="text-sm">Proxy URL</span>
      </div>
      <div className="flex items-center gap-2">
        <Input
          type="text"
          value={inputValue}
          onChange={(e) => setInputValue(e.target.value)}
          placeholder="http://proxy:8080"
          className="w-64"
          onKeyDown={(e) => {
            if (e.key === "Enter") handleSave();
          }}
        />
        <Button
          variant="outline"
          size="sm"
          onClick={handleSave}
          disabled={isSaving || inputValue === currentValue}
        >
          {isSaving ? <Spinner className="size-3" /> : <Save className="size-3" />}
          Save
        </Button>
        <Button
          variant="outline"
          size="sm"
          onClick={handleClear}
          disabled={isClearing || !currentValue}
        >
          {isClearing ? <Spinner className="size-3" /> : <Trash2 className="size-3" />}
          Clear
        </Button>
      </div>
    </div>
  );
}

function RoutingStrategySetting() {
  const [currentValue, setCurrentValue] = useState<string | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const [isSaving, setIsSaving] = useState(false);

  useEffect(() => {
    api.routing
      .getRoutingStrategy()
      .then((data) => setCurrentValue(data))
      .catch((err) => {
        toast.error("Failed to load Routing Strategy", {
          description: err instanceof Error ? err.message : "Unknown error",
        });
      })
      .finally(() => setIsLoading(false));
  }, []);

  const handleChange = useCallback(async (value: string) => {
    setIsSaving(true);
    try {
      await api.routing.putRoutingStrategy(value);
      setCurrentValue(value);
      toast.success(`Routing strategy set to ${value}`);
    } catch (err) {
      toast.error("Failed to update Routing Strategy", {
        description: err instanceof Error ? err.message : "Unknown error",
      });
    } finally {
      setIsSaving(false);
    }
  }, []);

  if (isLoading) {
    return (
      <div className="flex items-center justify-between gap-4 py-2">
        <div className="flex items-center gap-2">
          <Route className="size-4 text-muted-foreground" />
          <span className="text-sm">Routing Strategy</span>
        </div>
        <Skeleton className="h-8 w-40" />
      </div>
    );
  }

  return (
    <div className="flex items-center justify-between gap-4 py-2">
      <div className="flex items-center gap-2">
        <Route className="size-4 text-muted-foreground" />
        <span className="text-sm">Routing Strategy</span>
      </div>
      <div className="flex items-center gap-2">
        {isSaving && <Spinner className="size-3" />}
        <Select value={currentValue ?? ""} onValueChange={handleChange} disabled={isSaving}>
          <SelectTrigger className="w-40">
            <SelectValue placeholder="Select strategy" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="round-robin">round-robin</SelectItem>
            <SelectItem value="fill-first">fill-first</SelectItem>
          </SelectContent>
        </Select>
      </div>
    </div>
  );
}

function YAMLEditorTab() {
  const [yaml, setYaml] = useState("");
  const [isLoading, setIsLoading] = useState(true);
  const [isSaving, setIsSaving] = useState(false);

  useEffect(() => {
    api.config
      .getConfigYAML()
      .then((data) => setYaml(data))
      .catch((err) => {
        toast.error("Failed to load config.yaml", {
          description: err instanceof Error ? err.message : "Unknown error",
        });
      })
      .finally(() => setIsLoading(false));
  }, []);

  const handleSave = useCallback(async () => {
    setIsSaving(true);
    try {
      await api.config.putConfigYAML(yaml);
      toast.success("Config saved successfully");
    } catch (err) {
      toast.error("Failed to save config", {
        description: err instanceof Error ? err.message : "Unknown error",
      });
    } finally {
      setIsSaving(false);
    }
  }, [yaml]);

  if (isLoading) {
    return (
      <div className="flex flex-col gap-3">
        <Skeleton className="h-[384px] w-full" />
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-3">
      <Textarea
        value={yaml}
        onChange={(e) => setYaml(e.target.value)}
        className="font-mono min-h-[384px] resize-y"
        spellCheck={false}
      />
      <div className="flex justify-end">
        <Button onClick={handleSave} disabled={isSaving}>
          {isSaving ? <Spinner className="size-4" /> : <Save />}
          Save
        </Button>
      </div>
    </div>
  );
}

function SettingsFormTab() {
  return (
    <div className="flex flex-col gap-6">
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Settings className="size-4" />
            General Settings
          </CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-1">
          <BooleanSetting
            label="Debug Mode"
            icon={Bug}
            getter={api.boolean.getDebug}
            setter={api.boolean.putDebug}
          />
          <BooleanSetting
            label="Logging to File"
            icon={FileText}
            getter={api.boolean.getLoggingToFile}
            setter={api.boolean.putLoggingToFile}
          />
          <BooleanSetting
            label="Usage Statistics"
            icon={BarChart3}
            getter={api.boolean.getUsageStatisticsEnabled}
            setter={api.boolean.putUsageStatisticsEnabled}
          />
          <BooleanSetting
            label="Request Logging"
            icon={LogOut}
            getter={api.boolean.getRequestLogEnabled}
            setter={api.boolean.putRequestLog}
          />
          <BooleanSetting
            label="WebSocket Auth"
            icon={Shield}
            getter={api.boolean.getWsAuth}
            setter={api.boolean.putWsAuth}
          />
          <BooleanSetting
            label="Force Model Prefix"
            icon={Hash}
            getter={api.boolean.getForceModelPrefix}
            setter={api.boolean.putForceModelPrefix}
          />
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Globe className="size-4" />
            Network Settings
          </CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-1">
          <ProxyURLSetting />
          <NumberSetting
            label="Request Retry Count"
            icon={Hash}
            getter={api.integer.getRequestRetry}
            setter={api.integer.putRequestRetry}
            placeholder="0"
          />
          <NumberSetting
            label="Max Retry Interval"
            icon={Hash}
            getter={api.integer.getMaxRetryInterval}
            setter={api.integer.putMaxRetryInterval}
            placeholder="0"
          />
          <RoutingStrategySetting />
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <FolderOpen className="size-4" />
            Log Settings
          </CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-1">
          <NumberSetting
            label="Logs Max Total Size MB"
            icon={FileText}
            getter={api.integer.getLogsMaxTotalSizeMB}
            setter={api.integer.putLogsMaxTotalSizeMB}
            placeholder="0"
          />
          <NumberSetting
            label="Error Logs Max Files"
            icon={FileText}
            getter={api.integer.getErrorLogsMaxFiles}
            setter={api.integer.putErrorLogsMaxFiles}
            placeholder="0"
          />
        </CardContent>
      </Card>
    </div>
  );
}

export default function ConfigPage() {
  return (
    <div className="flex flex-col gap-6">
      <div className="flex items-center gap-2">
        <Settings className="size-5" />
        <h1 className="text-xl font-semibold">Config</h1>
      </div>
      <Tabs defaultValue="yaml">
        <TabsList>
          <TabsTrigger value="yaml">
            <FileText className="size-4" />
            YAML Editor
          </TabsTrigger>
          <TabsTrigger value="settings">
            <Settings className="size-4" />
            Settings
          </TabsTrigger>
        </TabsList>
        <TabsContent value="yaml">
          <YAMLEditorTab />
        </TabsContent>
        <TabsContent value="settings">
          <SettingsFormTab />
        </TabsContent>
      </Tabs>
    </div>
  );
}

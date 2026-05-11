"use client";

import { useState, useEffect, useCallback, useRef } from "react";
import {
  Plus,
  Pencil,
  Trash2,
  KeyRound,
  ShieldOff,
  Boxes,
} from "lucide-react";
import { toast } from "sonner";

import { api, type ModelAlias } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";

const PROVIDER_OPTIONS = [
  "claude", "codex", "gemini", "antigravity", "kimi", "kiro",
  "github-copilot", "gitlab", "cursor", "qoder", "kilo",
  "codearts", "codebuddy", "codebuddy-ai", "iflow",
];

function AliasesTab() {
  const [aliases, setAliases] = useState<Record<string, ModelAlias[]>>({});
  const [loading, setLoading] = useState(true);
  const fetchIdRef = useRef(0);

  const [addOpen, setAddOpen] = useState(false);
  const [formProvider, setFormProvider] = useState("");
  const [formName, setFormName] = useState("");
  const [formAlias, setFormAlias] = useState("");
  const [saving, setSaving] = useState(false);

  const [editTarget, setEditTarget] = useState<{ provider: string; alias: ModelAlias } | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<{ provider: string; alias: ModelAlias } | null>(null);
  const [deleting, setDeleting] = useState(false);

  const fetchAliases = useCallback(async () => {
    const fetchId = ++fetchIdRef.current;
    try {
      const data = await api.oauth.getOAuthModelAlias();
      if (fetchId === fetchIdRef.current) {
        setAliases(data || {});
      }
    } catch {
      if (fetchId === fetchIdRef.current) {
        toast.error("Failed to load model aliases");
      }
    } finally {
      if (fetchId === fetchIdRef.current) {
        setLoading(false);
      }
    }
  }, []);

  useEffect(() => {
    fetchAliases();
  }, [fetchAliases]);

  const openAdd = () => {
    setFormProvider("");
    setFormName("");
    setFormAlias("");
    setAddOpen(true);
  };

  const openEdit = (provider: string, alias: ModelAlias) => {
    setEditTarget({ provider, alias });
    setFormName(alias.name);
    setFormAlias(alias.alias);
  };

  const handleAdd = async () => {
    if (!formProvider || !formName.trim() || !formAlias.trim()) return;
    setSaving(true);
    try {
      const current = aliases[formProvider] || [];
      await api.oauth.patchOAuthModelAlias({
        provider: formProvider,
        aliases: [...current, { name: formName.trim(), alias: formAlias.trim() }],
      });
      toast.success("Model alias added");
      setAddOpen(false);
      await fetchAliases();
    } catch (err) {
      toast.error("Failed to add model alias", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setSaving(false);
    }
  };

  const handleEdit = async () => {
    if (!editTarget || !formName.trim() || !formAlias.trim()) return;
    setSaving(true);
    try {
      const current = aliases[editTarget.provider] || [];
      const updated = current.map((a) =>
        a.name === editTarget.alias.name && a.alias === editTarget.alias.alias
          ? { name: formName.trim(), alias: formAlias.trim() }
          : a,
      );
      await api.oauth.patchOAuthModelAlias({
        provider: editTarget.provider,
        aliases: updated,
      });
      toast.success("Model alias updated");
      setEditTarget(null);
      await fetchAliases();
    } catch (err) {
      toast.error("Failed to update model alias", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setSaving(false);
    }
  };

  const handleDelete = async () => {
    if (!deleteTarget) return;
    setDeleting(true);
    try {
      const current = aliases[deleteTarget.provider] || [];
      const remaining = current.filter(
        (a) => !(a.name === deleteTarget.alias.name && a.alias === deleteTarget.alias.alias),
      );
      if (remaining.length === 0) {
        await api.oauth.deleteOAuthModelAlias({ channel: deleteTarget.provider });
      } else {
        await api.oauth.patchOAuthModelAlias({
          provider: deleteTarget.provider,
          aliases: remaining,
        });
      }
      toast.success("Model alias deleted");
      setDeleteTarget(null);
      await fetchAliases();
    } catch (err) {
      toast.error("Failed to delete model alias", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setDeleting(false);
    }
  };

  const hasAliases = Object.values(aliases).some((list) => list.length > 0);

  if (loading) {
    return (
      <div className="rounded-lg border">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Provider</TableHead>
              <TableHead>Model</TableHead>
              <TableHead>Alias</TableHead>
              <TableHead className="w-20" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {Array.from({ length: 4 }).map((_, i) => (
              <TableRow key={i}>
                <TableCell><Skeleton className="h-5 w-20" /></TableCell>
                <TableCell><Skeleton className="h-5 w-32" /></TableCell>
                <TableCell><Skeleton className="h-5 w-24" /></TableCell>
                <TableCell><Skeleton className="h-5 w-16" /></TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
    );
  }

  return (
    <>
      <div className="flex items-center justify-between">
        <p className="text-sm text-muted-foreground">
          Model aliases map model names to alternative names for routing.
        </p>
        <Button size="sm" onClick={openAdd}>
          <Plus />
          Add Alias
        </Button>
      </div>

      {!hasAliases ? (
        <div className="flex flex-col items-center justify-center gap-2 rounded-lg border border-dashed p-12 text-center">
          <KeyRound className="size-10 text-muted-foreground/50" />
          <p className="text-sm text-muted-foreground">
            No model aliases configured.
          </p>
        </div>
      ) : (
        <div className="flex flex-col gap-6">
          {Object.entries(aliases)
            .filter(([, list]) => list.length > 0)
            .map(([provider, list]) => (
              <div key={provider} className="flex flex-col gap-2">
                <h3 className="text-sm font-medium">
                  <Badge variant="outline" className="mr-2">{provider}</Badge>
                  <span className="text-muted-foreground">({list.length} alias{list.length !== 1 ? "es" : ""})</span>
                </h3>
                <div className="rounded-lg border">
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>Model</TableHead>
                        <TableHead>Alias</TableHead>
                        <TableHead className="w-20" />
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {list.map((alias, idx) => (
                        <TableRow key={`${alias.name}-${idx}`}>
                          <TableCell className="font-mono text-sm">{alias.name}</TableCell>
                          <TableCell>
                            <Badge variant="outline">{alias.alias}</Badge>
                          </TableCell>
                          <TableCell>
                            <div className="flex items-center gap-1">
                              <Button
                                variant="ghost"
                                size="icon-xs"
                                onClick={() => openEdit(provider, alias)}
                              >
                                <Pencil />
                              </Button>
                              <Button
                                variant="ghost"
                                size="icon-xs"
                                onClick={() => setDeleteTarget({ provider, alias })}
                              >
                                <Trash2 />
                              </Button>
                            </div>
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </div>
              </div>
            ))}
        </div>
      )}

      <Dialog open={addOpen} onOpenChange={setAddOpen}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>Add Model Alias</DialogTitle>
            <DialogDescription>
              Create a new model alias mapping.
            </DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-4">
            <div className="flex flex-col gap-2">
              <Label htmlFor="add-provider">Provider</Label>
              <Select value={formProvider} onValueChange={setFormProvider}>
                <SelectTrigger id="add-provider" className="w-full">
                  <SelectValue placeholder="Select provider" />
                </SelectTrigger>
                <SelectContent>
                  {PROVIDER_OPTIONS.map((p) => (
                    <SelectItem key={p} value={p}>{p}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="add-model">Model</Label>
              <Input
                id="add-model"
                value={formName}
                onChange={(e) => setFormName(e.target.value)}
                placeholder="e.g. claude-sonnet-4-20250514"
              />
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="add-alias">Alias</Label>
              <Input
                id="add-alias"
                value={formAlias}
                onChange={(e) => setFormAlias(e.target.value)}
                placeholder="e.g. claude-sonnet-4"
              />
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setAddOpen(false)} disabled={saving}>
              Cancel
            </Button>
            <Button onClick={handleAdd} disabled={saving || !formProvider || !formName.trim() || !formAlias.trim()}>
              {saving ? "Saving..." : "Add"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={editTarget !== null} onOpenChange={(open) => { if (!open) setEditTarget(null); }}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>Edit Model Alias</DialogTitle>
            <DialogDescription>
              Modify the alias mapping for{" "}
              <Badge variant="outline" className="mx-1">{editTarget?.provider}</Badge>
              <span className="font-medium text-foreground">{editTarget?.alias.name}</span>.
            </DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-4">
            <div className="flex flex-col gap-2">
              <Label htmlFor="edit-model">Model</Label>
              <Input
                id="edit-model"
                value={formName}
                onChange={(e) => setFormName(e.target.value)}
              />
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="edit-alias">Alias</Label>
              <Input
                id="edit-alias"
                value={formAlias}
                onChange={(e) => setFormAlias(e.target.value)}
              />
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setEditTarget(null)} disabled={saving}>
              Cancel
            </Button>
            <Button onClick={handleEdit} disabled={saving || !formName.trim() || !formAlias.trim()}>
              {saving ? "Saving..." : "Save"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={deleteTarget !== null} onOpenChange={(open) => { if (!open) setDeleteTarget(null); }}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>Delete Model Alias</DialogTitle>
            <DialogDescription>
              Are you sure you want to delete the alias{" "}
              <Badge variant="outline" className="mx-1">{deleteTarget?.provider}</Badge>
              <span className="font-mono font-medium text-foreground">{deleteTarget?.alias.name}</span>{" "}
              → <span className="font-mono font-medium text-foreground">{deleteTarget?.alias.alias}</span>?
              This action cannot be undone.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleteTarget(null)} disabled={deleting}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={handleDelete}
              disabled={deleting}
            >
              {deleting ? "Deleting..." : "Delete"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

function ExcludedModelsTab() {
  const [excludedModels, setExcludedModels] = useState<Record<string, string[]>>({});
  const [loading, setLoading] = useState(true);
  const fetchIdRef = useRef(0);

  const [addOpen, setAddOpen] = useState(false);
  const [formProvider, setFormProvider] = useState("");
  const [formModel, setFormModel] = useState("");
  const [saving, setSaving] = useState(false);

  const [deleteTarget, setDeleteTarget] = useState<{ provider: string; model: string } | null>(null);
  const [deleting, setDeleting] = useState(false);

  const fetchExcluded = useCallback(async () => {
    const fetchId = ++fetchIdRef.current;
    try {
      const data = await api.oauth.getOAuthExcludedModels();
      if (fetchId === fetchIdRef.current) {
        setExcludedModels(data || {});
      }
    } catch {
      if (fetchId === fetchIdRef.current) {
        toast.error("Failed to load excluded models");
      }
    } finally {
      if (fetchId === fetchIdRef.current) {
        setLoading(false);
      }
    }
  }, []);

  useEffect(() => {
    fetchExcluded();
  }, [fetchExcluded]);

  const handleAdd = async () => {
    if (!formProvider || !formModel.trim()) return;
    setSaving(true);
    try {
      const current = excludedModels[formProvider] || [];
      await api.oauth.patchOAuthExcludedModels({
        provider: formProvider,
        models: [...current, formModel.trim()],
      });
      toast.success("Excluded model added");
      setAddOpen(false);
      setFormProvider("");
      setFormModel("");
      await fetchExcluded();
    } catch (err) {
      toast.error("Failed to add excluded model", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setSaving(false);
    }
  };

  const handleDelete = async () => {
    if (!deleteTarget) return;
    setDeleting(true);
    try {
      const current = excludedModels[deleteTarget.provider] || [];
      const remaining = current.filter((m) => m !== deleteTarget.model);
      if (remaining.length === 0) {
        await api.oauth.deleteOAuthExcludedModels({ provider: deleteTarget.provider });
      } else {
        await api.oauth.patchOAuthExcludedModels({
          provider: deleteTarget.provider,
          models: remaining,
        });
      }
      toast.success("Excluded model removed");
      setDeleteTarget(null);
      await fetchExcluded();
    } catch (err) {
      toast.error("Failed to remove excluded model", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setDeleting(false);
    }
  };

  const hasExcluded = Object.values(excludedModels).some((list) => list.length > 0);

  if (loading) {
    return (
      <div className="rounded-lg border">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Provider</TableHead>
              <TableHead>Model Pattern</TableHead>
              <TableHead className="w-20" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {Array.from({ length: 4 }).map((_, i) => (
              <TableRow key={i}>
                <TableCell><Skeleton className="h-5 w-20" /></TableCell>
                <TableCell><Skeleton className="h-5 w-40" /></TableCell>
                <TableCell><Skeleton className="h-5 w-16" /></TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
    );
  }

  return (
    <>
      <div className="flex items-center justify-between">
        <p className="text-sm text-muted-foreground">
          Manage models excluded from provider routing.
        </p>
        <Button size="sm" onClick={() => setAddOpen(true)}>
          <Plus />
          Add Excluded Model
        </Button>
      </div>

      {!hasExcluded ? (
        <div className="flex flex-col items-center justify-center gap-2 rounded-lg border border-dashed p-12 text-center">
          <ShieldOff className="size-10 text-muted-foreground/50" />
          <p className="text-sm text-muted-foreground">
            No excluded models configured.
          </p>
        </div>
      ) : (
        <div className="flex flex-col gap-6">
          {Object.entries(excludedModels)
            .filter(([, list]) => list.length > 0)
            .map(([provider, models]) => (
              <div key={provider} className="flex flex-col gap-2">
                <h3 className="text-sm font-medium">
                  <Badge variant="outline" className="mr-2">{provider}</Badge>
                  <span className="text-muted-foreground">({models.length} model{models.length !== 1 ? "s" : ""})</span>
                </h3>
                <div className="rounded-lg border">
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>Model Pattern</TableHead>
                        <TableHead className="w-20" />
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {models.map((model, idx) => (
                        <TableRow key={`${model}-${idx}`}>
                          <TableCell className="font-mono text-sm">{model}</TableCell>
                          <TableCell>
                            <Button
                              variant="ghost"
                              size="icon-xs"
                              onClick={() => setDeleteTarget({ provider, model })}
                            >
                              <Trash2 />
                            </Button>
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </div>
              </div>
            ))}
        </div>
      )}

      <Dialog open={addOpen} onOpenChange={setAddOpen}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>Add Excluded Model</DialogTitle>
            <DialogDescription>
              Exclude a model pattern from a specific provider.
            </DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-4">
            <div className="flex flex-col gap-2">
              <Label htmlFor="add-excluded-provider">Provider</Label>
              <Select value={formProvider} onValueChange={setFormProvider}>
                <SelectTrigger id="add-excluded-provider" className="w-full">
                  <SelectValue placeholder="Select provider" />
                </SelectTrigger>
                <SelectContent>
                  {PROVIDER_OPTIONS.map((p) => (
                    <SelectItem key={p} value={p}>
                      {p}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="add-excluded-model">Model Pattern</Label>
              <Input
                id="add-excluded-model"
                value={formModel}
                onChange={(e) => setFormModel(e.target.value)}
                placeholder="e.g. claude-3-5-haiku-*"
              />
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setAddOpen(false)} disabled={saving}>
              Cancel
            </Button>
            <Button onClick={handleAdd} disabled={saving || !formProvider || !formModel.trim()}>
              {saving ? "Saving..." : "Add"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={deleteTarget !== null} onOpenChange={(open) => { if (!open) setDeleteTarget(null); }}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>Remove Excluded Model</DialogTitle>
            <DialogDescription>
              Are you sure you want to remove{" "}
              <Badge variant="outline" className="mx-1">{deleteTarget?.provider}</Badge>
              <span className="font-mono font-medium text-foreground">{deleteTarget?.model}</span>{" "}
              from the excluded list? This action cannot be undone.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleteTarget(null)} disabled={deleting}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={handleDelete}
              disabled={deleting}
            >
              {deleting ? "Deleting..." : "Delete"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

export default function ModelsPage() {
  return (
    <div className="flex flex-col gap-6">
      <div className="flex items-center gap-2">
        <Boxes className="size-5 text-muted-foreground" />
        <h1 className="text-lg font-semibold">Models</h1>
      </div>

      <Tabs defaultValue="aliases">
        <TabsList>
          <TabsTrigger value="aliases">Model Aliases</TabsTrigger>
          <TabsTrigger value="excluded">Excluded Models</TabsTrigger>
        </TabsList>

        <TabsContent value="aliases">
          <AliasesTab />
        </TabsContent>
        <TabsContent value="excluded">
          <ExcludedModelsTab />
        </TabsContent>
      </Tabs>
    </div>
  );
}

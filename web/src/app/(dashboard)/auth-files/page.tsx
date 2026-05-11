"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { api, type AuthFile } from "@/lib/api";
import { toast } from "sonner";
import {
  Upload,
  MoreHorizontal,
  Pencil,
  Trash2,
  Power,
  FileKey2,
  Plus,
  X,
} from "lucide-react";

import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
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
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";

function statusBadge(status: string) {
  const lower = status.toLowerCase();
  if (lower === "active") {
    return (
      <Badge className="bg-emerald-500/10 text-emerald-600 dark:text-emerald-400">
        active
      </Badge>
    );
  }
  if (lower === "error") {
    return (
      <Badge className="bg-red-500/10 text-red-600 dark:text-red-400">
        error
      </Badge>
    );
  }
  if (lower === "disabled") {
    return (
      <Badge className="bg-yellow-500/10 text-yellow-600 dark:text-yellow-400">
        disabled
      </Badge>
    );
  }
  return (
    <Badge variant="secondary" className="text-muted-foreground">
      {status}
    </Badge>
  );
}

function providerBadge(provider: string) {
  return <Badge variant="outline">{provider}</Badge>;
}

interface HeaderEntry {
  key: string;
  value: string;
}

interface EditFormData {
  prefix: string;
  proxy_url: string;
  headers: HeaderEntry[];
  priority: string;
  note: string;
}

function fieldsToFormData(fields: Record<string, string>): EditFormData {
  const headers: HeaderEntry[] = [];
  if (fields.headers) {
    try {
      const parsed = JSON.parse(fields.headers) as Record<string, string>;
      for (const [k, v] of Object.entries(parsed)) {
        headers.push({ key: k, value: v });
      }
    } catch {
      headers.push({ key: "", value: "" });
    }
  }
  return {
    prefix: fields.prefix ?? "",
    proxy_url: fields.proxy_url ?? fields["proxy-url"] ?? "",
    headers: headers.length > 0 ? headers : [{ key: "", value: "" }],
    priority: fields.priority ?? "",
    note: fields.note ?? "",
  };
}

function formDataToFields(data: EditFormData): Record<string, string> {
  const fields: Record<string, string> = {};
  if (data.prefix) fields.prefix = data.prefix;
  if (data.proxy_url) fields.proxy_url = data.proxy_url;
  const cleanHeaders = data.headers.filter((h) => h.key.trim() !== "");
  if (cleanHeaders.length > 0) {
    const headerObj: Record<string, string> = {};
    for (const h of cleanHeaders) {
      headerObj[h.key] = h.value;
    }
    fields.headers = JSON.stringify(headerObj);
  }
  if (data.priority) fields.priority = data.priority;
  if (data.note) fields.note = data.note;
  return fields;
}

export default function AuthFilesPage() {
  const [files, setFiles] = useState<AuthFile[]>([]);
  const [loading, setLoading] = useState(true);
  const fetchIdRef = useRef(0);

  const fetchFiles = useCallback(async () => {
    const fetchId = ++fetchIdRef.current;
    try {
      const data = await api.authFiles.listAuthFiles();
      if (fetchId === fetchIdRef.current) {
        setFiles(data);
      }
    } catch (err) {
      if (fetchId === fetchIdRef.current) {
        toast.error("Failed to load auth files", {
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
    fetchFiles();
  }, [fetchFiles]);

  const [uploadOpen, setUploadOpen] = useState(false);
  const [uploading, setUploading] = useState(false);
  const fileInputRef = useRef<HTMLInputElement>(null);

  const handleUpload = async () => {
    const fileList = fileInputRef.current?.files;
    if (!fileList || fileList.length === 0) return;

    setUploading(true);
    try {
      for (let i = 0; i < fileList.length; i++) {
        const formData = new FormData();
        formData.append("file", fileList[i]);
        await api.authFiles.uploadAuthFile(formData);
      }
      toast.success("Auth file(s) uploaded successfully");
      setUploadOpen(false);
      if (fileInputRef.current) fileInputRef.current.value = "";
      await fetchFiles();
    } catch (err) {
      toast.error("Upload failed", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setUploading(false);
    }
  };

  const [deleteTarget, setDeleteTarget] = useState<AuthFile | null>(null);
  const [deleteAllOpen, setDeleteAllOpen] = useState(false);
  const [deleting, setDeleting] = useState(false);

  const handleDelete = async () => {
    if (!deleteTarget) return;
    setDeleting(true);
    try {
      await api.authFiles.deleteAuthFile({
        name: deleteTarget.name,
        provider: deleteTarget.provider,
      });
      toast.success("Auth file deleted");
      setDeleteTarget(null);
      await fetchFiles();
    } catch (err) {
      toast.error("Delete failed", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setDeleting(false);
    }
  };

  const handleDeleteAll = async () => {
    setDeleting(true);
    try {
      const results = await Promise.allSettled(
        files.map((f) =>
          api.authFiles.deleteAuthFile({ name: f.name, provider: f.provider })
        )
      );
      const failed = results.filter((r) => r.status === "rejected").length;
      if (failed > 0) {
        toast.error(`Deleted ${results.length - failed} of ${results.length} files, ${failed} failed`);
      } else {
        toast.success("All auth files deleted");
      }
      setDeleteAllOpen(false);
      await fetchFiles();
    } catch (err) {
      toast.error("Delete all failed", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setDeleting(false);
    }
  };

  const [editTarget, setEditTarget] = useState<AuthFile | null>(null);
  const [editForm, setEditForm] = useState<EditFormData>({
    prefix: "",
    proxy_url: "",
    headers: [{ key: "", value: "" }],
    priority: "",
    note: "",
  });
  const [saving, setSaving] = useState(false);

  const openEdit = (file: AuthFile) => {
    setEditTarget(file);
    setEditForm(fieldsToFormData(file.fields ?? {}));
  };

  const handleSaveEdit = async () => {
    if (!editTarget) return;
    setSaving(true);
    try {
      await api.authFiles.patchAuthFileFields({
        name: editTarget.name,
        provider: editTarget.provider,
        fields: formDataToFields(editForm),
      });
      toast.success("Auth file updated");
      setEditTarget(null);
      await fetchFiles();
    } catch (err) {
      toast.error("Update failed", {
        description: err instanceof Error ? err.message : undefined,
      });
    } finally {
      setSaving(false);
    }
  };

  const handleToggle = async (file: AuthFile) => {
    const newDisabled = !file.disabled;
    const originalFiles = [...files];
    setFiles((prev) =>
      prev.map((f) =>
        f.name === file.name && f.provider === file.provider
          ? { ...f, disabled: newDisabled, status: newDisabled ? "disabled" : "active" }
          : f
      )
    );
    try {
      await api.authFiles.patchAuthFileStatus({
        name: file.name,
        provider: file.provider,
        disabled: newDisabled,
      });
      toast.success(newDisabled ? "Auth file disabled" : "Auth file enabled");
    } catch (err) {
      setFiles(originalFiles);
      toast.error("Toggle failed", {
        description: err instanceof Error ? err.message : undefined,
      });
    }
  };

  const addHeaderEntry = () => {
    setEditForm((prev) => ({
      ...prev,
      headers: [...prev.headers, { key: "", value: "" }],
    }));
  };

  const removeHeaderEntry = (index: number) => {
    setEditForm((prev) => ({
      ...prev,
      headers: prev.headers.filter((_, i) => i !== index),
    }));
  };

  const updateHeaderEntry = (index: number, field: "key" | "value", val: string) => {
    setEditForm((prev) => ({
      ...prev,
      headers: prev.headers.map((h, i) => (i === index ? { ...h, [field]: val } : h)),
    }));
  };

  return (
    <div className="flex flex-col gap-6">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <FileKey2 className="size-5 text-muted-foreground" />
          <h1 className="text-lg font-semibold">Auth Files</h1>
        </div>
        <div className="flex items-center gap-2">
          {files.length > 0 && (
            <Button
              variant="destructive"
              size="sm"
              onClick={() => setDeleteAllOpen(true)}
            >
              <Trash2 />
              Delete All
            </Button>
          )}
          <Button size="sm" onClick={() => setUploadOpen(true)}>
            <Upload />
            Upload Auth File
          </Button>
        </div>
      </div>

      {loading ? (
        <div className="rounded-lg border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Provider</TableHead>
                <TableHead>Name</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Email / Account</TableHead>
                <TableHead>Priority</TableHead>
                <TableHead>Note</TableHead>
                <TableHead className="w-12">Enabled</TableHead>
                <TableHead className="w-12" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {Array.from({ length: 5 }).map((_, i) => (
                <TableRow key={i}>
                  <TableCell><Skeleton className="h-5 w-16" /></TableCell>
                  <TableCell><Skeleton className="h-5 w-24" /></TableCell>
                  <TableCell><Skeleton className="h-5 w-14" /></TableCell>
                  <TableCell><Skeleton className="h-5 w-32" /></TableCell>
                  <TableCell><Skeleton className="h-5 w-8" /></TableCell>
                  <TableCell><Skeleton className="h-5 w-20" /></TableCell>
                  <TableCell><Skeleton className="h-5 w-8" /></TableCell>
                  <TableCell><Skeleton className="h-5 w-8" /></TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      ) : files.length === 0 ? (
        <div className="flex flex-col items-center justify-center gap-2 rounded-lg border border-dashed p-12 text-center">
          <FileKey2 className="size-10 text-muted-foreground/50" />
          <p className="text-sm text-muted-foreground">
            No auth files found. Upload an auth file to get started.
          </p>
        </div>
      ) : (
        <div className="rounded-lg border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Provider</TableHead>
                <TableHead>Name</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Email / Account</TableHead>
                <TableHead>Priority</TableHead>
                <TableHead>Note</TableHead>
                <TableHead className="w-12">Enabled</TableHead>
                <TableHead className="w-12" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {files.map((file) => (
                <TableRow key={`${file.provider}-${file.name}`}>
                  <TableCell>{providerBadge(file.provider)}</TableCell>
                  <TableCell className="font-medium">{file.name}</TableCell>
                  <TableCell>{statusBadge(file.status)}</TableCell>
                  <TableCell className="text-muted-foreground">
                    {file.fields?.email || file.fields?.account || file.fields?.["email-or-account"] || "—"}
                  </TableCell>
                  <TableCell className="text-muted-foreground">
                    {file.fields?.priority ?? "—"}
                  </TableCell>
                  <TableCell className="max-w-[200px] truncate text-muted-foreground">
                    {file.fields?.note || "—"}
                  </TableCell>
                  <TableCell>
                    <Switch
                      size="sm"
                      checked={!file.disabled}
                      onCheckedChange={() => handleToggle(file)}
                      aria-label={`Toggle ${file.name}`}
                    />
                  </TableCell>
                  <TableCell>
                    <DropdownMenu>
                      <DropdownMenuTrigger asChild>
                        <Button variant="ghost" size="icon-xs">
                          <MoreHorizontal />
                        </Button>
                      </DropdownMenuTrigger>
                      <DropdownMenuContent align="end">
                        <DropdownMenuItem onClick={() => openEdit(file)}>
                          <Pencil />
                          Edit
                        </DropdownMenuItem>
                        <DropdownMenuItem onClick={() => handleToggle(file)}>
                          <Power />
                          {file.disabled ? "Enable" : "Disable"}
                        </DropdownMenuItem>
                        <DropdownMenuSeparator />
                        <DropdownMenuItem
                          variant="destructive"
                          onClick={() => setDeleteTarget(file)}
                        >
                          <Trash2 />
                          Delete
                        </DropdownMenuItem>
                      </DropdownMenuContent>
                    </DropdownMenu>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}

      <Dialog open={uploadOpen} onOpenChange={setUploadOpen}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>Upload Auth File</DialogTitle>
            <DialogDescription>
              Select one or more .json auth files to upload.
            </DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-4">
            <Input
              ref={fileInputRef}
              type="file"
              accept=".json"
              multiple
              disabled={uploading}
            />
          </div>
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => setUploadOpen(false)}
              disabled={uploading}
            >
              Cancel
            </Button>
            <Button onClick={handleUpload} disabled={uploading}>
              {uploading ? "Uploading..." : "Upload"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <AlertDialog
        open={deleteTarget !== null}
        onOpenChange={(open) => {
          if (!open) setDeleteTarget(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete Auth File</AlertDialogTitle>
            <AlertDialogDescription>
              Are you sure you want to delete{" "}
              <span className="font-medium text-foreground">
                {deleteTarget?.name}
              </span>{" "}
              ({deleteTarget?.provider})? This action cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleting}>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={handleDelete}
              disabled={deleting}
              className="bg-destructive/10 text-destructive hover:bg-destructive/20 focus-visible:border-destructive/40 focus-visible:ring-destructive/20 dark:bg-destructive/20 dark:hover:bg-destructive/30 dark:focus-visible:ring-destructive/40"
            >
              {deleting ? "Deleting..." : "Delete"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog
        open={deleteAllOpen}
        onOpenChange={setDeleteAllOpen}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete All Auth Files</AlertDialogTitle>
            <AlertDialogDescription>
              Are you sure you want to delete all {files.length} auth file(s)?
              This action cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleting}>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={handleDeleteAll}
              disabled={deleting}
              className="bg-destructive/10 text-destructive hover:bg-destructive/20 focus-visible:border-destructive/40 focus-visible:ring-destructive/20 dark:bg-destructive/20 dark:hover:bg-destructive/30 dark:focus-visible:ring-destructive/40"
            >
              {deleting ? "Deleting..." : "Delete All"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <Dialog open={editTarget !== null} onOpenChange={(open) => {
        if (!open) setEditTarget(null);
      }}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>Edit Auth File</DialogTitle>
            <DialogDescription>
              Modify fields for{" "}
              <span className="font-medium text-foreground">
                {editTarget?.name}
              </span>{" "}
              ({editTarget?.provider})
            </DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-4">
            <div className="flex flex-col gap-2">
              <Label htmlFor="edit-prefix">Prefix</Label>
              <Input
                id="edit-prefix"
                value={editForm.prefix}
                onChange={(e) =>
                  setEditForm((prev) => ({ ...prev, prefix: e.target.value }))
                }
                placeholder="Model prefix"
              />
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="edit-proxy-url">Proxy URL</Label>
              <Input
                id="edit-proxy-url"
                value={editForm.proxy_url}
                onChange={(e) =>
                  setEditForm((prev) => ({ ...prev, proxy_url: e.target.value }))
                }
                placeholder="https://proxy.example.com"
              />
            </div>
            <div className="flex flex-col gap-2">
              <Label>Headers</Label>
              <div className="flex flex-col gap-2">
                {editForm.headers.map((entry, i) => (
                  <div key={i} className="flex items-center gap-2">
                    <Input
                      value={entry.key}
                      onChange={(e) => updateHeaderEntry(i, "key", e.target.value)}
                      placeholder="Header name"
                      className="flex-1"
                    />
                    <Input
                      value={entry.value}
                      onChange={(e) => updateHeaderEntry(i, "value", e.target.value)}
                      placeholder="Header value"
                      className="flex-1"
                    />
                    <Button
                      variant="ghost"
                      size="icon-xs"
                      onClick={() => removeHeaderEntry(i)}
                      disabled={editForm.headers.length <= 1}
                    >
                      <X />
                    </Button>
                  </div>
                ))}
                <Button
                  variant="outline"
                  size="xs"
                  onClick={addHeaderEntry}
                  className="self-start"
                >
                  <Plus />
                  Add Header
                </Button>
              </div>
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="edit-priority">Priority</Label>
              <Input
                id="edit-priority"
                type="number"
                value={editForm.priority}
                onChange={(e) =>
                  setEditForm((prev) => ({ ...prev, priority: e.target.value }))
                }
                placeholder="0"
              />
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="edit-note">Note</Label>
              <Textarea
                id="edit-note"
                value={editForm.note}
                onChange={(e) =>
                  setEditForm((prev) => ({ ...prev, note: e.target.value }))
                }
                placeholder="Optional note"
                rows={3}
              />
            </div>
          </div>
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => setEditTarget(null)}
              disabled={saving}
            >
              Cancel
            </Button>
            <Button onClick={handleSaveEdit} disabled={saving}>
              {saving ? "Saving..." : "Save"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

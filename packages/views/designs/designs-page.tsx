"use client";

import { useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { FolderOpen, MoreHorizontal, PanelsTopLeft, Upload } from "lucide-react";
import { toast } from "sonner";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  designDraftListOptions,
  useCreateDesignDraft,
  useDeleteDesignDraft,
  useDesignDraftPreview,
  useRenameDesignDraft,
} from "@multica/core/design-drafts";
import type { DesignDraft } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@multica/ui/components/ui/dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@multica/ui/components/ui/dropdown-menu";
import { useT } from "../i18n";

const allowed = new Set(["html", "htm", "js", "mjs", "css", "json", "txt", "md", "svg", "png", "jpg", "jpeg", "gif", "webp", "ico", "woff", "woff2"]);

type SelectedFile = { file: File; path: string };

function formatBytes(value: number) {
  if (value < 1024) return `${value} B`;
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`;
  return `${(value / 1024 / 1024).toFixed(1)} MB`;
}

function relativePath(file: File) {
  const raw = file.webkitRelativePath || file.name;
  const parts = raw.replaceAll("\\", "/").split("/");
  return parts.length > 1 ? parts.slice(1).join("/") : parts[0] ?? "";
}

export function DesignsPage() {
  const { t } = useT("designs");
  const wsId = useWorkspaceId();
  const { data, isLoading } = useQuery(designDraftListOptions(wsId));
  const create = useCreateDesignDraft(wsId);
  const rename = useRenameDesignDraft(wsId);
  const remove = useDeleteDesignDraft(wsId);
  const preview = useDesignDraftPreview();
  const fileInput = useRef<HTMLInputElement>(null);
  const [uploadOpen, setUploadOpen] = useState(false);
  const [files, setFiles] = useState<SelectedFile[]>([]);
  const [name, setName] = useState("");
  const [entryPath, setEntryPath] = useState("");
  const [previewUrl, setPreviewUrl] = useState<string | null>(null);
  const htmlFiles = useMemo(() => files.filter((item) => /\.html?$/i.test(item.path)), [files]);

  const chooseFolder = () => fileInput.current?.click();
  const onFolder = (list: FileList | null) => {
    if (!list) return;
    const source = Array.from(list);
    const selected = source.map((file) => ({ file, path: relativePath(file) }));
    const invalid = selected.length < 1 || selected.length > 200 || selected.some(({ path }) => {
      const parts = path.split("/");
      const ext = path.split(".").pop()?.toLowerCase() ?? "";
      return !path || parts.some((part) => !part || part === ".." || part.startsWith(".")) || !allowed.has(ext);
    });
    if (invalid || !selected.some((item) => /\.html?$/i.test(item.path))) {
      toast.error(t(($) => $.invalid_folder));
      return;
    }
    const total = selected.reduce((sum, item) => sum + item.file.size, 0);
    if (total > 20 * 1024 * 1024 || selected.some((item) => item.file.size > 5 * 1024 * 1024)) {
      toast.error(t(($) => $.too_large));
      return;
    }
    const root = source[0]?.webkitRelativePath.split("/")[0] || "Design";
    setFiles(selected);
    setName(root);
    setEntryPath(selected.find((item) => /\.html?$/i.test(item.path))?.path ?? "");
    setUploadOpen(true);
  };

  const submit = async () => {
    const form = new FormData();
    form.set("name", name.trim());
    form.set("entry_path", entryPath);
    form.set("paths", JSON.stringify(files.map((item) => item.path)));
    files.forEach((item) => form.append("files", item.file, item.file.name));
    try {
      await create.mutateAsync(form);
      setUploadOpen(false); setFiles([]); setName(""); setEntryPath("");
    } catch { toast.error(t(($) => $.upload_failed)); }
  };

  const openPreview = async (draft: DesignDraft) => {
    try { const token = await preview.mutateAsync(draft.id); setPreviewUrl(token.preview_url); }
    catch { toast.error(t(($) => $.preview_failed)); }
  };

  const renameDraft = async (draft: DesignDraft) => {
    const next = window.prompt(t(($) => $.new_name), draft.name)?.trim();
    if (!next || next === draft.name) return;
    try { await rename.mutateAsync({ id: draft.id, name: next }); } catch { toast.error(t(($) => $.rename_failed)); }
  };

  const deleteDraft = async (draft: DesignDraft) => {
    if (!window.confirm(t(($) => $.confirm_delete))) return;
    try { await remove.mutateAsync(draft.id); } catch { toast.error(t(($) => $.delete_failed)); }
  };

  const drafts = data?.design_drafts ?? [];
  const enabled = data?.preview_enabled === true;
  return (
    <div className="flex h-full min-h-0 flex-col">
      <input ref={(node) => { fileInput.current = node; node?.setAttribute("webkitdirectory", ""); }} type="file" multiple className="hidden" onChange={(event) => { onFolder(event.target.files); event.currentTarget.value = ""; }} />
      <div className="flex items-center justify-between border-b px-6 py-4">
        <div><h1 className="text-lg font-semibold">{t(($) => $.title)}</h1><p className="text-sm text-muted-foreground">{t(($) => $.count, { count: drafts.length })}</p></div>
        <Button onClick={chooseFolder} disabled={!enabled}><Upload className="mr-2 size-4" />{t(($) => $.upload)}</Button>
      </div>
      {!enabled && <div className="mx-6 mt-4 rounded-md border border-dashed p-3 text-sm text-muted-foreground">{t(($) => $.not_configured)}</div>}
      <div className="min-h-0 flex-1 overflow-auto p-6">
        {isLoading ? <p className="text-sm text-muted-foreground">{t(($) => $.loading)}</p> : drafts.length === 0 ? (
          <div className="flex h-64 flex-col items-center justify-center text-center"><PanelsTopLeft className="mb-3 size-10 text-muted-foreground" /><h2 className="font-medium">{t(($) => $.empty_title)}</h2><p className="mt-1 max-w-md text-sm text-muted-foreground">{t(($) => $.empty_description)}</p></div>
        ) : (
          <div className="overflow-hidden rounded-lg border">
            <div className="grid grid-cols-[minmax(220px,2fr)_minmax(160px,1fr)_80px_100px_minmax(140px,1fr)_44px] gap-3 border-b bg-muted/40 px-4 py-2 text-xs font-medium text-muted-foreground">
              <span>{t(($) => $.name)}</span><span>{t(($) => $.entry)}</span><span>{t(($) => $.files)}</span><span>{t(($) => $.size)}</span><span>{t(($) => $.uploaded_by)}</span><span />
            </div>
            {drafts.map((draft) => <div key={draft.id} className="grid grid-cols-[minmax(220px,2fr)_minmax(160px,1fr)_80px_100px_minmax(140px,1fr)_44px] items-center gap-3 border-b px-4 py-3 last:border-b-0 hover:bg-muted/30">
              <button className="flex min-w-0 items-center gap-2 text-left font-medium" onClick={() => openPreview(draft)} disabled={!draft.preview_enabled}><FolderOpen className="size-4 shrink-0" /><span className="truncate">{draft.name}</span></button>
              <span className="truncate text-sm text-muted-foreground">{draft.entry_path}</span><span className="text-sm">{draft.files.length}</span><span className="text-sm">{formatBytes(draft.total_size)}</span>
              <span className="min-w-0 text-sm text-muted-foreground"><span className="block truncate">{draft.created_by_name}</span><span className="block truncate text-xs">{new Date(draft.created_at).toLocaleString()}</span></span>
              {draft.can_manage ? <DropdownMenu><DropdownMenuTrigger render={<Button variant="ghost" size="icon" aria-label={t(($) => $.actions)} />}><MoreHorizontal className="size-4" /></DropdownMenuTrigger><DropdownMenuContent align="end"><DropdownMenuItem onClick={() => renameDraft(draft)}>{t(($) => $.rename)}</DropdownMenuItem><DropdownMenuItem variant="destructive" onClick={() => deleteDraft(draft)}>{t(($) => $.delete)}</DropdownMenuItem></DropdownMenuContent></DropdownMenu> : <span />}
            </div>)}
          </div>
        )}
      </div>
      <Dialog open={uploadOpen} onOpenChange={setUploadOpen}><DialogContent><DialogHeader><DialogTitle>{t(($) => $.upload_title)}</DialogTitle><DialogDescription>{t(($) => $.upload_description)}</DialogDescription></DialogHeader><div className="space-y-4"><div><label className="mb-1 block text-sm font-medium">{t(($) => $.name)}</label><Input value={name} onChange={(e) => setName(e.target.value)} maxLength={200} /></div><div><label className="mb-1 block text-sm font-medium">{t(($) => $.entry)}</label><select className="h-9 w-full rounded-md border bg-background px-3 text-sm" value={entryPath} onChange={(e) => setEntryPath(e.target.value)}>{htmlFiles.map((item) => <option key={item.path} value={item.path}>{item.path}</option>)}</select></div><p className="text-xs text-muted-foreground">{files.length} {t(($) => $.files)} · {formatBytes(files.reduce((sum, item) => sum + item.file.size, 0))}</p></div><DialogFooter><Button variant="outline" onClick={() => setUploadOpen(false)}>{t(($) => $.cancel)}</Button><Button onClick={submit} disabled={!name.trim() || !entryPath || create.isPending}>{t(($) => $.upload)}</Button></DialogFooter></DialogContent></Dialog>
      <Dialog open={previewUrl !== null} onOpenChange={(open) => { if (!open) setPreviewUrl(null); }}><DialogContent className="h-[90vh] max-w-[95vw] p-0 sm:max-w-[95vw]"><DialogHeader className="sr-only"><DialogTitle>{t(($) => $.preview)}</DialogTitle><DialogDescription>{t(($) => $.preview)}</DialogDescription></DialogHeader>{previewUrl && <iframe title={t(($) => $.preview)} src={previewUrl} sandbox="allow-scripts" className="h-full w-full rounded-lg bg-white" />}</DialogContent></Dialog>
    </div>
  );
}

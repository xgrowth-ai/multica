import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";

export const designDraftKeys = {
  all: (wsId: string) => ["design-drafts", wsId] as const,
  list: (wsId: string) => [...designDraftKeys.all(wsId), "list"] as const,
};

export function designDraftListOptions(wsId: string) {
  return queryOptions({ queryKey: designDraftKeys.list(wsId), queryFn: () => api.listDesignDrafts() });
}

export function useCreateDesignDraft(wsId: string) {
  const client = useQueryClient();
  return useMutation({ mutationFn: (data: FormData) => api.createDesignDraft(data), onSuccess: () => client.invalidateQueries({ queryKey: designDraftKeys.all(wsId) }) });
}

export function useRenameDesignDraft(wsId: string) {
  const client = useQueryClient();
  return useMutation({ mutationFn: ({ id, name }: { id: string; name: string }) => api.renameDesignDraft(id, name), onSuccess: () => client.invalidateQueries({ queryKey: designDraftKeys.all(wsId) }) });
}

export function useDeleteDesignDraft(wsId: string) {
  const client = useQueryClient();
  return useMutation({ mutationFn: (id: string) => api.deleteDesignDraft(id), onSuccess: () => client.invalidateQueries({ queryKey: designDraftKeys.all(wsId) }) });
}

export function useDesignDraftPreview() {
  return useMutation({ mutationFn: (id: string) => api.createDesignDraftPreviewToken(id) });
}

export interface DesignDraftFile {
  path: string;
  content_type: string;
  size: number;
}

export interface DesignDraft {
  id: string;
  workspace_id: string;
  name: string;
  entry_path: string;
  files: DesignDraftFile[];
  total_size: number;
  created_by: string;
  created_by_name: string;
  can_manage: boolean;
  created_at: string;
  updated_at: string;
  preview_enabled: boolean;
}

export interface ListDesignDraftsResponse {
  design_drafts: DesignDraft[];
  preview_enabled: boolean;
}

export interface DesignDraftPreviewToken {
  preview_url: string;
  expires_at: string;
}

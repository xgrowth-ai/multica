package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	maxDesignDraftFiles    = 200
	maxDesignDraftFileSize = 5 << 20
	maxDesignDraftTotal    = 20 << 20
	maxDesignDraftRequest  = 22 << 20
	designPreviewTTL       = 10 * time.Minute
)

type designDraftFile struct {
	Path        string `json:"path"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	StorageKey  string `json:"storage_key,omitempty"`
}

type designDraftResponse struct {
	ID              string            `json:"id"`
	WorkspaceID     string            `json:"workspace_id"`
	Name            string            `json:"name"`
	EntryPath       string            `json:"entry_path"`
	Files           []designDraftFile `json:"files"`
	TotalSize       int64             `json:"total_size"`
	CreatedBy       string            `json:"created_by"`
	CreatedByName   string            `json:"created_by_name"`
	CanManage       bool              `json:"can_manage"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
	PreviewEnabled  bool              `json:"preview_enabled"`
	storageRevision string
}

func redactDesignDraftStorageKeys(d *designDraftResponse) {
	for i := range d.Files {
		d.Files[i].StorageKey = ""
	}
}

func (h *Handler) designPreviewEnabled() bool {
	return h.Storage != nil && h.cfg.DesignPreviewPublicURL != "" && h.cfg.DesignPreviewSecret != ""
}

func scanDesignDraft(row pgx.Row) (designDraftResponse, error) {
	var d designDraftResponse
	var manifest []byte
	var id, workspaceID, revision, createdBy uuid.UUID
	err := row.Scan(&id, &workspaceID, &d.Name, &d.EntryPath, &revision, &manifest,
		&d.TotalSize, &createdBy, &d.CreatedAt, &d.UpdatedAt, &d.CreatedByName)
	if err != nil {
		return d, err
	}
	d.ID, d.WorkspaceID, d.CreatedBy = id.String(), workspaceID.String(), createdBy.String()
	d.storageRevision = revision.String()
	if err := json.Unmarshal(manifest, &d.Files); err != nil {
		return d, err
	}
	return d, nil
}

const designDraftSelect = `SELECT d.id, d.workspace_id, d.name, d.entry_path, d.storage_revision,
       d.manifest, d.total_size, d.created_by, d.created_at, d.updated_at,
       COALESCE(NULLIF(u.name, ''), u.email, 'Unknown') AS created_by_name
FROM design_draft d LEFT JOIN "user" u ON u.id = d.created_by`

func (h *Handler) ListDesignDrafts(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	userID := requestUserID(r)
	member, err := h.getWorkspaceMember(r.Context(), userID, workspaceID)
	if err != nil {
		writeError(w, http.StatusForbidden, "not a member of this workspace")
		return
	}
	rows, err := h.DB.Query(r.Context(), designDraftSelect+` WHERE d.workspace_id = $1 ORDER BY d.created_at DESC`, parseUUID(workspaceID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list design drafts")
		return
	}
	defer rows.Close()
	drafts := make([]designDraftResponse, 0)
	for rows.Next() {
		d, err := scanDesignDraft(rows)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list design drafts")
			return
		}
		d.CanManage = d.CreatedBy == userID || roleAllowed(member.Role, "owner", "admin")
		d.PreviewEnabled = h.designPreviewEnabled()
		redactDesignDraftStorageKeys(&d)
		drafts = append(drafts, d)
	}
	writeJSON(w, http.StatusOK, map[string]any{"design_drafts": drafts, "preview_enabled": h.designPreviewEnabled()})
}

var designDraftContentTypes = map[string]string{
	".html": "text/html; charset=utf-8", ".htm": "text/html; charset=utf-8",
	".js": "application/javascript; charset=utf-8", ".mjs": "application/javascript; charset=utf-8",
	".css": "text/css; charset=utf-8", ".json": "application/json; charset=utf-8",
	".txt": "text/plain; charset=utf-8", ".md": "text/plain; charset=utf-8",
	".svg": "image/svg+xml", ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".webp": "image/webp", ".ico": "image/x-icon",
	".woff": "font/woff", ".woff2": "font/woff2",
}

func normalizeDesignDraftPath(raw string) (string, string, error) {
	raw = strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/")
	if raw == "" || strings.HasPrefix(raw, "/") || strings.ContainsRune(raw, '\x00') {
		return "", "", errors.New("invalid file path")
	}
	parts := strings.Split(raw, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, ".") {
			return "", "", errors.New("invalid file path")
		}
	}
	cleaned := path.Clean(raw)
	ext := strings.ToLower(path.Ext(cleaned))
	contentType, ok := designDraftContentTypes[ext]
	if !ok {
		return "", "", fmt.Errorf("unsupported file type: %s", ext)
	}
	return cleaned, contentType, nil
}

func (h *Handler) CreateDesignDraft(w http.ResponseWriter, r *http.Request) {
	if !h.designPreviewEnabled() {
		writeError(w, http.StatusServiceUnavailable, "design preview is not configured")
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	userID := requestUserID(r)
	r.Body = http.MaxBytesReader(w, r.Body, maxDesignDraftRequest)
	if err := r.ParseMultipartForm(maxDesignDraftRequest); err != nil {
		writeError(w, http.StatusBadRequest, "design draft is too large or invalid")
		return
	}
	defer r.MultipartForm.RemoveAll()
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" || len([]rune(name)) > 200 {
		writeError(w, http.StatusBadRequest, "name must be between 1 and 200 characters")
		return
	}
	var paths []string
	if err := json.Unmarshal([]byte(r.FormValue("paths")), &paths); err != nil {
		writeError(w, http.StatusBadRequest, "invalid paths manifest")
		return
	}
	headers := r.MultipartForm.File["files"]
	if len(headers) == 0 || len(headers) > maxDesignDraftFiles || len(paths) != len(headers) {
		writeError(w, http.StatusBadRequest, "design draft must contain 1 to 200 matching files")
		return
	}

	draftID, _ := uuid.NewV7()
	revision, _ := uuid.NewV7()
	files := make([]designDraftFile, 0, len(headers))
	seen := make(map[string]struct{}, len(headers))
	uploadedKeys := make([]string, 0, len(headers))
	cleanup := func() { h.Storage.DeleteKeys(r.Context(), uploadedKeys) }
	var total int64
	for i, header := range headers {
		filePath, contentType, err := normalizeDesignDraftPath(paths[i])
		if err != nil {
			cleanup()
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if _, exists := seen[filePath]; exists {
			cleanup()
			writeError(w, http.StatusBadRequest, "duplicate file path")
			return
		}
		seen[filePath] = struct{}{}
		if header.Size < 0 || header.Size > maxDesignDraftFileSize || total+header.Size > maxDesignDraftTotal {
			cleanup()
			writeError(w, http.StatusBadRequest, "design draft exceeds the file size limit")
			return
		}
		f, err := header.Open()
		if err != nil {
			cleanup()
			writeError(w, http.StatusBadRequest, "failed to read file")
			return
		}
		data, readErr := io.ReadAll(io.LimitReader(f, maxDesignDraftFileSize+1))
		f.Close()
		if readErr != nil || len(data) > maxDesignDraftFileSize {
			cleanup()
			writeError(w, http.StatusBadRequest, "failed to read file")
			return
		}
		key := fmt.Sprintf("design-drafts/%s/%s/%s/%s", workspaceID, draftID, revision, filePath)
		if _, err := h.Storage.Upload(r.Context(), key, data, contentType, path.Base(filePath)); err != nil {
			cleanup()
			writeError(w, http.StatusInternalServerError, "failed to upload design draft")
			return
		}
		uploadedKeys = append(uploadedKeys, key)
		total += int64(len(data))
		files = append(files, designDraftFile{Path: filePath, ContentType: contentType, Size: int64(len(data)), StorageKey: key})
	}
	entryPath, entryType, err := normalizeDesignDraftPath(r.FormValue("entry_path"))
	if err != nil || !strings.HasPrefix(entryType, "text/html") {
		cleanup()
		writeError(w, http.StatusBadRequest, "entry_path must be an uploaded HTML file")
		return
	}
	if _, ok := seen[entryPath]; !ok {
		cleanup()
		writeError(w, http.StatusBadRequest, "entry_path must be an uploaded HTML file")
		return
	}
	manifest, _ := json.Marshal(files)
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		cleanup()
		writeError(w, http.StatusInternalServerError, "failed to create design draft")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := h.Queries.WithTx(tx).LockWorkspaceForDelete(r.Context(), parseUUID(workspaceID)); err != nil {
		cleanup()
		writeError(w, http.StatusForbidden, "workspace is no longer available")
		return
	}
	row := tx.QueryRow(r.Context(), `INSERT INTO design_draft
        (id, workspace_id, name, entry_path, storage_revision, manifest, total_size, created_by)
        VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
        RETURNING id, workspace_id, name, entry_path, storage_revision, manifest, total_size,
                  created_by, created_at, updated_at, $9::text`,
		draftID, parseUUID(workspaceID), name, entryPath, revision, manifest, total, parseUUID(userID), "You")
	d, err := scanDesignDraft(row)
	if err != nil {
		cleanup()
		writeError(w, http.StatusInternalServerError, "failed to create design draft")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		cleanup()
		writeError(w, http.StatusInternalServerError, "failed to create design draft")
		return
	}
	d.CanManage, d.PreviewEnabled = true, true
	redactDesignDraftStorageKeys(&d)
	writeJSON(w, http.StatusCreated, d)
}

func (h *Handler) loadDesignDraftForUser(w http.ResponseWriter, r *http.Request) (designDraftResponse, bool) {
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "id")
	if !ok {
		return designDraftResponse{}, false
	}
	workspaceID := h.resolveWorkspaceID(r)
	d, err := scanDesignDraft(h.DB.QueryRow(r.Context(), designDraftSelect+` WHERE d.id=$1 AND d.workspace_id=$2`, id, parseUUID(workspaceID)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "design draft not found")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to load design draft")
		}
		return designDraftResponse{}, false
	}
	return d, true
}

func (h *Handler) canManageDesignDraft(r *http.Request, d designDraftResponse) bool {
	if d.CreatedBy == requestUserID(r) {
		return true
	}
	member, err := h.getWorkspaceMember(r.Context(), requestUserID(r), d.WorkspaceID)
	return err == nil && roleAllowed(member.Role, "owner", "admin")
}

func (h *Handler) RenameDesignDraft(w http.ResponseWriter, r *http.Request) {
	d, ok := h.loadDesignDraftForUser(w, r)
	if !ok {
		return
	}
	if !h.canManageDesignDraft(r, d) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len([]rune(req.Name)) > 200 {
		writeError(w, http.StatusBadRequest, "name must be between 1 and 200 characters")
		return
	}
	_, err := h.DB.Exec(r.Context(), `UPDATE design_draft SET name=$3, updated_at=now() WHERE id=$1 AND workspace_id=$2`, parseUUID(d.ID), parseUUID(d.WorkspaceID), req.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to rename design draft")
		return
	}
	d.Name, d.UpdatedAt, d.CanManage, d.PreviewEnabled = req.Name, time.Now(), true, h.designPreviewEnabled()
	redactDesignDraftStorageKeys(&d)
	writeJSON(w, http.StatusOK, d)
}

func (h *Handler) DeleteDesignDraft(w http.ResponseWriter, r *http.Request) {
	d, ok := h.loadDesignDraftForUser(w, r)
	if !ok {
		return
	}
	if !h.canManageDesignDraft(r, d) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}
	tag, err := h.DB.Exec(r.Context(), `DELETE FROM design_draft WHERE id=$1 AND workspace_id=$2`, parseUUID(d.ID), parseUUID(d.WorkspaceID))
	if err != nil || tag.RowsAffected() != 1 {
		writeError(w, http.StatusInternalServerError, "failed to delete design draft")
		return
	}
	keys := make([]string, len(d.Files))
	for i := range d.Files {
		keys[i] = d.Files[i].StorageKey
	}
	if h.Storage != nil {
		h.Storage.DeleteKeys(r.Context(), keys)
	}
	w.WriteHeader(http.StatusNoContent)
}

type designPreviewClaims struct {
	DraftID  string `json:"d"`
	Revision string `json:"r"`
	Expires  int64  `json:"e"`
}

func (h *Handler) signDesignPreview(claims designPreviewClaims) string {
	payload, _ := json.Marshal(claims)
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(h.cfg.DesignPreviewSecret))
	mac.Write([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (h *Handler) verifyDesignPreview(token string) (designPreviewClaims, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return designPreviewClaims{}, false
	}
	mac := hmac.New(sha256.New, []byte(h.cfg.DesignPreviewSecret))
	mac.Write([]byte(parts[0]))
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(sig, mac.Sum(nil)) {
		return designPreviewClaims{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return designPreviewClaims{}, false
	}
	var claims designPreviewClaims
	if json.Unmarshal(payload, &claims) != nil || claims.Expires < time.Now().Unix() {
		return designPreviewClaims{}, false
	}
	return claims, true
}

func (h *Handler) CreateDesignDraftPreviewToken(w http.ResponseWriter, r *http.Request) {
	if !h.designPreviewEnabled() {
		writeError(w, http.StatusServiceUnavailable, "design preview is not configured")
		return
	}
	d, ok := h.loadDesignDraftForUser(w, r)
	if !ok {
		return
	}
	claims := designPreviewClaims{DraftID: d.ID, Revision: d.storageRevision, Expires: time.Now().Add(designPreviewTTL).Unix()}
	token := h.signDesignPreview(claims)
	pathParts := strings.Split(strings.TrimPrefix(d.EntryPath, "/"), "/")
	for i := range pathParts {
		pathParts[i] = url.PathEscape(pathParts[i])
	}
	previewURL := h.cfg.DesignPreviewPublicURL + "/p/" + token + "/" + strings.Join(pathParts, "/")
	writeJSON(w, http.StatusOK, map[string]any{"preview_url": previewURL, "expires_at": time.Unix(claims.Expires, 0)})
}

func (h *Handler) ServeDesignDraftPreview(w http.ResponseWriter, r *http.Request) {
	configured, err := url.Parse(h.cfg.DesignPreviewPublicURL)
	if err != nil || !strings.EqualFold(r.Host, configured.Host) || !h.designPreviewEnabled() {
		http.NotFound(w, r)
		return
	}
	claims, ok := h.verifyDesignPreview(chi.URLParam(r, "token"))
	if !ok {
		http.Error(w, "Preview link expired or invalid", http.StatusUnauthorized)
		return
	}
	draftID, err := uuid.Parse(claims.DraftID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var revision uuid.UUID
	var manifest []byte
	err = h.DB.QueryRow(r.Context(), `SELECT storage_revision, manifest FROM design_draft WHERE id=$1`, draftID).Scan(&revision, &manifest)
	if err != nil || revision.String() != claims.Revision {
		http.NotFound(w, r)
		return
	}
	requested := chi.URLParam(r, "*")
	requested, _, err = normalizeDesignDraftPath(requested)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var files []designDraftFile
	if json.Unmarshal(manifest, &files) != nil {
		http.Error(w, "Invalid preview", http.StatusInternalServerError)
		return
	}
	var selected *designDraftFile
	for i := range files {
		if files[i].Path == requested {
			selected = &files[i]
			break
		}
	}
	if selected == nil {
		http.NotFound(w, r)
		return
	}
	reader, err := h.Storage.GetReader(r.Context(), selected.StorageKey)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer reader.Close()
	allowedAncestors := append([]string(nil), h.cfg.AttachmentFrameAncestors...)
	if h.cfg.PublicURL != "" {
		allowedAncestors = append(allowedAncestors, h.cfg.PublicURL)
	}
	ancestors := "'none'"
	if len(allowedAncestors) > 0 {
		ancestors = strings.Join(allowedAncestors, " ")
	}
	w.Header().Set("Content-Security-Policy", designDraftPreviewCSP(ancestors))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", selected.ContentType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", selected.Size))
	_, _ = io.Copy(w, reader)
}

func designDraftPreviewCSP(ancestors string) string {
	return "default-src 'none'; script-src 'self' 'unsafe-inline' https: blob:; style-src 'self' 'unsafe-inline' https:; img-src 'self' https: data: blob:; font-src 'self' https: data:; worker-src 'self' https: blob:; connect-src https: wss:; media-src https:; frame-src https:; object-src 'none'; form-action 'none'; base-uri 'none'; frame-ancestors " + ancestors
}

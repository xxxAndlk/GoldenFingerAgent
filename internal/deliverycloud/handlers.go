package deliverycloud

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"goldenfinger/agent/internal/store"
)

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	device, ok := s.require(w, r, "delivery:write")
	if !ok {
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 200 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Idempotency-Key required"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.maxBytes+(1<<20))
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "too large") {
			status = http.StatusRequestEntityTooLarge
		}
		writeJSON(w, status, map[string]string{"error": "invalid multipart body"})
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	artifactID := strings.TrimSpace(r.FormValue("artifact_id"))
	version, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("artifact_version")))
	targetID := strings.TrimSpace(r.FormValue("target_device_id"))
	name := sanitizeName(r.FormValue("name"))
	declaredSHA := normalizeSHA(r.FormValue("sha256"))
	declaredSize, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("size_bytes")), 10, 64)
	allowExecutable := strings.EqualFold(r.FormValue("allow_executable"), "true")
	if artifactID == "" || version <= 0 || targetID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "artifact_id, artifact_version and target_device_id required"})
		return
	}
	if !allowExecutable && isExecutableName(name) {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "executable files are refused by default"})
		return
	}
	target, err := s.repos.CloudDevices.Get(r.Context(), device.OwnerUserID, targetID)
	if err != nil || target.RevokedAt != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "target device not available"})
		return
	}

	if existing, err := s.repos.CloudDeliveries.GetByIdempotencyKey(r.Context(), device.OwnerUserID, key); err == nil {
		if sameDelivery(existing, artifactID, version, declaredSHA) {
			writeJSON(w, http.StatusOK, existing)
			return
		}
		writeJSON(w, http.StatusConflict, map[string]string{"error": "idempotency key reused with different content"})
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file part required"})
		return
	}
	defer file.Close()
	if header.Size > s.maxBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "file too large"})
		return
	}
	if declaredSize > 0 && header.Size >= 0 && declaredSize != header.Size {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "size_bytes mismatch"})
		return
	}
	if err := os.MkdirAll(s.tmpDir, 0o700); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "storage unavailable"})
		return
	}
	tmp, err := os.CreateTemp(s.tmpDir, "upload-*")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "storage unavailable"})
		return
	}
	tmpPath := tmp.Name()
	fail := func(status int, message string) {
		tmp.Close()
		_ = os.Remove(tmpPath)
		writeJSON(w, status, map[string]string{"error": message})
	}
	hasher := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(file, s.maxBytes+1))
	if err != nil {
		fail(http.StatusBadRequest, "read upload failed")
		return
	}
	if n > s.maxBytes {
		fail(http.StatusRequestEntityTooLarge, "file too large")
		return
	}
	if err := tmp.Sync(); err != nil {
		fail(http.StatusInternalServerError, "sync failed")
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "close failed"})
		return
	}
	gotSHA := hex.EncodeToString(hasher.Sum(nil))
	if declaredSHA != "" && declaredSHA != gotSHA {
		_ = os.Remove(tmpPath)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sha256 mismatch"})
		return
	}
	if declaredSize > 0 && declaredSize != n {
		_ = os.Remove(tmpPath)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "size_bytes mismatch"})
		return
	}
	mimeType := strings.TrimSpace(r.FormValue("mime_type"))
	if mimeType == "" {
		mimeType = detectMime(tmpPath, name)
	}

	delivery := &store.CloudDelivery{
		DeliveryID: uuid.NewString(), OwnerUserID: device.OwnerUserID,
		UploadedByDevice: device.ID, TargetDeviceID: targetID,
		ArtifactID: artifactID, ArtifactVersion: version,
		Name: name, MimeType: mimeType, SizeBytes: n, SHA256: gotSHA,
		IdempotencyKey: key, Status: store.DeliveryActive,
		CreatedAt: s.now(), ExpiresAt: s.now().Add(s.ttl),
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		_ = os.Remove(tmpPath)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "storage unavailable"})
		return
	}
	finalPath := filepath.Join(s.dir, delivery.DeliveryID)
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store file failed"})
		return
	}
	delivery.StoragePath = finalPath
	if err := s.repos.CloudDeliveries.Create(r.Context(), delivery); err != nil {
		_ = os.Remove(finalPath)
		if errors.Is(err, store.ErrConflict) {
			if existing, getErr := s.repos.CloudDeliveries.GetByIdempotencyKey(r.Context(), device.OwnerUserID, key); getErr == nil &&
				sameDelivery(existing, artifactID, version, declaredSHA) {
				writeJSON(w, http.StatusOK, existing)
				return
			}
		}
		s.logf("[deliverycloud] create delivery failed: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "create delivery failed"})
		return
	}
	writeJSON(w, http.StatusCreated, delivery)
}

func sameDelivery(d *store.CloudDelivery, artifactID string, version int, sha string) bool {
	if d == nil || d.ArtifactID != artifactID || d.ArtifactVersion != version {
		return false
	}
	if sha != "" && d.SHA256 != sha {
		return false
	}
	return true
}

func (s *Server) handleInbox(w http.ResponseWriter, r *http.Request) {
	device, ok := s.require(w, r, "delivery:read")
	if !ok {
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 200 {
		limit = 200
	}
	items, err := s.repos.CloudDeliveries.Inbox(r.Context(), device.ID, limit)
	if err != nil {
		s.logf("[deliverycloud] inbox failed: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "inbox failed"})
		return
	}
	out := make([]store.CloudDelivery, 0, len(items))
	now := s.now()
	for _, item := range items {
		if now.After(item.ExpiresAt) {
			continue
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"deliveries": out})
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	device, ok := s.require(w, r, "delivery:read")
	if !ok {
		return
	}
	item, err := s.repos.CloudDeliveries.Get(r.Context(), device.OwnerUserID, r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) handleContent(w http.ResponseWriter, r *http.Request) {
	device, ok := s.require(w, r, "delivery:read")
	if !ok {
		return
	}
	item, err := s.repos.CloudDeliveries.Get(r.Context(), device.OwnerUserID, r.PathValue("id"))
	if err != nil || item.TargetDeviceID != device.ID {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if item.Status != store.DeliveryActive || s.now().After(item.ExpiresAt) {
		writeJSON(w, http.StatusGone, map[string]string{"error": "delivery expired or revoked"})
		return
	}
	if !s.withinDir(item.StoragePath) {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "storage path invalid"})
		return
	}
	f, err := os.Open(item.StoragePath)
	if err != nil {
		writeJSON(w, http.StatusGone, map[string]string{"error": "content unavailable"})
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", firstNonEmpty(item.MimeType, "application/octet-stream"))
	w.Header().Set("X-Content-Hash", item.SHA256)
	w.Header().Set("Content-Disposition", contentDisposition(item.Name))
	http.ServeContent(w, r, item.Name, item.CreatedAt, f)
}

func (s *Server) handleAck(w http.ResponseWriter, r *http.Request) {
	device, ok := s.require(w, r, "delivery:read")
	if !ok {
		return
	}
	item, err := s.repos.CloudDeliveries.Get(r.Context(), device.OwnerUserID, r.PathValue("id"))
	if err != nil || item.TargetDeviceID != device.ID {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if _, err := s.repos.CloudDeliveries.Ack(r.Context(), device.OwnerUserID, item.DeliveryID, s.now()); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "ack failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "acked"})
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	device, ok := s.require(w, r, "delivery:write")
	if !ok {
		return
	}
	item, err := s.repos.CloudDeliveries.Get(r.Context(), device.OwnerUserID, r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if s.withinDir(item.StoragePath) {
		_ = os.Remove(item.StoragePath)
	}
	if _, err := s.repos.CloudDeliveries.Revoke(r.Context(), device.OwnerUserID, item.DeliveryID, s.now()); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "revoke failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

func (s *Server) withinDir(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	root, err := filepath.Abs(s.dir)
	if err != nil {
		return false
	}
	full, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false
	}
	return true
}

func sanitizeName(name string) string {
	name = filepath.Base(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == '/' || r == '\\' || r == '"' {
			return -1
		}
		return r
	}, name)
	if name == "" || name == "." {
		return "file"
	}
	return name
}

func normalizeSHA(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "sha256:")
	return strings.ToLower(value)
}

func detectMime(path, name string) string {
	if ext := strings.ToLower(filepath.Ext(name)); ext != "" {
		if value := mime.TypeByExtension(ext); value != "" {
			return value
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return "application/octet-stream"
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	return http.DetectContentType(buf[:n])
}

var executableExts = map[string]bool{
	".exe": true, ".com": true, ".bat": true, ".cmd": true, ".ps1": true, ".psm1": true,
	".vbs": true, ".vbe": true, ".wsf": true, ".wsh": true, ".scr": true, ".msi": true,
	".msp": true, ".lnk": true, ".reg": true, ".apk": true, ".app": true, ".dmg": true,
	".pkg": true, ".deb": true, ".rpm": true, ".jar": true, ".sh": true, ".bash": true,
	".zsh": true, ".py": true, ".rb": true, ".pl": true, ".js": true, ".jse": true,
}

func isExecutableName(name string) bool {
	return executableExts[strings.ToLower(filepath.Ext(name))]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func contentDisposition(filename string) string {
	filename = sanitizeName(filename)
	var ascii strings.Builder
	for _, r := range filename {
		if r < 32 || r > 126 || r == '"' || r == '\\' {
			ascii.WriteByte('_')
			continue
		}
		ascii.WriteRune(r)
	}
	fallback := ascii.String()
	if fallback == "" {
		fallback = "download"
	}
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, fallback, urlEscape(filename))
}

func urlEscape(value string) string {
	var b strings.Builder
	for _, r := range []byte(value) {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '-' || r == '_' || r == '.' || r == '~' {
			b.WriteByte(r)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", r)
	}
	return b.String()
}

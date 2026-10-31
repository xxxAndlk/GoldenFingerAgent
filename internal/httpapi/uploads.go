package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

const maxUploadBytes = 60 << 20

type uploadRequest struct {
	Name string `json:"name"`
	MIME string `json:"mime"`
	Kind string `json:"kind"`
	Data string `json:"data"` // base64，允许带 data URL 前缀
}

type uploadResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	URL  string `json:"url"`
}

var uploadExtensions = map[string]string{
	"image/jpeg":      ".jpg",
	"image/png":       ".png",
	"image/gif":       ".gif",
	"image/webp":      ".webp",
	"image/bmp":       ".bmp",
	"video/mp4":       ".mp4",
	"video/webm":      ".webm",
	"video/quicktime": ".mov",
}

// handleUpload 接收 base64 图片/视频并保存到 UploadDir；只供本机界面使用。
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if s.UploadDir == "" {
		writeError(w, http.StatusServiceUnavailable, "uploads not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes*4/3+(1<<20))
	var req uploadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request body")
		return
	}
	mime := strings.ToLower(strings.TrimSpace(req.MIME))
	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	if kind != "image" && kind != "video" {
		kind = ""
		if strings.HasPrefix(mime, "image/") {
			kind = "image"
		} else if strings.HasPrefix(mime, "video/") {
			kind = "video"
		}
	}
	if kind != "image" && kind != "video" {
		writeError(w, http.StatusBadRequest, "只支持图片或视频")
		return
	}
	data := req.Data
	if index := strings.Index(data, ","); index >= 0 && strings.Contains(data[:index], "base64") {
		data = data[index+1:]
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		writeError(w, http.StatusBadRequest, "附件编码无效")
		return
	}
	if len(raw) == 0 {
		writeError(w, http.StatusBadRequest, "附件为空")
		return
	}
	if len(raw) > maxUploadBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "附件不能超过 60MB")
		return
	}
	ext := strings.ToLower(filepath.Ext(req.Name))
	if len(ext) < 2 || len(ext) > 8 || !strings.HasPrefix(ext, ".") {
		ext = uploadExtensions[mime]
	}
	if ext == "" {
		ext = ".bin"
	}
	name := filepath.Base(strings.TrimSpace(req.Name))
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = "附件" + ext
	}
	if err := os.MkdirAll(s.UploadDir, 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	fileName := uuid.NewString() + ext
	target := filepath.Join(s.UploadDir, fileName)
	if err := os.WriteFile(target, raw, 0o644); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, uploadResponse{
		ID:   strings.TrimSuffix(fileName, ext),
		Name: name,
		Kind: kind,
		URL:  "/uploads/" + fileName,
	})
}

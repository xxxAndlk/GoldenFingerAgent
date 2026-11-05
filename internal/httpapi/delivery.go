package httpapi

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"goldenfinger/agent/internal/deliveryclient"
)

func (s *Server) requireDelivery(w http.ResponseWriter) bool {
	if s.Delivery == nil {
		writeError(w, http.StatusServiceUnavailable, "云端文件交付未启用")
		return false
	}
	return true
}

func (s *Server) handleDeliveryInbox(w http.ResponseWriter, r *http.Request) {
	if !s.requireDelivery(w) {
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	items, err := s.Delivery.Inbox(r.Context(), limit)
	if err != nil {
		writeDeliveryError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"deliveries": items})
}

func (s *Server) handleDeliveryStatus(w http.ResponseWriter, r *http.Request) {
	enabled := s.Delivery != nil
	writeJSON(w, 200, map[string]any{"enabled": enabled})
}

func (s *Server) handleDeliveryContent(w http.ResponseWriter, r *http.Request) {
	if !s.requireDelivery(w) {
		return
	}
	download, err := s.Delivery.Download(r.Context(), r.PathValue("id"))
	if err != nil {
		writeDeliveryError(w, err)
		return
	}
	defer download.Body.Close()
	if download.Mime != "" {
		w.Header().Set("Content-Type", download.Mime)
	}
	if download.SHA256 != "" {
		w.Header().Set("X-Content-Hash", download.SHA256)
	}
	if download.Size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(download.Size, 10))
	}
	if download.Name != "" {
		w.Header().Set("Content-Disposition", contentDisposition(download.Name))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, download.Body)
}

func (s *Server) handleDeliveryAck(w http.ResponseWriter, r *http.Request) {
	if !s.requireDelivery(w) {
		return
	}
	if err := s.Delivery.Ack(r.Context(), r.PathValue("id")); err != nil {
		writeDeliveryError(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "acked"})
}

func (s *Server) handleDeliveryDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireDelivery(w) {
		return
	}
	if err := s.Delivery.Delete(r.Context(), r.PathValue("id")); err != nil {
		writeDeliveryError(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "revoked"})
}

func writeDeliveryError(w http.ResponseWriter, err error) {
	var httpErr *deliveryclient.HTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.Status {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
			http.StatusConflict, http.StatusGone, http.StatusRequestEntityTooLarge,
			http.StatusUnsupportedMediaType:
			writeError(w, httpErr.Status, strings.TrimSpace(httpErr.Body))
			return
		}
		writeError(w, http.StatusBadGateway, httpErr.Error())
		return
	}
	writeError(w, http.StatusBadGateway, err.Error())
}

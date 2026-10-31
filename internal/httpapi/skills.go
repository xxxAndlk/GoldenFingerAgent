package httpapi

import "net/http"

// handleListSkills 返回本机技能清单（名称 + 描述），不返回正文。
func (s *Server) handleListSkills(w http.ResponseWriter, _ *http.Request) {
	type skillDTO struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	out := make([]skillDTO, 0)
	if s.Skills != nil {
		for _, item := range s.Skills.All() {
			out = append(out, skillDTO{ID: item.ID, Name: item.Name, Description: item.Description})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"skills": out})
}

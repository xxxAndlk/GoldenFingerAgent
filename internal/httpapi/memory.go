package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"goldenfinger/agent/internal/memory"
	"goldenfinger/agent/internal/store"
)

func (s *Server) handleListPersons(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	persons, err := s.Repos.Persons.ListByOwner(r.Context(), u.ID)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	// 为记忆面板包含每个人物的当前事实。
	type personWithFacts struct {
		store.Person
		Aliases []string     `json:"aliases"`
		Facts   []store.Fact `json:"facts"`
	}
	out := make([]personWithFacts, 0, len(persons))
	for _, p := range persons {
		item := personWithFacts{Person: p}
		if aliases, err := s.Repos.Persons.ListAliases(r.Context(), p.ID); err == nil {
			for _, a := range aliases {
				item.Aliases = append(item.Aliases, a.Alias)
			}
		}
		if facts, err := s.Repos.Facts.ListByPerson(r.Context(), p.ID); err == nil {
			item.Facts = facts
		}
		out = append(out, item)
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleGetPerson(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	id := r.PathValue("id")
	p, err := s.Repos.Persons.Get(r.Context(), u.ID, id)
	if err != nil {
		writeError(w, 404, "person not found")
		return
	}
	aliases, _ := s.Repos.Persons.ListAliases(r.Context(), p.ID)
	facts, _ := s.Repos.Facts.ListByPerson(r.Context(), p.ID)
	var aliasNames []string
	for _, a := range aliases {
		aliasNames = append(aliasNames, a.Alias)
	}
	writeJSON(w, 200, map[string]any{"person": p, "aliases": aliasNames, "facts": facts})
}

type updatePersonRequest struct {
	Notes string `json:"notes"`
}

func (s *Server) handleUpdatePerson(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	id := r.PathValue("id")
	var req updatePersonRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	// 走记忆服务：备注同时要写进共享长期记忆，手机与电脑才能看到同一份。
	if s.Memory != nil {
		if err := s.Memory.UpdateNotes(r.Context(), u.ID, id, req.Notes); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ok"})
		return
	}
	if err := s.Repos.Persons.UpdateNotes(r.Context(), u.ID, id, req.Notes); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	owner := u.ID
	_ = s.Repos.Audit.Append(r.Context(), &owner, "user", "person_edit", id, nil)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleForgetPerson 运行遗忘级联（记忆可遗忘）。
func (s *Server) handleForgetPerson(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	id := r.PathValue("id")
	// 走记忆服务：级联里包含共享长期记忆的删除，
	// 只删本机的话，另一端写的版本仍能被检索到。
	if s.Memory != nil {
		if err := s.Memory.ForgetPerson(r.Context(), u.ID, id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, 404, "person not found")
				return
			}
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"status": "forgotten"})
		return
	}
	p, err := s.Repos.Persons.Get(r.Context(), u.ID, id)
	if err != nil {
		writeError(w, 404, "person not found")
		return
	}
	if err := s.Repos.Persons.SoftDeleteCascade(r.Context(), u.ID, id); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	_ = s.Repos.Episodes.ClearPersonRefs(r.Context(), u.ID, p.CanonicalName)
	owner := u.ID
	_ = s.Repos.Audit.Append(r.Context(), &owner, "user", "person_forget", id, nil)
	writeJSON(w, 200, map[string]string{"status": "forgotten"})
}

// handleSaveSharedMemory 写入一条跨端共享的长期记忆。
//
// 手机与电脑只共享同一份长期记忆：协同总结、工作简报、联系人备注
// 都要从这里写进去，另一端才能从同一份记忆里检索到。
func (s *Server) handleSaveSharedMemory(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	var req struct {
		Slot       string `json:"slot"`
		Text       string `json:"text"`
		Source     string `json:"source"`
		CapturedAt string `json:"captured_at"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	req.Slot = strings.TrimSpace(req.Slot)
	if req.Slot == "" {
		writeError(w, 400, "slot 不能为空")
		return
	}
	if s.Memory == nil {
		writeError(w, 503, "记忆服务未配置")
		return
	}
	at := time.Time{}
	if req.CapturedAt != "" {
		if parsed, err := time.Parse(time.RFC3339, req.CapturedAt); err == nil {
			at = parsed
		}
	}
	if err := s.Memory.SaveShared(r.Context(), memory.SharedInput{
		OwnerUserID: u.ID,
		Slot:        req.Slot,
		Text:        req.Text,
		Source:      req.Source,
		At:          at,
	}); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) handleForgetFact(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	id := r.PathValue("id")
	// 走记忆服务：要一起做 owner 归属校验，并把云端派生记忆一并删掉。
	// 直接硬删 SQLite 的话，云端仍能召回这条事实。
	if s.Memory != nil {
		if err := s.Memory.ForgetFact(r.Context(), u.ID, id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, 404, "fact not found")
				return
			}
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"status": "forgotten"})
		return
	}
	if err := s.Repos.Facts.HardDelete(r.Context(), id); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	owner := u.ID
	_ = s.Repos.Audit.Append(r.Context(), &owner, "user", "fact_forget", id, nil)
	writeJSON(w, 200, map[string]string{"status": "forgotten"})
}

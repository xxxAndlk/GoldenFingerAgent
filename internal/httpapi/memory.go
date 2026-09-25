package httpapi

import (
	"encoding/json"
	"net/http"

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
	// Include each person's current facts for the memory panel.
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
	if err := s.Repos.Persons.UpdateNotes(r.Context(), u.ID, id, req.Notes); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	owner := u.ID
	_ = s.Repos.Audit.Append(r.Context(), &owner, "user", "person_edit", id, nil)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleForgetPerson runs the forget cascade (memory 可遗忘).
func (s *Server) handleForgetPerson(w http.ResponseWriter, r *http.Request) {
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
	if err := s.Repos.Persons.SoftDeleteCascade(r.Context(), u.ID, id); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	_ = s.Repos.Episodes.ClearPersonRefs(r.Context(), u.ID, p.CanonicalName)
	owner := u.ID
	_ = s.Repos.Audit.Append(r.Context(), &owner, "user", "person_forget", id, nil)
	writeJSON(w, 200, map[string]string{"status": "forgotten"})
}

func (s *Server) handleForgetFact(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	id := r.PathValue("id")
	if err := s.Repos.Facts.HardDelete(r.Context(), id); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	owner := u.ID
	_ = s.Repos.Audit.Append(r.Context(), &owner, "user", "fact_forget", id, nil)
	writeJSON(w, 200, map[string]string{"status": "forgotten"})
}

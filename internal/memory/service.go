// Package memory: person/fact governance (write-path conflict resolution,
// forget cascade, traceability) and retrieval ranking.
package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"goldenfinger/agent/internal/llm"
	"goldenfinger/agent/internal/nlu"
	"goldenfinger/agent/internal/store"
)

// ErrNeedClarify signals the caller must ask the user before writing.
var ErrNeedClarify = errors.New("memory: needs clarification")

// ConflictPolicy controls fact supersession (doc F1: 较新且已确认 wins).
type Outcome string

const (
	OutcomeWritten    Outcome = "written"
	OutcomeSuperseded Outcome = "superseded" // replaced an older fact
	OutcomeInferred   Outcome = "inferred"   // stored as inferred, user should confirm
	OutcomeRejected   Outcome = "rejected"   // evaluative / child-blocked
	OutcomeConflict   Outcome = "conflict"   // equal-status conflict → clarify
)

// Service is the memory business logic layer.
type Service struct {
	persons         *store.PersonRepo
	facts           *store.FactRepo
	eps             *store.EpisodeRepo
	audit           *store.AuditRepo
	embed           llm.Embedder
	th              nlu.Thresholds
	recencyHalfLife time.Duration
	now             func() time.Time
}

func NewService(persons *store.PersonRepo, facts *store.FactRepo, eps *store.EpisodeRepo,
	audit *store.AuditRepo, embed llm.Embedder, th nlu.Thresholds, recencyHalfLife time.Duration, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	if recencyHalfLife <= 0 {
		recencyHalfLife = 30 * 24 * time.Hour
	}
	return &Service{persons: persons, facts: facts, eps: eps, audit: audit, embed: embed,
		th: th, recencyHalfLife: recencyHalfLife, now: now}
}

func (s *Service) recencyHalfLifeValue() time.Duration { return s.recencyHalfLife }

// Resolution is the result of resolving a person name against memory.
type Resolution struct {
	Person     *store.Person
	Candidates []store.Person // when Ambiguous
	Ambiguous  bool
	NotFound   bool
}

// ResolvePerson maps a name to exactly one person, or reports ambiguity.
func (s *Service) ResolvePerson(ctx context.Context, ownerID, name string) (*Resolution, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return &Resolution{NotFound: true}, nil
	}
	hits, err := s.persons.FindByAlias(ctx, ownerID, name)
	if err != nil {
		return nil, err
	}
	switch len(hits) {
	case 0:
		return &Resolution{NotFound: true}, nil
	case 1:
		return &Resolution{Person: &hits[0]}, nil
	default:
		return &Resolution{Candidates: hits, Ambiguous: true}, nil
	}
}

// EnsurePerson creates a person page (plus alias) when the name is new.
func (s *Service) EnsurePerson(ctx context.Context, ownerID, name, relation string) (*store.Person, error) {
	res, err := s.ResolvePerson(ctx, ownerID, name)
	if err != nil {
		return nil, err
	}
	if res.Person != nil {
		return res.Person, nil
	}
	p := &store.Person{OwnerUserID: ownerID, CanonicalName: name}
	if relation != "" {
		p.Notes = "关系: " + relation
	}
	if err := s.persons.Create(ctx, p); err != nil {
		return nil, err
	}
	if err := s.persons.AddAlias(ctx, p.ID, name); err != nil {
		return nil, err
	}
	owner := ownerID
	_ = s.audit.Append(ctx, &owner, "agent", "person_create", p.ID, nil)
	return p, nil
}

// SaveFactInput is a normalized fact write request.
type SaveFactInput struct {
	OwnerUserID string
	Actor       string // "user" | "agent"
	PersonID    string
	PersonName  string
	FactType    string
	ValueText   string
	Confidence  float64
	SourceMsgID *string
	IsChildUser bool
}

// SaveFact applies the full governance pipeline:
// person resolution → child fact-type gate → evaluative gate → conflict
// resolution (confirmed-newer supersedes) → embed → store.
func (s *Service) SaveFact(ctx context.Context, in SaveFactInput) (Outcome, *store.Fact, error) {
	// Evaluative labels are NEVER auto-written (P5).
	if nlu.IsEvaluative(in.FactType, in.ValueText) {
		owner := in.OwnerUserID
		_ = s.audit.Append(ctx, &owner, in.Actor, "fact_reject_evaluative", in.PersonName, nil)
		log.Printf("[memory] fact rejected (evaluative): person=%q type=%s value=%q", in.PersonName, in.FactType, in.ValueText)
		return OutcomeRejected, nil, nil
	}
	// Children: fact types only.
	if in.IsChildUser && !nlu.ChildFactAllowed(in.FactType) {
		owner := in.OwnerUserID
		_ = s.audit.Append(ctx, &owner, in.Actor, "fact_reject_child_blocked", in.FactType, nil)
		log.Printf("[memory] fact rejected (child-blocked): person=%q type=%s", in.PersonName, in.FactType)
		return OutcomeRejected, nil, nil
	}

	personID := in.PersonID
	if personID == "" {
		p, err := s.EnsurePerson(ctx, in.OwnerUserID, in.PersonName, "")
		if err != nil {
			return "", nil, err
		}
		personID = p.ID
	}

	status := store.FactConfirmed
	if in.Confidence < s.th.FactConfirmed {
		status = store.FactInferred
	}

	// Conflict resolution on (person_id, fact_type).
	conflicts, err := s.facts.FindConflict(ctx, personID, in.FactType, in.ValueText)
	if err != nil {
		return "", nil, err
	}
	now := s.now()
	for _, c := range conflicts {
		switch {
		case status == store.FactConfirmed:
			// Newer confirmed supersedes: close the old window (kept for traceability).
			if err := s.facts.SetValidity(ctx, c.ID, &now); err != nil {
				return "", nil, err
			}
		case c.Status == store.FactConfirmed:
			// Inferred never supersedes confirmed → treat as conflict for clarify.
			log.Printf("[memory] fact conflict: person=%s type=%s new=%q vs existing=%q(confirmed) → clarify", in.PersonID, in.FactType, in.ValueText, c.ValueText)
			return OutcomeConflict, nil, nil
		default:
			// Two inferred values disagree → clarify.
			log.Printf("[memory] fact conflict: person=%s type=%s new=%q vs existing=%q(inferred) → clarify", in.PersonID, in.FactType, in.ValueText, c.ValueText)
			return OutcomeConflict, nil, nil
		}
	}

	// Embed for retrieval (failure is non-fatal — fact still stored).
	var vec []float32
	if s.embed != nil {
		vecs, err := s.embed.Embed(ctx, []string{in.FactType + " " + in.ValueText})
		if err == nil && len(vecs) == 1 {
			vec = vecs[0]
		} else if err != nil {
			log.Printf("[memory] embed failed (fact stored without vector): %v", err)
		}
	}

	f := &store.Fact{
		PersonID:    personID,
		FactType:    in.FactType,
		ValueText:   in.ValueText,
		Confidence:  in.Confidence,
		Status:      status,
		SourceMsgID: in.SourceMsgID,
		ValidFrom:   now,
		Embedding:   vec,
	}
	if err := s.facts.Insert(ctx, f); err != nil {
		return "", nil, err
	}
	owner := in.OwnerUserID
	detail, _ := json.Marshal(map[string]any{"fact_id": f.ID, "status": status})
	_ = s.audit.Append(ctx, &owner, in.Actor, "fact_write", f.ID, detail)

	outcome := OutcomeWritten
	if status == store.FactInferred {
		outcome = OutcomeInferred
	} else if len(conflicts) > 0 {
		outcome = OutcomeSuperseded
	}
	log.Printf("[memory] fact %s: person=%s type=%s value=%q conf=%.2f status=%s", outcome, in.PersonName, in.FactType, in.ValueText, in.Confidence, status)
	return outcome, f, nil
}

// ForgetPerson runs the forget cascade: person soft-deleted, aliases and facts
// hard-deleted (vectors gone → 删除后不可检索), episode references scrubbed.
func (s *Service) ForgetPerson(ctx context.Context, ownerID, personID string) error {
	p, err := s.persons.Get(ctx, ownerID, personID)
	if err != nil {
		return err
	}
	if err := s.persons.SoftDeleteCascade(ctx, ownerID, personID); err != nil {
		return err
	}
	_ = s.eps.ClearPersonRefs(ctx, ownerID, p.CanonicalName)
	log.Printf("[memory] forget person: %q (%s) — cascade: aliases+facts hard-deleted, episode refs scrubbed", p.CanonicalName, personID)
	owner := ownerID
	// Audit keeps only the deletion action itself, not the content (PIPL).
	return s.audit.Append(ctx, &owner, "user", "person_forget", personID, nil)
}

// ForgetFact hard-deletes one fact.
func (s *Service) ForgetFact(ctx context.Context, ownerID, factID string) error {
	f, err := s.facts.Get(ctx, factID)
	if err != nil {
		return err
	}
	// Ownership check via person.
	p, err := s.persons.Get(ctx, ownerID, f.PersonID)
	if err != nil {
		return err
	}
	if p == nil {
		return store.ErrNotFound
	}
	if err := s.facts.HardDelete(ctx, factID); err != nil {
		return err
	}
	log.Printf("[memory] forget fact: %s (%q)", factID, f.ValueText)
	owner := ownerID
	return s.audit.Append(ctx, &owner, "user", "fact_forget", factID, nil)
}

// PersonView is a wiki-style person page (facts grouped).
type PersonView struct {
	Person  store.Person `json:"person"`
	Aliases []string     `json:"aliases"`
	Facts   []store.Fact `json:"facts"`
}

// GetPersonView assembles one person page.
func (s *Service) GetPersonView(ctx context.Context, ownerID, personID string) (*PersonView, error) {
	p, err := s.persons.Get(ctx, ownerID, personID)
	if err != nil {
		return nil, err
	}
	aliases, err := s.persons.ListAliases(ctx, personID)
	if err != nil {
		return nil, err
	}
	facts, err := s.facts.ListByPerson(ctx, personID)
	if err != nil {
		return nil, err
	}
	view := &PersonView{Person: *p, Facts: facts}
	for _, a := range aliases {
		view.Aliases = append(view.Aliases, a.Alias)
	}
	return view, nil
}

// ListPersons returns all person pages for the user.
func (s *Service) ListPersons(ctx context.Context, ownerID string) ([]store.Person, error) {
	return s.persons.ListByOwner(ctx, ownerID)
}

// UpdateNotes edits person notes (memory 可改).
func (s *Service) UpdateNotes(ctx context.Context, ownerID, personID, notes string) error {
	if err := s.persons.UpdateNotes(ctx, ownerID, personID, notes); err != nil {
		return err
	}
	owner := ownerID
	return s.audit.Append(ctx, &owner, "user", "person_edit", personID, nil)
}

// SaveNote persists a voice note as an episode (retrievable) with optional task link.
func (s *Service) SaveNote(ctx context.Context, ownerID, text, rawRef string, sourceMsgID *string) (*store.Episode, error) {
	var vec []float32
	if s.embed != nil {
		if vecs, err := s.embed.Embed(ctx, []string{text}); err == nil && len(vecs) == 1 {
			vec = vecs[0]
		} else if err != nil {
			log.Printf("[memory] note embed failed (stored without vector): %v", err)
		}
	}
	e := &store.Episode{
		OwnerUserID: ownerID,
		Summary:     text,
		RawRef:      rawRef,
		Embedding:   vec,
	}
	if err := s.eps.Create(ctx, e); err != nil {
		return nil, err
	}
	owner := ownerID
	_ = s.audit.Append(ctx, &owner, "agent", "note_save", e.ID, nil)
	return e, nil
}

// FactLine renders a fact as one prompt line.
func FactLine(f *store.Fact) string {
	return fmt.Sprintf("%s: %s", f.FactType, f.ValueText)
}

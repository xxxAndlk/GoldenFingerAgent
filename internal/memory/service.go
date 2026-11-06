// Package memory：人物/事实治理（写入路径冲突消解、
// 遗忘级联、可追溯性）与检索排序。
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

// ErrNeedClarify 表示调用方在写入前必须先询问用户。
var ErrNeedClarify = errors.New("memory: needs clarification")

// ConflictPolicy 控制事实的更替（文档 F1：较新且已确认 wins）。
type Outcome string

const (
	OutcomeWritten    Outcome = "written"
	OutcomeSuperseded Outcome = "superseded" // 替换了一条更旧的事实
	OutcomeInferred   Outcome = "inferred"   // 以推断状态存储，用户应确认
	OutcomeRejected   Outcome = "rejected"   // 评价性 / 儿童受限
	OutcomeConflict   Outcome = "conflict"   // 同级冲突 → 澄清
)

// Service 是记忆业务逻辑层。
type Service struct {
	persons         *store.PersonRepo
	facts           *store.FactRepo
	eps             *store.EpisodeRepo
	audit           *store.AuditRepo
	embed           llm.Embedder
	longTerm        LongTermMemory
	slots           *store.SharedMemorySlotRepo
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

// Resolution 是对人物名与记忆解析的结果。
type Resolution struct {
	Person     *store.Person
	Candidates []store.Person // 歧义时的候选项
	Ambiguous  bool
	NotFound   bool
}

// ResolvePerson 把名字映射到唯一一个人物，或报告歧义。
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

// EnsurePerson 在名字是新人时创建人物页（并加别名）。
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
	// 初始备注（关系）同样进共享长期记忆：手机记录的联系人信息
	// 电脑要能检索到。
	syncErr := s.mirrorPersonNotes(ctx, ownerID, p, p.Notes)
	owner := ownerID
	_ = s.audit.Append(ctx, &owner, "agent", "person_create", p.ID, nil)
	if syncErr != nil {
		return p, fmt.Errorf("联系人已在本机保存，但共享记忆同步任务保存失败: %w", syncErr)
	}
	return p, nil
}

// SaveFactInput 是规范化的事实写入请求。
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

// SaveFact 应用完整治理管线：
// 人物解析 → 儿童事实类型门 → 评价性门 → 冲突
// 消解（已确认且更新者更替）→ 嵌入 → 存储。
func (s *Service) SaveFact(ctx context.Context, in SaveFactInput) (Outcome, *store.Fact, error) {
	// 评价性标签绝不自动写入（P5）。
	if nlu.IsEvaluative(in.FactType, in.ValueText) {
		owner := in.OwnerUserID
		_ = s.audit.Append(ctx, &owner, in.Actor, "fact_reject_evaluative", in.PersonName, nil)
		log.Printf("[memory] fact rejected (evaluative): person=%q type=%s value=%q", in.PersonName, in.FactType, in.ValueText)
		return OutcomeRejected, nil, nil
	}
	// 儿童：仅限事实类型。
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

	// (person_id, fact_type) 上的冲突消解。
	conflicts, err := s.facts.FindConflict(ctx, personID, in.FactType, in.ValueText)
	if err != nil {
		return "", nil, err
	}
	now := s.now()
	for _, c := range conflicts {
		switch {
		case status == store.FactConfirmed:
			// 较新且已确认者更替：关闭旧窗口（保留用于追溯）。
			if err := s.facts.SetValidity(ctx, c.ID, &now); err != nil {
				return "", nil, err
			}
		case c.Status == store.FactConfirmed:
			// 推断值永不更替已确认值 → 视为冲突走澄清。
			log.Printf("[memory] fact conflict: person=%s type=%s new=%q vs existing=%q(confirmed) → clarify", in.PersonID, in.FactType, in.ValueText, c.ValueText)
			return OutcomeConflict, nil, nil
		default:
			// 两个推断值不一致 → 澄清。
			log.Printf("[memory] fact conflict: person=%s type=%s new=%q vs existing=%q(inferred) → clarify", in.PersonID, in.FactType, in.ValueText, c.ValueText)
			return OutcomeConflict, nil, nil
		}
	}

	// 为检索做嵌入（失败不致命——事实仍会存储）。
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
	syncErr := s.mirrorEpisode(ctx, factSourceID(f.ID), in.OwnerUserID,
		fmt.Sprintf("%s 的 %s：%s", in.PersonName, in.FactType, in.ValueText), "person_fact", RoleOwner, now)
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
	if syncErr != nil {
		return outcome, f, fmt.Errorf("联系人事实已在本机保存，但共享记忆同步任务保存失败: %w", syncErr)
	}
	return outcome, f, nil
}

// ForgetPerson 执行遗忘级联：人物软删除，别名与事实
// 硬删除（向量消失 → 删除后不可检索），片段引用清除。
func (s *Service) ForgetPerson(ctx context.Context, ownerID, personID string) error {
	p, err := s.persons.Get(ctx, ownerID, personID)
	if err != nil {
		return err
	}
	// 事实要先取出来：级联删除会把事实行硬删掉，
	// 之后再列就拿不到它们的长期记忆引用了。
	facts, err := s.facts.ListByPerson(ctx, personID)
	if err != nil {
		return err
	}
	if err := s.persons.SoftDeleteCascade(ctx, ownerID, personID); err != nil {
		return err
	}
	_ = s.eps.ClearPersonRefs(ctx, ownerID, p.CanonicalName)
	var syncErrors []error
	// 共享长期记忆里的备注也要删掉：只删本机那份的话，
	// 另一端写的版本仍能被检索到，「忘了这个人」就只是半途。
	if err := s.ForgetShared(ctx, ownerID, PersonNotesSlot(p.CanonicalName)); err != nil {
		log.Printf("[memory] 遗忘共享备注失败 (%s): %v", p.CanonicalName, err)
		syncErrors = append(syncErrors, fmt.Errorf("备注: %w", err))
	}
	// 长期记忆里由该人物派生的事实也要真删，否则“忘了它”只是半途。
	// 按来源引用删：事实的长期记忆片段来源是 fact:<id>，
	// 用本机事实 ID 删不到云端派生出来的那条。
	for _, f := range facts {
		if err := s.forgetLongTerm(ctx, ForgetRequest{OwnerID: ownerID, SourceID: factSourceID(f.ID)}); err != nil {
			syncErrors = append(syncErrors, fmt.Errorf("事实 %s: %w", f.ID, err))
		}
	}
	log.Printf("[memory] forget person: %q (%s) — cascade: aliases+facts hard-deleted, episode refs scrubbed", p.CanonicalName, personID)
	owner := ownerID
	// 审计只保留删除动作本身，不保留内容（PIPL）。
	auditErr := s.audit.Append(ctx, &owner, "user", "person_forget", personID, nil)
	if len(syncErrors) > 0 {
		return fmt.Errorf("联系人已在本机遗忘，但共享记忆删除任务保存失败: %w", errors.Join(syncErrors...))
	}
	return auditErr
}

// ForgetFact 硬删除一条事实。
func (s *Service) ForgetFact(ctx context.Context, ownerID, factID string) error {
	f, err := s.facts.Get(ctx, factID)
	if err != nil {
		return err
	}
	// 通过人物做归属校验。
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
	// 云端派生记忆按来源引用删除：本机事实 ID 与云端的派生条目 ID 不同，
	// 按 ID 删会删不到。
	syncErr := s.forgetLongTerm(ctx, ForgetRequest{OwnerID: ownerID, SourceID: factSourceID(factID)})
	log.Printf("[memory] forget fact: %s (%q)", factID, f.ValueText)
	owner := ownerID
	auditErr := s.audit.Append(ctx, &owner, "user", "fact_forget", factID, nil)
	if syncErr != nil {
		return fmt.Errorf("事实已在本机删除，但共享记忆删除任务保存失败: %w", syncErr)
	}
	return auditErr
}

// PersonView 是 wiki 风格的人物页（事实分组展示）。
type PersonView struct {
	Person  store.Person `json:"person"`
	Aliases []string     `json:"aliases"`
	Facts   []store.Fact `json:"facts"`
}

// GetPersonView 组装一个人物页。
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

// ListPersons 返回该用户的全部人物页。
func (s *Service) ListPersons(ctx context.Context, ownerID string) ([]store.Person, error) {
	return s.persons.ListByOwner(ctx, ownerID)
}

// UpdateNotes 编辑人物备注（memory 可改）。
//
// 备注同时写进共享长期记忆：手机与电脑只共享同一份长期记忆，
// 联系人备注是必须共享的那一类内容。
func (s *Service) UpdateNotes(ctx context.Context, ownerID, personID, notes string) error {
	if err := s.persons.UpdateNotes(ctx, ownerID, personID, notes); err != nil {
		return err
	}
	var syncErr error
	person, err := s.persons.Get(ctx, ownerID, personID)
	switch {
	case err != nil:
		syncErr = err
	case person != nil:
		syncErr = s.mirrorPersonNotes(ctx, ownerID, person, notes)
	}
	owner := ownerID
	auditErr := s.audit.Append(ctx, &owner, "user", "person_edit", personID, nil)
	if syncErr != nil {
		return fmt.Errorf("联系人备注已在本机保存，但共享记忆同步任务保存失败: %w", syncErr)
	}
	return auditErr
}

// mirrorPersonNotes 把联系人备注写入（或清空）共享长期记忆。
func (s *Service) mirrorPersonNotes(ctx context.Context, ownerID string, person *store.Person, notes string) error {
	if person == nil || s.longTerm == nil {
		return nil
	}
	slot := PersonNotesSlot(person.CanonicalName)
	text := strings.TrimSpace(notes)
	if text == "" {
		// 备注被清空：共享记忆里的那份也要删掉。
		if err := s.ForgetShared(ctx, ownerID, slot); err != nil {
			log.Printf("[memory] 清空共享备注失败 (%s): %v", slot, err)
			return err
		}
		return nil
	}
	if err := s.SaveShared(ctx, SharedInput{
		OwnerUserID: ownerID,
		Slot:        slot,
		Text:        fmt.Sprintf("%s 的备注：%s", person.CanonicalName, text),
		Source:      SharedSlotPersonNotes,
		At:          s.now(),
	}); err != nil {
		log.Printf("[memory] 共享备注写入失败 (%s): %v", slot, err)
		return err
	}
	return nil
}

// SaveNote 把语音笔记持久化为一个片段（可检索），可带任务链接。
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
	s.mirrorEpisode(ctx, "episode:"+e.ID, ownerID, e.Summary, "note", RoleOwner, e.CreatedAt)
	owner := ownerID
	_ = s.audit.Append(ctx, &owner, "agent", "note_save", e.ID, nil)
	return e, nil
}

// SaveSystemEvent 写入系统生成的跨端事件摘要（交付完成等），
// 以 system 角色入库，避免被抽取成机主本人的意愿。
func (s *Service) SaveSystemEvent(ctx context.Context, ownerID, text, rawRef string) (*store.Episode, error) {
	e := &store.Episode{OwnerUserID: ownerID, Summary: text, RawRef: rawRef}
	if err := s.eps.Create(ctx, e); err != nil {
		return nil, err
	}
	s.mirrorEpisode(ctx, rawRef, ownerID, text, "system", RoleSystem, e.CreatedAt)
	return e, nil
}

// SaveWorkerResult 先把 Worker 结论同步写成片段（立即可检索），
// 再异步补向量并镜像长期记忆；嵌入失败不影响结论已经进入统一记忆。
func (s *Service) SaveWorkerResult(ctx context.Context, ownerID, text, rawRef string) (*store.Episode, error) {
	e := &store.Episode{OwnerUserID: ownerID, Summary: text, RawRef: rawRef}
	if err := s.eps.Create(ctx, e); err != nil {
		return nil, err
	}
	s.mirrorEpisode(ctx, rawRef, ownerID, text, "worker", RoleOwner, e.CreatedAt)
	if s.embed != nil {
		go func(id, text string) {
			embedCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			vecs, err := s.embed.Embed(embedCtx, []string{text})
			if err != nil || len(vecs) != 1 {
				return
			}
			if err := s.eps.SetEmbedding(context.Background(), id, vecs[0]); err != nil {
				log.Printf("[memory] worker result embedding update failed: %v", err)
			}
		}(e.ID, text)
	}
	owner := ownerID
	_ = s.audit.Append(ctx, &owner, "agent", "worker_result", e.ID, nil)
	return e, nil
}

// factSourceID 是事实在长期记忆里的来源 ID。
//
// 事实的长期记忆片段用它做来源，遗忘时也用它删除：
// 云端派生出来的条目用的是来源引用，不是本机事实 ID。
func factSourceID(factID string) string {
	return "fact:" + factID
}

// FactLine 把一条事实渲染为一行提示文本。
func FactLine(f *store.Fact) string {
	return fmt.Sprintf("%s: %s", f.FactType, f.ValueText)
}

// forgetLongTerm 把遗忘请求同步到可替换长期记忆后端。
// 本机删除先生效时，由调用方把后端错误包装成明确的同步失败。
func (s *Service) forgetLongTerm(ctx context.Context, request ForgetRequest) error {
	if s.longTerm == nil {
		return nil
	}
	if err := s.longTerm.Forget(ctx, request); err != nil {
		log.Printf("[memory] long-term forget failed (%+v): %v", request, err)
		return err
	}
	return nil
}

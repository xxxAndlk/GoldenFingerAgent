package memory

import (
	"context"
	"testing"
	"time"
)

type providerStub struct {
	appended Episode
	facts    []MemoryFact
}

func (p *providerStub) Append(_ context.Context, episode Episode) error {
	p.appended = episode
	return nil
}
func (p *providerStub) Search(context.Context, MemoryQuery) ([]MemoryFact, error) {
	return p.facts, nil
}
func (p *providerStub) Forget(context.Context, ForgetRequest) error { return ErrUnsupported }
func (p *providerStub) Health(context.Context) (MemoryHealth, error) {
	return MemoryHealth{Ready: true, Backend: "stub"}, nil
}

func TestServiceDependsOnProviderNeutralContract(t *testing.T) {
	_, svc := testRepos(t)
	provider := &providerStub{facts: []MemoryFact{{
		ID: "opaque-id", Text: "luokun 正在设计通用采集接口",
		SourceRefs: []string{"episode-local-1"},
	}}}
	svc.SetLongTermMemory(provider)
	at := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	svc.MirrorChat(context.Background(), "owner-1", "message-1", "正在设计通用采集接口", at)
	if provider.appended.SourceID != "chat:message-1" || !provider.appended.ReferenceTime.Equal(at) {
		t.Fatalf("provider-neutral append mismatch: %+v", provider.appended)
	}
	results, err := svc.Search(context.Background(), "owner-1", "采集接口", 8)
	if err != nil || len(results) != 1 {
		t.Fatalf("provider-neutral search failed: %+v, %v", results, err)
	}
	if results[0].ID != "ltm:opaque-id" || results[0].RawRef != "episode-local-1" {
		t.Fatalf("internal identity or source reference leaked: %+v", results[0])
	}
}

var _ LongTermMemory = (*providerStub)(nil)

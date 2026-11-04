package superagent

import (
	"encoding/json"
	"testing"

	"goldenfinger/agent/internal/store"
)

func TestWeChatContactTriggerLosesOwnerAuthority(t *testing.T) {
	contact, _ := json.Marshal(map[string]string{"actor_role": "contact"})
	owner, _ := json.Marshal(map[string]string{"actor_role": "owner"})
	if !isWeChatContactTrigger(&store.Trigger{Channel: store.ChannelWeChatDesktop, Payload: contact}) {
		t.Fatal("contact message must be blocked from task/worker escalation")
	}
	if isWeChatContactTrigger(&store.Trigger{Channel: store.ChannelWeChatDesktop, Payload: owner}) {
		t.Fatal("owner message should not be blocked by the contact gate")
	}
	if isWeChatContactTrigger(&store.Trigger{Channel: store.ChannelEmail, Payload: contact}) {
		t.Fatal("non-WeChat channel must not be affected")
	}
}

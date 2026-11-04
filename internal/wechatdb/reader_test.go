package wechatdb

import (
	"strings"
	"testing"
)

func TestAccountKeyMatchesConnectorIdentity(t *testing.T) {
	if got := accountKey("wxid_fdg4i5gm1gxe22"); got != "c6593e17d1bf6e85" {
		t.Fatalf("accountKey=%s", got)
	}
}

func TestMessageIdentityIsDeterministicAndKeepsShard(t *testing.T) {
	r := &Reader{AccountKey: "c6593e17d1bf6e85", WXID: "wxid_me"}
	first := r.messageFromRow("wxid_other", "张三", "message__message_0.db", 123, 1,
		"wxid_other", 1793351290, []byte("你好"), 1793351290000)
	second := r.messageFromRow("wxid_other", "张三", "message__message_0.db", 123, 1,
		"wxid_other", 1793351290, []byte("你好"), 1793351290000)
	if first.EventID != second.EventID {
		t.Fatalf("event id not deterministic: %s / %s", first.EventID, second.EventID)
	}
	if !strings.Contains(first.MessageID, ":message__message_0.db:123") {
		t.Fatalf("message id lost shard: %s", first.MessageID)
	}
	if first.SourceID != "wechat-db:c6593e17d1bf6e85:wxid_other" {
		t.Fatalf("source id=%s", first.SourceID)
	}
	if first.Direction != "other" || first.ActorRole != "contact" || first.Text != "你好" {
		t.Fatalf("message=%+v", first)
	}
}

func TestMessageTypeAndSelfDirection(t *testing.T) {
	r := &Reader{AccountKey: "acct", WXID: "wxid_me"}
	message := r.messageFromRow("wxid_other", "张三", "message__message_0.db", 2, 1,
		"wxid_me", 1793351290, []byte("我发的"), 0)
	if message.Direction != "self" || message.ActorRole != "owner" {
		t.Fatalf("message=%+v", message)
	}
	if got := messageType(10000); got != "system" {
		t.Fatalf("system type=%s", got)
	}
	if got := decodeContent(nil, "image"); got != "[图片]" {
		t.Fatalf("image placeholder=%s", got)
	}
}

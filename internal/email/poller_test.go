package email

import (
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap"
)

type testLiteral struct {
	*strings.Reader
	size int
}

func (l testLiteral) Len() int { return l.size }

func TestParseMessageExtractsThreadAndBody(t *testing.T) {
	raw := "From: Client <client@example.com>\r\n" +
		"To: Me <me@example.com>\r\n" +
		"Subject: Re: Q4 =?UTF-8?B?5oql5Lu3?=\r\n" +
		"Message-Id: <msg-2@example.com>\r\n" +
		"References: <root@example.com>\r\n" +
		"Date: Sat, 01 Nov 2026 10:00:00 +0800\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n\r\n" +
		"=E8=AF=B7=E5=9C=A8=E5=91=A8=E4=BA=94=E5=89=8D=E6=8F=90=E4=BA=A4=E6=8A=A5=E4=BB=B7=E5=8D=95\r\n"
	section := &imap.BodySectionName{Peek: true}
	msg := &imap.Message{
		Uid: 7, InternalDate: time.Now(),
		Body: map[*imap.BodySectionName]imap.Literal{
			{}: testLiteral{Reader: strings.NewReader(raw), size: len(raw)},
		},
	}
	parsed, err := parseMessage(Config{Username: "me@example.com", Host: "imap.example.com", Mailbox: "INBOX"}, "INBOX", msg, section)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.MessageID != "msg-2@example.com" || parsed.ThreadKey != "root@example.com" {
		t.Fatalf("ids=%+v", parsed)
	}
	if !strings.Contains(parsed.Subject, "报价") || !strings.Contains(parsed.Text, "周五") {
		t.Fatalf("subject=%q text=%q", parsed.Subject, parsed.Text)
	}
	if parsed.From == "" || len(parsed.To) != 1 {
		t.Fatalf("addresses from=%q to=%v", parsed.From, parsed.To)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestMessageMatchesSenderSubjectAndDateFilters(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.Local)
	cfg := Config{Senders: []string{"client@"}, SubjectFilters: []string{"报价"}, SinceDays: 7}
	message := Message{
		From: "Client <client@example.com>", Subject: "Q4 报价",
		ReceivedAt: now.AddDate(0, 0, -1),
	}
	if !messageMatches(cfg, message, now) {
		t.Fatal("matching message rejected")
	}
	message.From = "other@example.com"
	if messageMatches(cfg, message, now) {
		t.Fatal("sender filter not applied")
	}
	message.From = "client@example.com"
	message.Subject = "会议邀请"
	if messageMatches(cfg, message, now) {
		t.Fatal("subject filter not applied")
	}
	message.Subject = "Q4 报价"
	message.ReceivedAt = now.AddDate(0, 0, -30)
	if messageMatches(cfg, message, now) {
		t.Fatal("date filter not applied")
	}
}

func TestMailboxListDefaultsAndDeduplicates(t *testing.T) {
	if got := (Config{}).MailboxList(); len(got) != 1 || got[0] != "INBOX" {
		t.Fatalf("default=%v", got)
	}
	got := (Config{Mailbox: "INBOX", Mailboxes: []string{"INBOX", "项目/客户", " "}}).MailboxList()
	if len(got) != 2 || !contains(got, "INBOX") || !contains(got, "项目/客户") {
		t.Fatalf("mailboxes=%v", got)
	}
}

func TestSubjectThreadKeyNormalizesReplyPrefixes(t *testing.T) {
	a := subjectThreadKey("Re: 项目方案")
	b := subjectThreadKey("回复：项目方案")
	if a != b {
		t.Fatalf("thread keys differ: %s vs %s", a, b)
	}
}

func TestHTMLToText(t *testing.T) {
	text := htmlToText(`<html><body><p>Hello&nbsp;世界</p><script>x()</script><p>第二段</p></body></html>`)
	if !strings.Contains(text, "Hello") || !strings.Contains(text, "第二段") {
		t.Fatalf("text=%q", text)
	}
	if strings.Contains(text, "x()") {
		t.Fatalf("script leaked: %q", text)
	}
}

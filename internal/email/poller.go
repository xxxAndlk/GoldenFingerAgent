// Package email 提供可选的 IMAP 收件采集器。
// 只读取邮件正文并交给 Super-agent 的渠道入口；不发送邮件、不修改已读状态。
package email

import (
	"context"
	"crypto/sha1"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"html"
	"io"
	"log"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	gomail "github.com/emersion/go-message/mail"
)

type Config struct {
	Enabled      bool
	Host         string
	Port         int
	Username     string
	Password     string
	Mailbox      string
	TLS          bool
	InitialFetch int
	PollInterval time.Duration
}

func (c Config) AccountKey() string {
	return c.Username + "|" + c.Host + "|" + c.Mailbox
}

type Message struct {
	Account     string
	MessageID   string
	ThreadKey   string
	From        string
	To          []string
	Cc          []string
	Subject     string
	Text        string
	Snippet     string
	ReceivedAt  time.Time
	Attachments []string
	UID         uint32
}

type CursorStore interface {
	Get(ctx context.Context, account string) (uint32, error)
	Set(ctx context.Context, account string, uid uint32) error
}

type Poller struct {
	Config    func() Config
	Cursors   CursorStore
	OnMessage func(ctx context.Context, message Message) error
	Now       func() time.Time
}

func NewPoller(config func() Config, cursors CursorStore, onMessage func(context.Context, Message) error) *Poller {
	return &Poller{Config: config, Cursors: cursors, OnMessage: onMessage, Now: time.Now}
}

func (p *Poller) Run(ctx context.Context) {
	for {
		cfg := p.Config()
		wait := cfg.PollInterval
		if wait < 30*time.Second {
			wait = 60 * time.Second
		}
		if cfg.Enabled && strings.TrimSpace(cfg.Host) != "" && cfg.Username != "" {
			if err := p.PollOnce(ctx, cfg); err != nil && ctx.Err() == nil {
				log.Printf("[email] poll failed: %v", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (p *Poller) PollOnce(ctx context.Context, cfg Config) error {
	if p.Cursors == nil || p.OnMessage == nil {
		return fmt.Errorf("email poller not configured")
	}
	port := cfg.Port
	if port == 0 {
		port = 993
	}
	mailbox := strings.TrimSpace(cfg.Mailbox)
	if mailbox == "" {
		mailbox = "INBOX"
	}
	addr := net.JoinHostPort(strings.TrimSpace(cfg.Host), strconv.Itoa(port))
	var c *client.Client
	var err error
	if cfg.TLS || port == 993 {
		c, err = client.DialTLS(addr, &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12})
	} else {
		c, err = client.Dial(addr)
	}
	if err != nil {
		return err
	}
	c.Timeout = 30 * time.Second
	defer c.Logout()
	if err := c.Login(cfg.Username, cfg.Password); err != nil {
		return fmt.Errorf("imap login failed")
	}
	mbox, err := c.Select(mailbox, true)
	if err != nil {
		return err
	}
	if mbox.Messages == 0 {
		return nil
	}
	uids, err := c.UidSearch(&imap.SearchCriteria{})
	if err != nil {
		return err
	}
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
	cursor, err := p.Cursors.Get(ctx, cfg.AccountKey())
	if err != nil {
		return err
	}
	if cursor == 0 {
		initial := cfg.InitialFetch
		if initial <= 0 {
			initial = 20
		}
		if len(uids) > initial {
			uids = uids[len(uids)-initial:]
		}
	} else {
		next := uids[:0]
		for _, uid := range uids {
			if uid > cursor {
				next = append(next, uid)
			}
		}
		uids = next
	}
	if len(uids) == 0 {
		return nil
	}
	if len(uids) > 50 {
		uids = uids[:50]
	}
	seqset := new(imap.SeqSet)
	for _, uid := range uids {
		seqset.AddNum(uid)
	}
	section := &imap.BodySectionName{Peek: true}
	items := []imap.FetchItem{imap.FetchUid, section.FetchItem()}
	messages := make(chan *imap.Message, 10)
	done := make(chan error, 1)
	go func() { done <- c.UidFetch(seqset, items, messages) }()

	order := make(map[uint32]int, len(uids))
	for i, uid := range uids {
		order[uid] = i
	}
	fetched := make([]*imap.Message, 0, len(uids))
	for msg := range messages {
		if msg != nil {
			fetched = append(fetched, msg)
		}
	}
	if err := <-done; err != nil {
		return err
	}
	sort.Slice(fetched, func(i, j int) bool { return order[fetched[i].Uid] < order[fetched[j].Uid] })
	for _, msg := range fetched {
		parsed, err := parseMessage(cfg, msg, section)
		if err != nil {
			return fmt.Errorf("parse uid %d: %w", msg.Uid, err)
		}
		if err := p.OnMessage(ctx, parsed); err != nil {
			return fmt.Errorf("ingest uid %d: %w", msg.Uid, err)
		}
		if err := p.Cursors.Set(ctx, cfg.AccountKey(), msg.Uid); err != nil {
			return err
		}
	}
	return nil
}

func parseMessage(cfg Config, msg *imap.Message, section *imap.BodySectionName) (Message, error) {
	out := Message{Account: cfg.Username, UID: msg.Uid}
	body := msg.GetBody(section)
	if body == nil {
		return out, fmt.Errorf("empty body")
	}
	reader, err := gomail.CreateReader(body)
	if err != nil && !isUnknownCharset(err) {
		return out, err
	}
	if reader == nil {
		return out, err
	}
	defer reader.Close()
	header := &reader.Header
	subject, _ := header.Subject()
	out.Subject = strings.TrimSpace(subject)
	if from, err := header.AddressList("From"); err == nil {
		out.From = joinAddresses(from)
	}
	if to, err := header.AddressList("To"); err == nil {
		out.To = addressStrings(to)
	}
	if cc, err := header.AddressList("Cc"); err == nil {
		out.Cc = addressStrings(cc)
	}
	messageID, _ := header.MessageID()
	if messageID == "" {
		messageID = fmt.Sprintf("%s-%d", cfg.AccountKey(), msg.Uid)
	}
	out.MessageID = messageID
	refs, _ := header.MsgIDList("References")
	reply, _ := header.MsgIDList("In-Reply-To")
	switch {
	case len(refs) > 0:
		out.ThreadKey = refs[0]
	case len(reply) > 0:
		out.ThreadKey = reply[0]
	default:
		out.ThreadKey = subjectThreadKey(out.Subject)
	}
	if date, err := header.Date(); err == nil && !date.IsZero() {
		out.ReceivedAt = date
	}
	if out.ReceivedAt.IsZero() {
		out.ReceivedAt = msg.InternalDate
	}
	if out.ReceivedAt.IsZero() {
		out.ReceivedAt = time.Now()
	}

	var plain, htmlBody strings.Builder
	for {
		part, partErr := reader.NextPart()
		if partErr == io.EOF {
			break
		}
		if partErr != nil && !isUnknownCharset(partErr) {
			break
		}
		if part == nil {
			continue
		}
		contentType := strings.ToLower(part.Header.Get("Content-Type"))
		disposition := strings.ToLower(part.Header.Get("Content-Disposition"))
		if strings.Contains(disposition, "attachment") {
			if name := attachmentName(disposition); name != "" {
				out.Attachments = append(out.Attachments, name)
			}
			continue
		}
		if strings.HasPrefix(contentType, "text/plain") {
			appendLimited(&plain, part.Body, 256<<10)
		} else if strings.HasPrefix(contentType, "text/html") {
			appendLimited(&htmlBody, part.Body, 512<<10)
		}
	}
	out.Text = strings.TrimSpace(plain.String())
	if out.Text == "" && htmlBody.Len() > 0 {
		out.Text = htmlToText(htmlBody.String())
	}
	out.Text = truncate(strings.TrimSpace(out.Text), 32<<10)
	out.Snippet = truncate(strings.Join(strings.Fields(out.Text), " "), 240)
	return out, nil
}

func appendLimited(builder *strings.Builder, reader io.Reader, limit int64) {
	_, _ = io.Copy(builder, io.LimitReader(reader, limit))
}

func addressStrings(list []*gomail.Address) []string {
	out := make([]string, 0, len(list))
	for _, addr := range list {
		out = append(out, addr.String())
	}
	return out
}

func joinAddresses(list []*gomail.Address) string {
	return strings.Join(addressStrings(list), ", ")
}

func subjectThreadKey(subject string) string {
	normalized := strings.TrimSpace(strings.ToLower(subject))
	for {
		trimmed := normalized
		for _, prefix := range []string{"re:", "re：", "fw:", "fwd:", "回复：", "转发："} {
			if strings.HasPrefix(trimmed, prefix) {
				trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
			}
		}
		if trimmed == normalized {
			break
		}
		normalized = trimmed
	}
	sum := sha1.Sum([]byte(normalized))
	return "subject:" + hex.EncodeToString(sum[:])
}

var (
	htmlScript = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>|<style[^>]*>.*?</style>`)
	htmlTags   = regexp.MustCompile(`(?s)<[^>]+>`)
	spaces     = regexp.MustCompile(`[ \t\r\f\v]+`)
	blankLines = regexp.MustCompile(`\n{3,}`)
)

func htmlToText(raw string) string {
	text := htmlScript.ReplaceAllString(raw, " ")
	text = htmlTags.ReplaceAllString(text, "\n")
	text = html.UnescapeString(text)
	text = spaces.ReplaceAllString(text, " ")
	text = blankLines.ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text)
}

func attachmentName(disposition string) string {
	for _, marker := range []string{"filename=\"", "filename*=\""} {
		if index := strings.Index(disposition, marker); index >= 0 {
			rest := disposition[index+len(marker):]
			if end := strings.Index(rest, "\""); end >= 0 {
				return truncate(rest[:end], 200)
			}
		}
	}
	return ""
}

func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

func isUnknownCharset(err error) bool {
	// go-message returns a partially usable reader for unknown charsets.
	return strings.Contains(strings.ToLower(err.Error()), "unknown charset")
}

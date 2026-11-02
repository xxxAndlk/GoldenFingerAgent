package email

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

type memoryCursors struct{ value Cursor }

func (m *memoryCursors) Get(context.Context, string) (Cursor, error) { return m.value, nil }
func (m *memoryCursors) Set(_ context.Context, _ string, cursor Cursor) error {
	m.value = cursor
	return nil
}

func serveFakeIMAP(t *testing.T, listener net.Listener, raw string, uidValidity uint32) {
	t.Helper()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		writer := bufio.NewWriter(conn)
		reader := bufio.NewReader(conn)
		fmt.Fprint(writer, "* OK fake imap ready\r\n")
		writer.Flush()
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSpace(line)
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			tag := fields[0]
			upper := strings.ToUpper(line)
			switch {
			case strings.Contains(upper, " LOGIN "):
				fmt.Fprintf(writer, "%s OK LOGIN completed\r\n", tag)
			case strings.Contains(upper, " SELECT ") || strings.Contains(upper, " EXAMINE "):
				fmt.Fprintf(writer, "* 1 EXISTS\r\n* OK [UIDVALIDITY %d] UIDs valid\r\n%s OK [READ-ONLY] SELECT completed\r\n", uidValidity, tag)
			case strings.Contains(upper, " UID SEARCH "):
				fmt.Fprintf(writer, "* SEARCH 1\r\n%s OK SEARCH completed\r\n", tag)
			case strings.Contains(upper, " UID FETCH "):
				fmt.Fprintf(writer, "* 1 FETCH (UID 1 BODY[] {%d}\r\n", len(raw))
				fmt.Fprint(writer, raw)
				fmt.Fprintf(writer, ")\r\n%s OK FETCH completed\r\n", tag)
			case strings.Contains(upper, " LOGOUT"):
				fmt.Fprintf(writer, "* BYE\r\n%s OK LOGOUT completed\r\n", tag)
				writer.Flush()
				return
			default:
				fmt.Fprintf(writer, "%s OK completed\r\n", tag)
			}
			writer.Flush()
		}
	}()
}

func TestPollOnceFetchesAndAdvancesCursor(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	raw := "From: Client <client@example.com>\r\n" +
		"To: Me <me@example.com>\r\n" +
		"Subject: 测试邮件\r\n" +
		"Message-Id: <imap-1@example.com>\r\n" +
		"Date: Sat, 01 Nov 2026 10:00:00 +0800\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\n" +
		"请周五前提交报价单。\r\n"
	serveFakeIMAP(t, listener, raw, 1)

	cursors := &memoryCursors{}
	var got []Message
	poller := NewPoller(nil, cursors, func(_ context.Context, message Message) error {
		got = append(got, message)
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = poller.PollOnce(ctx, Config{
		Host: "127.0.0.1", Port: port, Username: "me@example.com", Password: "secret",
		Mailbox: "INBOX", TLS: false, InitialFetch: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("messages=%d", len(got))
	}
	if got[0].MessageID != "imap-1@example.com" || !strings.Contains(got[0].Text, "周五") {
		t.Fatalf("message=%+v", got[0])
	}
	if cursors.value.UID != 1 || cursors.value.UIDValidity != 1 {
		t.Fatalf("cursor=%+v", cursors.value)
	}
}

func TestPollOnceResetsCursorWhenUIDValidityChanges(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	raw := "From: Client <client@example.com>\r\n" +
		"To: Me <me@example.com>\r\n" +
		"Subject: 重置后的邮件\r\n" +
		"Message-Id: <imap-reset@example.com>\r\n" +
		"Date: Sat, 01 Nov 2026 10:00:00 +0800\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\n" +
		"邮箱重建后仍应拉取。\r\n"
	serveFakeIMAP(t, listener, raw, 2)

	// 旧游标 UIDVALIDITY=1、UID=1；新邮箱 UIDVALIDITY=2，UID 不能继续比较。
	cursors := &memoryCursors{value: Cursor{UIDValidity: 1, UID: 1}}
	var got []Message
	poller := NewPoller(nil, cursors, func(_ context.Context, message Message) error {
		got = append(got, message)
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := poller.PollOnce(ctx, Config{
		Host: "127.0.0.1", Port: port, Username: "me@example.com", Password: "secret",
		Mailbox: "INBOX", InitialFetch: 10,
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.Contains(got[0].Text, "邮箱重建") {
		t.Fatalf("messages=%+v", got)
	}
	if cursors.value.UIDValidity != 2 || cursors.value.UID != 1 {
		t.Fatalf("cursor=%+v", cursors.value)
	}
}

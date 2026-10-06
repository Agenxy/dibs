package humanask

import (
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/notify"
)

func TestContactOpenUsesOnlyValidatedFixedAppLink(t *testing.T) {
	for _, bad := range []string{
		"file:///tmp/payload", "codex://threads/ok?other=yes",
		"codex://threads/bad\nargument", "claude://code/continue?session=local_ok&x=1",
	} {
		if contactOpenArgv(bad) != nil {
			t.Fatalf("accepted unsafe link %q", bad)
		}
	}
	const url = "codex://threads/019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
	var got []string
	old := openContact
	openContact = func(argv []string) error { got = append([]string(nil), argv...); return nil }
	defer func() { openContact = old }()
	m := Message{
		Type: "contact", Body: "SECRET PARTICIPANT BODY",
		Contact: &Contact{
			Sender: "asker", Recipient: "worker", Kind: "request", Message: 42,
			OpenURL: url, OpenHint: "open worker in ChatGPT",
		},
		ask: func(title, body string, receipt notify.Receipt, buttons ...string) (string, error) {
			if strings.Contains(title+body, "SECRET PARTICIPANT BODY") {
				t.Fatal("participant body leaked into contact notification")
			}
			if len(buttons) != 2 || buttons[1] != "Open" {
				t.Fatalf("buttons=%v", buttons)
			}
			return "Open", nil
		},
	}
	if _, err := Ask(m); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "/usr/bin/open" || got[1] != url {
		t.Fatalf("open argv=%v", got)
	}
}

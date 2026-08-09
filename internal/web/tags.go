package web

import (
	"fmt"
	"html/template"
	"time"
)

// humanAgo renders durations the way a person says them.
func humanAgo(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	switch {
	case d < 0:
		return "now"
	case d < 5*time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// stateTag renders a message state as a semantically colored tag.
func stateTag(state string) template.HTML {
	class := "mute"
	switch state {
	case "answered", "approved", "acked":
		class = "ok"
	case "pending", "delivered":
		class = "wait"
	case "denied", "expired_recipient_dead":
		class = "bad"
	case "declined", "displaced", "expired_unanswered", "expired_recipient_dormant":
		class = "mute"
	}
	// #nosec G203 -- not attacker-controlled: these are vendored assets embedded
	// at compile time from this repository, or a string already passed through
	// template.HTMLEscapeString on the line itself.
	return template.HTML(fmt.Sprintf(`<span class="tag %s">%s</span>`, class, template.HTMLEscapeString(state)))
}

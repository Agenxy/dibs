// Package humanask puts one message to the PERSON and returns what they
// said, on whatever machine it runs on.
//
// It used to live inside the engine, which was right while the person always
// sat at the machine running the board. It is not right for a board on a
// server: there the person's own Mac shows the message (the human relay,
// docs/NETWORK.md §8), and the relay and the board must ask in exactly the
// same way, or the same request reads differently depending on where it was
// shown. So the asking is here, and both call it; recording the answer stays
// with each caller, because that is where the two differ.
package humanask

import (
	"errors"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/notify"
)

// Message is one message for the person.
type Message struct {
	Type            string // question, request, handoff, notify
	From            string
	FromName        string // current name; From stays the stable ID
	Who             string // the daemon's line about the sender, never the sender's own words
	Body            string
	Contact         *Contact // daemon-authored reachability notice, never participant body
	Choices         []string
	Grant           string
	Adopt           string
	AdoptName       string // current name; Adopt stays the stable ID
	Serial          uint64 // stable message identity on Node
	Node            string // board identity; serials are not global
	Receipt         notify.Receipt
	DeliveryReceipt notify.DeliveryReceipt
	ask             func(string, string, notify.Receipt, ...string) (string, error) // test presenter
}

// Contact contains only a system-composed summary. OpenURL is permitted only
// after the daemon proved that the recipient belongs to the named local app;
// the button validates its exact link grammar again at the point of use.
type Contact struct {
	Recipient string `json:"recipient"`
	Sender    string `json:"sender"`
	Kind      string `json:"kind"`
	Message   uint64 `json:"message"`
	Count     int    `json:"count"`
	OpenURL   string `json:"open_url,omitempty"`
	OpenHost  string `json:"open_host,omitempty"`
	OpenHint  string `json:"open_hint"`
}

func (m Message) displayFrom() string {
	if m.FromName != "" {
		return OneLine(m.FromName)
	}
	return m.From
}

func (m Message) displayAdopt() string {
	if m.AdoptName != "" {
		name := OneLine(m.AdoptName)
		if name != m.Adopt {
			return name + " (formerly " + m.Adopt + ")"
		}
		return name
	}
	return m.Adopt
}

// Answer is what the person said. An empty Disposition is no answer:
// dismissed, deferred, or a message that asks for none.
type Answer struct {
	Disposition string // approve, deny, answer
	Body        string
}

// Ask raises the message and waits for the person. notify.ErrCannotNotify
// means nobody saw it, which the caller reports.
func Ask(m Message) (Answer, error) {
	title := "Dibs · " + m.displayFrom()
	switch m.Type {
	case "contact":
		return askContact(m)
	case core.MsgRequest:
		// A request is literally "approve or deny", so ask it that way.
		return approve(m)
	case core.MsgQuestion:
		// Answerable, not merely announced. A question that arrives as a banner
		// is a notification that the board has something on it, which is what
		// the board already was: the person still has to go and open it, and
		// the asking agent waits out its deadline while they decide whether to.
		return answer(m)
	case core.MsgHandoff:
		if m.DeliveryReceipt != nil {
			return Answer{}, notify.BannerWithDeliveryReceipt(title, "hands work to you", OneLine(m.Body), m.DeliveryReceipt)
		}
		return Answer{}, notify.BannerWithReceipt(title, "hands work to you", OneLine(m.Body), m.Receipt)
	default:
		if m.DeliveryReceipt != nil {
			return Answer{}, notify.BannerWithDeliveryReceipt(title, "says", OneLine(m.Body), m.DeliveryReceipt)
		}
		return Answer{}, notify.BannerWithReceipt(title, "says", OneLine(m.Body), m.Receipt)
	}
}

var openContact = func(argv []string) error {
	// #nosec G204 -- argv is reconstructed by the two validated app-link
	// builders below, with a fixed /usr/bin/open executable and no shell.
	return exec.Command(argv[0], argv[1:]...).Run()
}

func contactOpenArgv(url string) []string {
	if thread, ok := strings.CutPrefix(url, "codex://threads/"); ok {
		argv := harnessenv.ChatGPTOpenArgv(thread)
		if len(argv) == 2 && argv[1] == url {
			return argv
		}
	}
	if local, ok := strings.CutPrefix(url, "claude://code/continue?session="); ok {
		argv := harnessenv.ClaudeOpenArgv(local)
		if len(argv) == 2 && argv[1] == url {
			return argv
		}
	}
	return nil
}

func askContact(m Message) (Answer, error) {
	c := m.Contact
	if c == nil {
		return Answer{}, errors.New("contact notice has no system summary")
	}
	title := "Dibs · " + OneLine(c.Sender) + " needs " + OneLine(c.Recipient)
	line := "Unread " + OneLine(c.Kind) + " #" + strconv.FormatUint(c.Message, 10) +
		"; " + OneLine(c.OpenHint)
	if c.Count > 1 {
		line += "; " + strconv.Itoa(c.Count) + " messages coalesced"
	}
	argv := contactOpenArgv(c.OpenURL)
	if argv == nil {
		if m.DeliveryReceipt != nil {
			return Answer{}, notify.BannerWithDeliveryReceipt(title, "cannot reach this agent", line, m.DeliveryReceipt)
		}
		return Answer{}, notify.BannerWithReceipt(title, "cannot reach this agent", line, m.Receipt)
	}
	pressed, err := m.askNotification(title, line, "Later", "Open")
	if err != nil || pressed != "Open" {
		return Answer{}, err
	}
	return Answer{}, openContact(argv)
}

// RequestTitle is the line that states what pressing Approve DOES.
//
// The title is the daemon's sentence, not the sender's. It is the only line on
// the notification that states the effect, so it must come from the typed
// field rather than from the prose beside it. An agent writes the body; if the
// body were the only thing the person read, a request that says "just need to
// check something" could carry grant: coordinator and be approved by somebody
// who never saw the word. The body is still shown, as the reason, underneath.
//
// Every effect, not the first one. This was a switch, so a request carrying
// BOTH a grant and an adoption rendered as "make X coordinator?" and moved a
// mailbox on the same yes. core.Admit refuses that combination now, and this
// does not rely on it: if a second effect ever reaches here, the person reads
// it rather than approving it blind.
func RequestTitle(from, grant, adopt string) string {
	switch {
	case grant != "" && adopt != "":
		return "Dibs · make " + from + " " + grant + " AND give it " + adopt + "'s mail?"
	case grant == core.PermRelocate:
		// A permission, not a role, so not "make X relocate?".
		return "Dibs · let " + from + " move agents to other environments?"
	case grant != "":
		return "Dibs · make " + from + " " + grant + "?"
	case adopt != "":
		return "Dibs · give " + adopt + "'s mail to " + from + "?"
	}
	return "Dibs · " + from + " requests"
}

func approve(m Message) (Answer, error) {
	choice, err := m.askNotification(RequestTitle(m.displayFrom(), m.Grant, m.displayAdopt()),
		Said(m.Who, m.Body), "Deny", "Later", "Approve")
	if errors.Is(err, notify.ErrCannotNotify) {
		return Answer{}, err
	}
	if err != nil || choice == "" || choice == DeferButton {
		// Dismissed or deferred is not an answer, and inventing one would be
		// answering on their behalf. The request stays open on the board.
		//
		// Said out loud when the ASK itself came back empty, because that is the
		// case where the operator may never have been shown anything, and until
		// this it was indistinguishable from a deliberate "not now". An agent
		// then waited out its deadline against a question nobody saw.
		if err != nil {
			slog.Warn("the human was asked and nothing came back", "from", m.From, "msg", m.Serial, "err", err)
		}
		return noAnswer(m, err)
	}
	if choice == "Approve" {
		return Answer{Disposition: "approve", Body: "answered from the desktop notification"}, nil
	}
	return Answer{Disposition: "deny", Body: "answered from the desktop notification"}, nil
}

// answer puts a question to the person.
//
// Two shapes, because a question has two. When the asker enumerated the
// answers they become the buttons, and answering is one press with nothing to
// type and no window to find. When it did not, the notification offers to
// open a box, and only then does anything take the screen.
//
// That order is the whole design. The alternative is to raise the text box on
// arrival, which is a coordination service deciding that its optional
// question outranks whatever the person was doing: the same reason Ask goes
// through the bundle rather than a modal alert. Nothing here steals focus
// until the human has pressed something asking it to.
func answer(m Message) (Answer, error) {
	title := "Dibs · " + m.displayFrom() + " asks"
	line := Said(m.Who, m.Body)
	plan := PlanFor(m.Choices, notify.CanPrompt())

	pressed, err := m.askNotification(title, line, plan.Buttons...)
	if errors.Is(err, notify.ErrCannotNotify) {
		return Answer{}, err
	}
	if err != nil || pressed == "" || pressed == DeferButton {
		// Dismissed or deferred is not an answer. The question stays open.
		return noAnswer(m, err)
	}
	if plan.Then == "" {
		return Answer{Disposition: "answer", Body: pressed}, nil // the press WAS the answer
	}

	// Only now, after a press that asked for it, does anything take the screen.
	var text string
	switch plan.Then {
	case ThenPick:
		text, err = notify.Pick(title, line, m.Choices...)
	case ThenBoard:
		// No text field on this platform: the press asked where to answer,
		// and that is the one thing a notification here can still say.
		_ = notify.Banner(title, "", "Answer this one on the board: `dibs web` opens it. "+
			"The question stays open until you do.")
		return Answer{}, nil
	default:
		text, err = notify.Prompt(title, line)
	}
	if err != nil || strings.TrimSpace(text) == "" {
		// Opening the box and closing it again is still not an answer.
		return noAnswer(m, err)
	}
	return Answer{Disposition: "answer", Body: text}, nil
}

// noAnswer is the person not answering: dismissed, deferred, or a dialog
// that failed after they had seen the notification. Failures propagate;
// dismissal supplies no answer and the message stays open on the board.
func noAnswer(m Message, err error) (Answer, error) {
	if err != nil {
		slog.Warn("the human was asked and no answer came back", "from", m.From, "msg", m.Serial, "err", err)
	}
	return Answer{}, err
}

// How an answer is collected once the human has asked to give one.
const (
	ThenPick   = "pick"   // a list, because the choices did not fit as buttons
	ThenPrompt = "prompt" // a text box, because there were no choices
	ThenBoard  = "board"  // a pointer to the board, because there is no text box here
)

// DeferButton is the way out that is offered on every question and means
// nothing: it exists so dismissing is a deliberate press rather than the only
// thing a person can do with a notification they do not want to answer yet.
const DeferButton = "Later"

// Plan is how a question will be put to the person: what the notification
// carries, and what pressing it opens.
type Plan struct {
	Buttons []string
	Then    string // "" when the press itself is the answer
}

// PlanFor decides the shape of the interaction, separately from performing
// it, because performing it means a person at a keyboard and that is not
// available to a test. The decision is the part with a rule in it.
//
// The rule: NOTHING opens without a press first. A question is by definition
// something its asker can wait for.
//
// Three buttons is what a notification carries, so up to three choices ARE
// the buttons and answering is one press. A fourth cannot be, and rather than
// silently dropping it the notification offers the list. Where the platform
// cannot open a text field (notify.CanPrompt), a question with no choices is
// not offered "Write answer…": the press opened nothing and said nothing on
// Linux, so the button names the one thing it can do, point at the board.
func PlanFor(choices []string, canPrompt bool) Plan {
	if n := len(choices); n > 0 && n <= 3 {
		return Plan{Buttons: choices}
	}
	if len(choices) > 0 {
		return Plan{Buttons: []string{DeferButton, "Pick one…"}, Then: ThenPick}
	}
	if !canPrompt {
		return Plan{Buttons: []string{DeferButton, "Where to answer…"}, Then: ThenBoard}
	}
	// "Write answer…", not "Answer". A button labelled Answer promises a field
	// that is not there: you press it expecting to type, and the notification
	// vanishes while a box opens somewhere else. Reported exactly that way,
	// twice. The verb says what the press DOES, and the ellipsis keeps the
	// platform's own promise that something further opens.
	return Plan{Buttons: []string{DeferButton, "Write answer…"}, Then: ThenPrompt}
}

// OneLine keeps a notification readable. A banner truncates anyway, and a
// multi-paragraph handoff rendered into one is unreadable rather than
// informative.
func OneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const limit = 180
	if len(s) > limit {
		return s[:limit] + "…"
	}
	return s
}

// Said puts WHO above what they wrote.
//
// The identity line is composed by the daemon; the body is the agent's own
// text. Keeping them on separate lines, in that order, means the first thing
// read is the part the sender did not author. A request whose body says
// "routine, just approve" cannot be the first thing a person sees.
func Said(who, body string) string {
	line := OneLine(body)
	if who == "" {
		return line
	}
	return who + "\n" + line
}

func (m Message) askNotification(title, body string, buttons ...string) (string, error) {
	if m.ask == nil && m.DeliveryReceipt != nil {
		var pressed string
		var err error
		if m.Node == "" {
			pressed, err = notify.AskWithDeliveryReceipt(title, body, m.DeliveryReceipt, buttons...)
		} else {
			pressed, err = notify.AskMessageWithDeliveryReceipt(m.Node, m.Serial, title, body, m.DeliveryReceipt, buttons...)
		}
		if pressed == DeferButton {
			m.DeliveryReceipt(notify.ReceiptData{State: "dismissed"})
		}
		return pressed, err
	}
	ask := m.ask
	if ask == nil {
		ask = notify.AskWithReceipt
		if m.Node != "" {
			ask = func(title, body string, receipt notify.Receipt, buttons ...string) (string, error) {
				return notify.AskMessage(m.Node, m.Serial, title, body, receipt, buttons...)
			}
		}
	}
	pressed, err := ask(title, body, m.Receipt, buttons...)
	if pressed == DeferButton && m.Receipt != nil {
		m.Receipt("dismissed")
	}
	return pressed, err
}

package web

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kollektivhuset-rudbeckia/members/internal/config"
	"github.com/kollektivhuset-rudbeckia/members/internal/i18n"
	"github.com/kollektivhuset-rudbeckia/members/internal/store"
)

// This is where the registry tells the house what happened.
//
// The messages are written in Swedish rather than through the i18n catalogue,
// unlike everything a member reads. They go to two of the association's own
// channels, which have no reader whose language we know — a channel is not a
// person — and the audit trail they are drawn from is already written in
// Swedish in the same way. Putting multi-line Markdown in the phrase
// catalogue would cost more than it buys.

// announceTimeout bounds one notification. It is generous on purpose: whoever
// caused the change is already looking at their next page and nobody is
// waiting for this.
const announceTimeout = 30 * time.Second

// announce sends one message to one channel, in the background.
//
// Notifications must never make a request slower or, worse, fail one. A board
// member saving a correction should not see an error page because the chat
// server is down, so this returns immediately and reports trouble to the log.
func (s *Server) announce(channel, kind, message string) {
	if strings.TrimSpace(channel) == "" {
		// No channel configured for this kind of thing. Not a failure: a
		// house that wants no announcements simply leaves it out.
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), announceTimeout)
		defer cancel()

		// While the messages are being written they go to one person instead
		// of to the house. The channel it would have gone to is named in the
		// message, because the routing is the part worth checking and it is
		// invisible once everything arrives in the same direct conversation.
		if to := s.rt.Chat.TestUser; to != "" {
			body := fmt.Sprintf("_(test: hade gått till kanal `%s`)_\n\n%s", channel, message)
			if err := s.chat.DM(ctx, to, body); err != nil {
				s.log.Error("could not send the test message", "kind", kind, "err", err)
				return
			}
			s.log.Info("sent a test message instead of announcing",
				"kind", kind, "would_be_channel", channel, "to", to)
			return
		}

		if err := s.chat.Post(ctx, channel, message); err != nil {
			s.log.Error("could not announce in the chat", "kind", kind, "err", err)
			return
		}
		s.log.Info("announced in the chat", "kind", kind, "channel", channel)
	}()
}

// announceInterest tells the interview team that somebody has put their name
// down through the public page.
//
// This is the one notification that is not about the register at all, and it
// is the one that matters most: somebody who fills in a form and hears nothing
// is the worst thing this system can do to a person. It goes to the interview
// team's own channel so that the answer comes from a human, quickly.
func (s *Server) announceInterest(c store.Candidate) {
	var m strings.Builder
	fmt.Fprintf(&m, ":raising_hand: **Ny intresseanmälan** — %s\n\n", cell(nameOrUnknown(c.Name())))
	m.WriteString("| | |\n|---|---|\n")
	row(&m, "Namn", nameOrUnknown(c.Name()))
	row(&m, "E-post", c.Email)
	row(&m, "Telefon", c.Phone)
	// Why, rather than which membership: everybody who applies becomes a
	// vänmedlem, so the useful line is what drew them here.
	row(&m, "Varför", s.reasonWords(c, string(s.defaultLang())))
	if c.Apartment != "" {
		row(&m, "Lägenhet", c.Apartment)
	}
	m.WriteString("\n")
	if msg := strings.TrimSpace(c.Message); msg != "" {
		fmt.Fprintf(&m, "%s\n\n", blockquote(msg))
	}
	fmt.Fprintf(&m, "Hen ligger nu i **%s** på [kandidattavlan](%s/kandidater).\n",
		s.stageName(c.Stage), s.rt.BaseURL)

	s.announce(s.cfg.Chat.Candidates, "candidate.registered", m.String())
}

// announceChange tells the board that the register changed, or that somebody
// has asked for it to.
//
// It is called from audit(), which is the one place every change already
// passes through. That is deliberate: a notification hung off each handler
// would be forgotten by the next handler somebody writes, whereas anything
// that reaches the trail reaches the channel for free.
func (s *Server) announceChange(actor, role, action string, m store.Member, detail string) {
	if !s.cfg.Chat.Announces(action) {
		return
	}
	headline, ok := headlines[action]
	if !ok {
		// An action nobody has written words for. Say something true rather
		// than nothing, so a new kind of change is visible as soon as it
		// exists instead of waiting for somebody to notice it is missing.
		headline = ":pencil: **" + action + "**"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s — %s\n\n", headline, cell(nameOrUnknown(m.Name())))
	b.WriteString("| | |\n|---|---|\n")
	row(&b, "Medlem", nameOrUnknown(m.Name()))
	row(&b, "E-post", m.Email)
	row(&b, "Typ", s.kindWord(m.Kind))
	row(&b, "Av", actorName(actor, role))
	if detail != "" {
		row(&b, "Ändring", detail)
	}
	b.WriteString("\n")

	if strings.HasPrefix(action, "proposal.update") || strings.HasPrefix(action, "proposal.delete") {
		fmt.Fprintf(&b, ":hourglass: Väntar på styrelsen: [ta ställning till förslaget](%s/andringar)\n",
			s.rt.BaseURL)
	} else if m.ID != "" {
		fmt.Fprintf(&b, "[Öppna i registret](%s/medlem/%s)\n", s.rt.BaseURL, m.ID)
	}

	s.announce(s.cfg.Chat.Registry, action, b.String())
}

// headlines are the words for each thing the trail records. Only the actions
// that reach a channel need one; the rest fall back to their own name.
var headlines = map[string]string{
	"member.added":      ":tada: **Ny medlem**",
	"member.changed":    ":pencil2: **Ändrad medlem**",
	"member.removed":    ":wave: **Medlem borttagen**",
	"proposal.update":   ":raised_hand: **Föreslagen ändring**",
	"proposal.delete":   ":raised_hand: **Föreslagen borttagning**",
	"proposal.approved": ":white_check_mark: **Förslag godkänt**",
	"proposal.rejected": ":x: **Förslag avslaget**",
	"payment.recorded":  ":moneybag: **Avgift betald**",
	"payment.withdrawn": ":arrow_backward: **Avgift återtagen**",
}

// row writes one line of a two-column Markdown table, skipping the empties so
// a message never carries a heading with nothing after it.
func row(b *strings.Builder, label, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	fmt.Fprintf(b, "| **%s** | %s |\n", label, cell(value))
}

// cell escapes the pipes and newlines that would otherwise break out of a
// table cell. Names, notes and addresses all arrive from a form, so this is
// the boundary where they stop being able to rearrange the message.
func cell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.Join(strings.Fields(s), " ")
}

// blockquote shows what somebody wrote about themselves as a Markdown quote.
//
// Every line gets the marker, including the ones the writer made by pressing
// return. A single unquoted line would stop being a quotation and start being
// part of the registry's own message, which is the one thing a stranger
// typing into a public form must not be able to do.
func blockquote(s string) string {
	lines := strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' })
	for i, l := range lines {
		lines[i] = "> " + strings.TrimSpace(l)
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

// actorName is who did it, in the house's terms rather than as an address.
func actorName(actor, role string) string {
	who := map[string]string{
		"intake":  "intervjugruppen",
		"cashier": "kassören",
		"board":   "styrelsen",
	}[role]
	switch {
	case actor != "" && who != "":
		return fmt.Sprintf("%s (%s)", who, actor)
	case actor != "":
		return actor
	case who != "":
		return who
	default:
		return "registret"
	}
}

// nameOrUnknown keeps a message readable when somebody has filled in an
// address but no name, which the public form allows.
func nameOrUnknown(name string) string {
	if n := strings.TrimSpace(name); n != "" {
		return n
	}
	return "namn saknas"
}

// kindWord is the membership in the house's own words, in the deployment's
// language. The catalogue already holds them for the register's own pages.
func (s *Server) kindWord(k config.Kind) string {
	return i18n.T(s.defaultLang(), kindKey(k))
}

// stageName is the candidate's column in the words the board uses for it.
func (s *Server) stageName(id string) string {
	if st, ok := s.cfg.Pipeline.Stage(id); ok {
		return st.NameFor(string(s.cfg.Site.Language))
	}
	return id
}

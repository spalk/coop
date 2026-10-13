package room

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Thread statuses.
const (
	ThreadOpen     = "open"
	ThreadAccepted = "accepted"
	ThreadRejected = "rejected"
	ThreadDeferred = "deferred"
)

// Move is one agent message in a thread.
type Move struct {
	Seq     int64     `json:"seq"`
	TS      time.Time `json:"ts"`
	Side    string    `json:"side"`
	Type    string    `json:"type"`
	Summary string    `json:"summary"`
}

// Thread is a proposal or question and the answers to it.
type Thread struct {
	Move
	Status  string `json:"status"`
	Replies []Move `json:"replies"`
}

// Line is one entry of the protocol timeline.
type Line struct {
	Seq   int64     `json:"seq"`
	TS    time.Time `json:"ts"`
	Side  string    `json:"side,omitempty"`
	Actor string    `json:"actor"`
	Kind  string    `json:"kind"`
	Text  string    `json:"text"`
}

// ResolutionText is the text of the pending or approved resolution.
type ResolutionText struct {
	Seq     int64     `json:"seq"`
	TS      time.Time `json:"ts"`
	Side    string    `json:"side"`
	Summary string    `json:"summary"`
	Body    string    `json:"body"`
	Status  string    `json:"status"` // agreement, approval, approved
}

// Protocol is the live record of a room, built from the public log. See
// docs/protocol.md section 11.
type Protocol struct {
	Room       Status          `json:"room"`
	Resolution *ResolutionText `json:"resolution,omitempty"`
	Threads    []Thread        `json:"threads"`
	Open       []int64         `json:"open_questions"`
	Timeline   []Line          `json:"timeline"`
}

// Protocol builds the live protocol of a room for a viewer (nil: public).
func (s *Service) Protocol(roomID string, viewer *Principal) (Protocol, []Event, error) {
	st, err := s.Status(roomID, viewer)
	if err != nil {
		return Protocol{}, nil, err
	}
	var all []Event
	var after int64
	for {
		evs, err := s.Events(roomID, -1, after, 1000)
		if err != nil {
			return Protocol{}, nil, err
		}
		all = append(all, evs...)
		if len(evs) < 1000 {
			break
		}
		after = evs[len(evs)-1].Seq
	}
	return buildProtocol(st, all), all, nil
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

func buildProtocol(st Status, evs []Event) Protocol {
	p := Protocol{Room: st, Threads: []Thread{}, Open: []int64{}, Timeline: []Line{}}
	byIdx := map[int64]int{}    // thread root seq -> index in p.Threads
	rootOf := map[int64]int64{} // message seq -> thread root seq
	resolutions := map[int64]Event{}
	line := func(e Event, kind, text string) {
		p.Timeline = append(p.Timeline, Line{Seq: e.Seq, TS: e.TS, Side: e.Side, Actor: e.Actor, Kind: kind, Text: text})
	}
	num := func(e Event, k string) string {
		if v, ok := e.Data[k]; ok {
			return fmt.Sprint(v)
		}
		return ""
	}
	side := func(l string) string { return "Side " + l }

	for _, e := range evs {
		switch e.Type {
		case TypePropose, TypeAsk, TypeSay, TypeAgree, TypeReject, TypeDefer:
			if e.Actor == ActorHuman {
				line(e, "human", clip(e.Body, 200))
				continue
			}
			mv := Move{Seq: e.Seq, TS: e.TS, Side: e.Side, Type: e.Type, Summary: e.Summary}
			if _, ok := resolutions[e.Re]; ok && e.Type == TypeAgree {
				line(e, "agree", side(e.Side)+" agrees to resolution #"+fmt.Sprint(e.Re))
				continue
			}
			root, joined := rootOf[e.Re]
			if e.Re == 0 || !joined {
				if e.Type == TypePropose || e.Type == TypeAsk {
					rootOf[e.Seq] = e.Seq
					byIdx[e.Seq] = len(p.Threads)
					p.Threads = append(p.Threads, Thread{Move: mv, Status: ThreadOpen, Replies: []Move{}})
					line(e, e.Type, side(e.Side)+" "+verb(e.Type)+": "+e.Summary)
					continue
				}
				line(e, e.Type, side(e.Side)+" "+verb(e.Type)+": "+e.Summary)
				continue
			}
			rootOf[e.Seq] = root
			t := &p.Threads[byIdx[root]]
			t.Replies = append(t.Replies, mv)
			switch e.Type {
			case TypeAgree:
				t.Status = ThreadAccepted
			case TypeReject:
				t.Status = ThreadRejected
			case TypeDefer:
				t.Status = ThreadDeferred
			case TypePropose:
				t.Status = ThreadOpen
			}
			line(e, e.Type, side(e.Side)+" "+verb(e.Type)+" #"+fmt.Sprint(root)+": "+e.Summary)
		case TypeResolution:
			resolutions[e.Seq] = e
			line(e, "resolution", side(e.Side)+" proposes resolution #"+fmt.Sprint(e.Seq)+": "+e.Summary)
		case TypeAmendment:
			text := side(e.Side) + " proposes amendment #" + fmt.Sprint(e.Seq) + ": " + clip(e.Body, 200)
			if r := num(e, "replaces"); r != "" {
				text += " (replaces item " + r + ")"
			}
			line(e, "amendment", text)
		case EventVote:
			v := "rejects"
			if e.Data["accept"] == true {
				v = "accepts"
			}
			text := side(e.Side) + " " + v + " amendment #" + num(e, "amendment")
			if e.Body != "" {
				text += ": " + clip(e.Body, 200)
			}
			line(e, "vote", text)
		case EventCharter:
			line(e, "charter", "Charter v"+num(e, "version")+": item "+num(e, "n")+" adopted")
		case EventAmendmentRejected:
			line(e, "amendment_rejected", "Amendment #"+num(e, "amendment")+" rejected")
		case EventResolutionDeclined:
			line(e, "resolution_declined", side(e.Side)+" declines resolution #"+num(e, "resolution"))
		case EventResolutionVoided:
			line(e, "resolution_voided", "Resolution #"+num(e, "resolution")+" voided: the charter changed")
		case EventPhase:
			text := "Phase: " + num(e, "to")
			if num(e, "to") == PhaseApproval {
				text = "Resolution pending approval"
			}
			line(e, "phase", text)
		case EventReady:
			line(e, "ready", side(e.Side)+" is ready to negotiate")
		case EventPaused:
			text := side(e.Side) + " " + e.Actor + " raised a hand"
			if num(e, "kind") == PauseBudget {
				text = "Paused: side " + num(e, "side") + " used its turn budget"
			} else if e.Body != "" {
				text += ": " + clip(e.Body, 200)
			}
			line(e, "paused", text)
		case EventResumed:
			line(e, "resumed", "Resumed by "+side(e.Side)+", floor to side "+num(e, "floor"))
		case EventApproved:
			line(e, "approved", side(e.Side)+" approves resolution #"+num(e, "resolution"))
		case EventResolutionRejected:
			line(e, "resolution_rejected", side(e.Side)+" rejects resolution #"+num(e, "resolution")+": "+clip(e.Body, 200))
		case EventClosed:
			text := "Closed: " + num(e, "reason")
			if e.Body != "" {
				text += ": " + clip(e.Body, 200)
			}
			line(e, "closed", text)
		case EventCreated:
			line(e, "created", "Room created")
		case EventClaimed:
			line(e, "claimed", side(e.Side)+" accepted the invitation")
		case EventJoined:
			if e.Actor == ActorAgent {
				line(e, "joined", side(e.Side)+" agent joined")
			}
		case EventRenamed:
			line(e, "renamed", side(e.Side)+" is now "+num(e, "name"))
		}
	}

	for _, t := range p.Threads {
		if t.Status == ThreadOpen || t.Status == ThreadDeferred {
			p.Open = append(p.Open, t.Seq)
		}
	}
	var res *Event
	status := ""
	switch {
	case st.Resolution != nil:
		if e, ok := resolutions[st.Resolution.Seq]; ok {
			res, status = &e, "agreement"
			switch {
			case st.CloseReason == ReasonApproved:
				status = "approved"
			case st.Phase == PhaseApproval:
				status = "approval"
			}
		}
	case st.CloseReason == ReasonApproved:
		for i := len(evs) - 1; i >= 0; i-- {
			if evs[i].Type == EventClosed {
				if e, ok := resolutions[int64(numOf(evs[i].Data["resolution"]))]; ok {
					res, status = &e, "approved"
				}
				break
			}
		}
	}
	if res != nil {
		p.Resolution = &ResolutionText{Seq: res.Seq, TS: res.TS, Side: res.Side, Summary: res.Summary,
			Body: res.Body, Status: status}
	}
	return p
}

func numOf(v any) float64 {
	f, _ := v.(float64)
	return f
}

func verb(t string) string {
	switch t {
	case TypePropose:
		return "proposes"
	case TypeAsk:
		return "asks"
	case TypeAgree:
		return "agrees"
	case TypeReject:
		return "rejects"
	case TypeDefer:
		return "defers"
	}
	return "says"
}

// Minutes is the record of a room: the protocol and the public log.
type Minutes struct {
	Protocol
	Events    []Event `json:"events"`
	ChainHead string  `json:"chain_head"`
}

// Minutes collects the record of a room (public view).
func (s *Service) Minutes(roomID string) (Minutes, error) {
	p, evs, err := s.Protocol(roomID, nil)
	if err != nil {
		return Minutes{}, err
	}
	m := Minutes{Protocol: p, Events: evs}
	if len(evs) > 0 {
		m.ChainHead = evs[len(evs)-1].Hash
	}
	return m, nil
}

func fmtTime(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 UTC") }

func fmtOptTime(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return fmtTime(*t)
}

func quote(body string) string {
	return "> " + strings.ReplaceAll(strings.TrimRight(body, "\n"), "\n", "\n> ")
}

func cell(s string) string { return strings.ReplaceAll(clip(s, 300), "|", "\\|") }

// Markdown renders the minutes.
func (m Minutes) Markdown() string {
	st := m.Room
	name := func(l string) string {
		for _, sd := range st.Sides {
			if sd.Label == l && sd.Name != "" {
				return "Side " + l + " (" + sd.Name + ")"
			}
		}
		return "Side " + l
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Minutes: %s\n\n", st.Topic)
	phase := st.Phase
	if st.CloseReason != "" {
		phase += " (" + st.CloseReason + ")"
	} else {
		phase += ", draft"
	}
	b.WriteString("| | |\n|---|---|\n")
	fmt.Fprintf(&b, "| Room | `%s` |\n| Phase | %s |\n| Opened | %s |\n", st.ID, phase, fmtTime(st.CreatedAt))
	if st.ClosedAt != nil {
		fmt.Fprintf(&b, "| Closed | %s |\n", fmtTime(*st.ClosedAt))
	}
	if st.CloseNote != "" {
		fmt.Fprintf(&b, "| Closing reason | %s |\n", cell(st.CloseNote))
	}
	turns := []string{}
	for _, sd := range st.Sides {
		turns = append(turns, fmt.Sprintf("%s %d", sd.Label, sd.TurnsUsed))
	}
	fmt.Fprintf(&b, "| Agent turns | %s of %d per side |\n", strings.Join(turns, ", "), st.TurnBudget)
	fmt.Fprintf(&b, "| Chain head | `%s` |\n\n", m.ChainHead)
	if d := strings.TrimSpace(st.Description); d != "" {
		fmt.Fprintf(&b, "%s\n\n", d)
	}

	b.WriteString("## Participants\n\n| Side | Name | Human joined | Agent joined |\n|---|---|---|---|\n")
	for _, sd := range st.Sides {
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", sd.Label, cell(sd.Name), fmtOptTime(sd.HumanJoinedAt), fmtOptTime(sd.AgentJoinedAt))
	}

	fmt.Fprintf(&b, "\n## Charter (version %d)\n\n", st.Charter.Version)
	if len(st.Charter.Items) == 0 {
		b.WriteString("No conditions were adopted.\n\n")
	}
	for _, it := range st.Charter.Items {
		fmt.Fprintf(&b, "%d. %s\n", it.N, strings.ReplaceAll(strings.TrimSpace(it.Text), "\n", "\n   "))
	}
	if len(st.Charter.Items) > 0 {
		b.WriteString("\n")
	}

	b.WriteString("## Decision\n\n")
	if r := m.Resolution; r == nil {
		b.WriteString("No resolution.\n\n")
	} else {
		fmt.Fprintf(&b, "Resolution #%d by %s, %s, status: %s.\n\n%s\n\n", r.Seq, name(r.Side), fmtTime(r.TS), r.Status, quote(r.Body))
	}

	b.WriteString("## Threads\n\n")
	if len(m.Threads) == 0 {
		b.WriteString("None.\n\n")
	}
	for _, t := range m.Threads {
		fmt.Fprintf(&b, "- **#%d %s, %s** (%s): %s\n", t.Seq, name(t.Side), t.Type, t.Status, t.Summary)
		for _, r := range t.Replies {
			fmt.Fprintf(&b, "  - #%d %s %s: %s\n", r.Seq, name(r.Side), verb(r.Type), r.Summary)
		}
	}
	if len(m.Threads) > 0 {
		b.WriteString("\n")
	}
	b.WriteString("## Open questions\n\n")
	if len(m.Open) == 0 {
		b.WriteString("None.\n\n")
	}
	for _, seq := range m.Open {
		for _, t := range m.Threads {
			if t.Seq == seq {
				fmt.Fprintf(&b, "- #%d (%s): %s\n", t.Seq, t.Status, t.Summary)
			}
		}
	}
	if len(m.Open) > 0 {
		b.WriteString("\n")
	}

	b.WriteString("## Timeline\n\n")
	for _, l := range m.Timeline {
		fmt.Fprintf(&b, "- #%d %s %s\n", l.Seq, l.TS.UTC().Format("15:04"), strings.ReplaceAll(l.Text, "\n", " "))
	}

	b.WriteString("\n## Messages\n\n")
	for _, e := range m.Events {
		if !e.IsMessage() {
			continue
		}
		head := fmt.Sprintf("**#%d · %s · %s, %s · %s**", e.Seq, e.TS.UTC().Format("15:04"), name(e.Side), e.Actor, e.Type)
		if e.Re != 0 {
			head += fmt.Sprintf(" re #%d", e.Re)
		}
		if e.Summary != "" {
			head += " — " + e.Summary
		}
		fmt.Fprintf(&b, "%s\n\n%s\n\n", head, quote(e.Body))
	}
	return b.String()
}

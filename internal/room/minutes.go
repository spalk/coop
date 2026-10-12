package room

import (
	"fmt"
	"strings"
	"time"
)

// Minutes is the record of a room: its status and the full event log.
type Minutes struct {
	Room   Status  `json:"room"`
	Events []Event `json:"events"`
}

// Minutes collects the record of a room.
func (s *Service) Minutes(roomID string) (Minutes, error) {
	st, err := s.Status(roomID, nil)
	if err != nil {
		return Minutes{}, err
	}
	var all []Event
	var after int64
	for {
		evs, err := s.Events(roomID, after, 1000)
		if err != nil {
			return Minutes{}, err
		}
		all = append(all, evs...)
		if len(evs) < 1000 {
			break
		}
		after = evs[len(evs)-1].Seq
	}
	return Minutes{Room: st, Events: all}, nil
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

// Markdown renders the minutes as a Markdown document.
func (m Minutes) Markdown() string {
	st := m.Room
	names := map[string]string{}
	for _, sd := range st.Sides {
		names[sd.Label] = sd.Name
	}
	who := func(side, actor string) string {
		if side == "" {
			return "Server"
		}
		s := "Side " + side
		if n := names[side]; n != "" {
			s += " (" + n + ")"
		}
		if actor != "" && actor != ActorServer {
			s += ", " + actor
		}
		return s
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Minutes: %s\n\n", st.Topic)
	state := st.State
	if st.CloseReason != "" {
		state += " (" + st.CloseReason + ")"
	}
	if st.State != StateClosed {
		state += ", draft"
	}
	fmt.Fprintf(&b, "| | |\n|---|---|\n")
	fmt.Fprintf(&b, "| Room | `%s` |\n", st.ID)
	fmt.Fprintf(&b, "| State | %s |\n", state)
	fmt.Fprintf(&b, "| Opened | %s |\n", fmtTime(st.CreatedAt))
	if st.ClosedAt != nil {
		fmt.Fprintf(&b, "| Closed | %s |\n", fmtTime(*st.ClosedAt))
	}
	fmt.Fprintf(&b, "| Agent turns | %d of %d |\n\n", st.TurnsUsed, st.TurnBudget)
	if d := strings.TrimSpace(st.Description); d != "" {
		fmt.Fprintf(&b, "%s\n\n", d)
	}

	b.WriteString("## Participants\n\n| Side | Name | Agent joined | Human joined |\n|---|---|---|---|\n")
	for _, sd := range st.Sides {
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", sd.Label, sd.Name, fmtOptTime(sd.AgentJoinedAt), fmtOptTime(sd.HumanJoinedAt))
	}

	b.WriteString("\n## Opening positions\n\n")
	for _, sd := range st.Sides {
		fmt.Fprintf(&b, "### %s\n\n", who(sd.Label, ""))
		found := false
		for _, e := range m.Events {
			if e.IsMessage() && e.Side == sd.Label && e.Actor == ActorAgent {
				fmt.Fprintf(&b, "%s\n\n", quote(e.Body))
				found = true
				break
			}
		}
		if !found {
			b.WriteString("No position stated.\n\n")
		}
	}

	b.WriteString("## Chronology\n\n")
	for _, e := range m.Events {
		t := e.TS.UTC().Format("15:04")
		if e.IsMessage() {
			head := fmt.Sprintf("**#%d · %s · %s · %s**", e.Seq, t, who(e.Side, e.Actor), e.Type)
			if e.To != "" {
				head += " to side " + e.To
			}
			fmt.Fprintf(&b, "%s\n\n%s\n\n", head, quote(e.Body))
			continue
		}
		if line := describe(e, who); line != "" {
			fmt.Fprintf(&b, "_#%d · %s · %s_\n\n", e.Seq, t, line)
		}
	}

	b.WriteString("## Decision\n\n")
	var res *Event
	for i := range m.Events {
		if m.Events[i].Seq == st.PendingResolution {
			res = &m.Events[i]
		}
	}
	if res == nil {
		b.WriteString("No resolution.\n\n")
	} else {
		status := "pending approval"
		if st.State == StateClosed && st.CloseReason == ReasonApproved {
			status = "approved by all sides"
		}
		fmt.Fprintf(&b, "Resolution #%d by %s, %s, %s.\n\n%s\n\n", res.Seq, who(res.Side, res.Actor), fmtTime(res.TS), status, quote(res.Body))
		b.WriteString("## Approvals\n\n| Side | Name | Approved |\n|---|---|---|\n")
		for _, sd := range st.Sides {
			fmt.Fprintf(&b, "| %s | %s | %s |\n", sd.Label, sd.Name, fmtOptTime(sd.ApprovedAt))
		}
	}
	return b.String()
}

func describe(e Event, who func(side, actor string) string) string {
	str := func(k string) string {
		if v, ok := e.Data[k]; ok {
			return fmt.Sprint(v)
		}
		return ""
	}
	switch e.Type {
	case EventCreated:
		return "Room created"
	case EventJoined:
		return who(e.Side, e.Actor) + " joined"
	case EventTurn:
		return "" // implied by the order of messages
	case EventPaused:
		s := "Paused by " + who(e.Side, e.Actor)
		if e.Side == "" && str("reason") == ReasonBudget {
			s = "Paused: turn budget used up"
		} else if r := str("reason"); r != "" {
			s += ": " + r
		}
		return s
	case EventResumed:
		return fmt.Sprintf("Resumed by %s, floor to side %s, budget %s turns", who(e.Side, e.Actor), str("floor"), str("turn_budget"))
	case EventApproved:
		return fmt.Sprintf("%s approved resolution #%s", who(e.Side, e.Actor), str("resolution"))
	case EventClosed:
		return "Closed: " + str("reason")
	case EventTokenReissued:
		return "Token of side " + e.Side + " reissued"
	case EventRenamed:
		return "Side " + e.Side + " renamed to " + str("name")
	}
	return e.Type
}

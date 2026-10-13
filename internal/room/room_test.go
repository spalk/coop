package room

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fixture struct {
	t      *testing.T
	s      *Service
	clock  time.Time
	id     string
	agentA Principal
	agentB Principal
	humanA Principal
	humanB Principal
	tokens map[string]string
}

func newFixture(t *testing.T, req CreateRequest) *fixture {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "coop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	f := &fixture{t: t, s: s, clock: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC), tokens: map[string]string{}}
	s.Now = func() time.Time { return f.clock }
	if req.Topic == "" {
		req.Topic = "Orders export"
	}
	c, err := s.CreateRoom("", "10.0.0.1", req)
	if err != nil {
		t.Fatal(err)
	}
	f.id = c.Status.ID
	f.tokens["humanA"], f.tokens["agentA"], f.tokens["inviteB"] = c.HumanToken, c.AgentToken, c.Invitations[0].Token
	f.agentA = f.auth(c.AgentToken, 0, ActorAgent)
	f.humanA = f.auth(c.HumanToken, 0, ActorHuman)
	hb, tokB, err := s.ClaimInvitation(c.Invitations[0].Token)
	if err != nil {
		t.Fatal(err)
	}
	f.tokens["humanB"] = tokB
	f.humanB = hb
	agentB, err := s.IssueAgentLink(hb)
	if err != nil {
		t.Fatal(err)
	}
	f.tokens["agentB"] = agentB
	f.agentB = f.auth(agentB, 1, ActorAgent)
	return f
}

func (f *fixture) auth(token string, side int, actor string) Principal {
	f.t.Helper()
	p, err := f.s.Authenticate(token)
	if err != nil {
		f.t.Fatal(err)
	}
	if p.Side != side || p.Actor != actor || p.RoomID != f.id {
		f.t.Fatalf("auth: got %+v", p)
	}
	return p
}

func ack(n int64) *int64 { return &n }

// move posts a public agent message, acknowledging everything so far.
func (f *fixture) move(p Principal, typ, body string, re int64) Event {
	f.t.Helper()
	ev, _, err := f.s.Post(p, Message{Type: typ, Body: body, Summary: body, Re: re, Ack: ack(f.status().LastSeq)})
	if err != nil {
		f.t.Fatalf("%s by %s/%s: %v", typ, p.Label(), p.Actor, err)
	}
	return ev
}

func (f *fixture) human(p Principal, typ, body string) Event {
	f.t.Helper()
	ev, _, err := f.s.Post(p, Message{Type: typ, Body: body})
	if err != nil {
		f.t.Fatalf("%s by %s/human: %v", typ, p.Label(), err)
	}
	return ev
}

func (f *fixture) status() Status {
	f.t.Helper()
	st, err := f.s.Status(f.id, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	return st
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("want error %s, got %v", code, err)
	}
}

func TestCredentialsAreSeparate(t *testing.T) {
	f := newFixture(t, CreateRequest{})
	// an invitation is not a credential and works once
	if _, err := f.s.Authenticate(f.tokens["inviteB"]); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("invitation authenticated: %v", err)
	}
	if _, _, err := f.s.ClaimInvitation(f.tokens["inviteB"]); !errors.Is(err, ErrInvite) {
		t.Fatalf("invitation reused: %v", err)
	}
	if st := f.status(); !st.Sides[1].Claimed {
		t.Fatal("side B not claimed")
	}
	// agents cannot do what humans do
	wantCode(t, f.s.Approve(f.agentA, 1), "humans_only")
	wantCode(t, f.s.CloseRoom(f.agentA, "x"), "humans_only")
	wantCode(t, f.s.SetMandate(f.agentA, "x"), "humans_only")
	wantCode(t, f.s.Vote(f.agentA, 1, true, ""), "humans_only")
	wantCode(t, f.s.Ready(f.agentA), "humans_only")
	if _, err := f.s.IssueAgentLink(f.agentA); !errors.Is(err, ErrHumansOnly) {
		t.Fatal("agent issued an agent link")
	}
	// a new agent link revokes the old one
	if _, err := f.s.IssueAgentLink(f.humanB); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Authenticate(f.tokens["agentB"]); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("old agent token still works: %v", err)
	}
	// invitations only for unclaimed sides
	_, err := f.s.IssueInvitation(f.humanA, "B")
	wantCode(t, err, "already_claimed")
}

func TestReissueInvitation(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "coop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, err := s.CreateRoom("", "ip", CreateRequest{Topic: "t"})
	if err != nil {
		t.Fatal(err)
	}
	ha, _ := s.Authenticate(c.HumanToken)
	inv, err := s.IssueInvitation(ha, "B")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Invitation(c.Invitations[0].Token); !errors.Is(err, ErrInvite) {
		t.Fatal("old invitation still valid")
	}
	if _, _, err := s.ClaimInvitation(inv); err != nil {
		t.Fatal(err)
	}
}

func TestQuotas(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "coop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.CreateRoom("", "ip", CreateRequest{Topic: "t", TurnBudget: 41}); err == nil {
		t.Fatal("anonymous room over the budget quota")
	}
	if _, err := s.CreateRoom("", "ip", CreateRequest{Topic: "t", TTLHours: 73}); err == nil {
		t.Fatal("anonymous room over the lifetime quota")
	}
	if _, err := s.CreateRoom("coophk_nope", "ip", CreateRequest{Topic: "t"}); !errors.Is(err, ErrHostKey) {
		t.Fatalf("bad host key: %v", err)
	}
	key, _ := s.AddHostKey("k")
	c, err := s.CreateRoom(key, "ip", CreateRequest{Topic: "t", TurnBudget: 200, TTLHours: 24 * 30})
	if err != nil {
		t.Fatal(err)
	}
	if c.Status.TurnBudget != 200 {
		t.Fatalf("budget %d", c.Status.TurnBudget)
	}
	if _, err := s.CreateRoom("", "ip", CreateRequest{Topic: "t"}); err != nil {
		t.Fatal(err)
	}
	n, err := s.AnonymousRoomsSince("ip", time.Now().Add(-time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("anonymous count %d %v", n, err)
	}
}

func TestFloorAndSummary(t *testing.T) {
	f := newFixture(t, CreateRequest{})
	if st := f.status(); st.Phase != PhaseNegotiation || st.Floor != "A" {
		t.Fatalf("initial: %+v", st)
	}
	last := f.status().LastSeq
	_, _, err := f.s.Post(f.agentB, Message{Type: TypeSay, Body: "hi", Summary: "hi", Ack: ack(last)})
	wantCode(t, err, "not_your_turn")
	_, _, err = f.s.Post(f.agentA, Message{Type: TypeSay, Body: "hi", Ack: ack(last)})
	wantCode(t, err, "bad_request") // summary required
	_, _, err = f.s.Post(f.agentA, Message{Type: TypeSay, Body: "hi", Summary: "hi"})
	wantCode(t, err, "bad_request") // ack required
	_, _, err = f.s.Post(f.agentA, Message{Type: TypeAgree, Body: "ok", Summary: "ok", Ack: ack(last)})
	wantCode(t, err, "bad_request") // agree needs re
	_, _, err = f.s.Post(f.agentA, Message{Type: TypeSay, Body: "x", Summary: strings.Repeat("s", MaxSummary+1), Ack: ack(last)})
	wantCode(t, err, "bad_request")

	f.move(f.agentA, TypePropose, "paginated endpoint", 0)
	if st := f.status(); st.Floor != "B" || st.Sides[0].TurnsUsed != 1 {
		t.Fatalf("after A: %+v", st)
	}
}

func TestHumanMessagesMustBeRead(t *testing.T) {
	f := newFixture(t, CreateRequest{})
	before := f.status().LastSeq
	f.human(f.humanB, TypeSay, "Do not discuss pricing.")
	_, _, err := f.s.Post(f.agentA, Message{Type: TypeSay, Body: "x", Summary: "x", Ack: ack(before)})
	var e *Error
	if !errors.As(err, &e) || e.Code != "unread_human_message" || len(e.Unread) != 1 || e.Unread[0].Body != "Do not discuss pricing." {
		t.Fatalf("unread: %v", err)
	}
	f.move(f.agentA, TypeSay, "noted", 0)
	// a private message of side A is invisible to B, so B need not acknowledge it
	before = f.status().LastSeq
	if _, _, err := f.s.Post(f.humanA, Message{Type: TypeSay, Body: "keep it short", Private: true}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.Post(f.agentB, Message{Type: TypeSay, Body: "ok", Summary: "ok", Ack: ack(before)}); err != nil {
		t.Fatalf("private message of A blocked B: %v", err)
	}
	evs, _ := f.s.Events(f.id, 1, 0, 0)
	for _, e := range evs {
		if strings.Contains(e.Body, "keep it short") {
			t.Fatal("B sees A's private message")
		}
	}
	evs, _ = f.s.Events(f.id, 0, 0, 0)
	found := false
	for _, e := range evs {
		found = found || e.Body == "keep it short" && e.Private
	}
	if !found {
		t.Fatal("A does not see its private message")
	}
}

func TestPrivateAgentChannel(t *testing.T) {
	f := newFixture(t, CreateRequest{})
	if _, _, err := f.s.Post(f.agentB, Message{Type: TypeAsk, Body: "May I share the schema?", Private: true,
		Ack: ack(f.status().LastSeq)}); err != nil {
		t.Fatal(err)
	}
	if st := f.status(); st.Sides[1].TurnsUsed != 0 || st.Floor != "A" {
		t.Fatalf("private message used the floor: %+v", st)
	}
}

func TestMandate(t *testing.T) {
	f := newFixture(t, CreateRequest{})
	if err := f.s.SetMandate(f.humanA, "Never disclose the customer list."); err != nil {
		t.Fatal(err)
	}
	st, _ := f.s.Status(f.id, &f.agentA)
	if st.You.Mandate != "Never disclose the customer list." {
		t.Fatalf("mandate: %+v", st.You)
	}
	stB, _ := f.s.Status(f.id, &f.agentB)
	if stB.You.Mandate != "" {
		t.Fatal("B sees A's mandate")
	}
	_, _, err := f.s.Post(f.agentA, Message{Type: TypeSay, Body: "x", Summary: "x", Ack: ack(st.LastSeq - 1)})
	wantCode(t, err, "unread_human_message")
	if m, _ := f.s.Minutes(f.id); strings.Contains(m.Markdown(), "customer list") {
		t.Fatal("mandate in the minutes")
	}
}

func TestAmendmentCharterAndVoiding(t *testing.T) {
	f := newFixture(t, CreateRequest{})
	res := f.move(f.agentA, TypeResolution, "GET /orders, 500 per page", 0)
	a := f.human(f.humanB, TypeAmendment, "Only orders after 2026-01-01.")
	st := f.status()
	if st.Pause == nil || st.Pause.Kind != PauseAmendment || st.Amendment.Votes["B"] != "accept" {
		t.Fatalf("pending amendment: %+v %+v", st.Pause, st.Amendment)
	}
	_, _, err := f.s.Post(f.agentB, Message{Type: TypeAgree, Body: "ok", Summary: "ok", Re: res.Seq, Ack: ack(st.LastSeq)})
	wantCode(t, err, "paused")
	_, _, err = f.s.Post(f.humanA, Message{Type: TypeAmendment, Body: "another"})
	wantCode(t, err, "amendment_pending")
	wantCode(t, f.s.Vote(f.humanB, a.Seq, true, ""), "already_voted")
	if err := f.s.Vote(f.humanA, a.Seq, true, "fine"); err != nil {
		t.Fatal(err)
	}
	st = f.status()
	if st.Charter.Version != 1 || len(st.Charter.Items) != 1 || st.Charter.Items[0].Text != "Only orders after 2026-01-01." {
		t.Fatalf("charter: %+v", st.Charter)
	}
	if st.Resolution != nil || st.Pause != nil {
		t.Fatalf("resolution not voided or still paused: %+v %+v", st.Resolution, st.Pause)
	}
	a2 := f.human(f.humanA, TypeAmendment, "Only orders after 2026-06-01.")
	if err := f.s.Vote(f.humanB, a2.Seq, false, "too late"); err != nil {
		t.Fatal(err)
	}
	if st := f.status(); st.Charter.Version != 1 || st.Amendment != nil {
		t.Fatalf("after reject: %+v", st)
	}
	ev, _, err := f.s.Post(f.humanA, Message{Type: TypeAmendment, Body: "Only orders after 2026-03-01.", Replaces: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.Vote(f.humanB, ev.Seq, true, ""); err != nil {
		t.Fatal(err)
	}
	st = f.status()
	if st.Charter.Version != 2 || len(st.Charter.Items) != 1 || st.Charter.Items[0].Text != "Only orders after 2026-03-01." {
		t.Fatalf("replaced charter: %+v", st.Charter)
	}
}

func TestResolutionAgreementApproval(t *testing.T) {
	f := newFixture(t, CreateRequest{})
	f.move(f.agentA, TypeResolution, "v1", 0)
	f.move(f.agentB, TypeAsk, "which fields?", 0) // declines v1
	if st := f.status(); st.Resolution != nil {
		t.Fatal("resolution not declined")
	}
	f.move(f.agentA, TypeSay, "id, total", 0)
	r2 := f.move(f.agentB, TypeResolution, "v2", 0)
	wantCode(t, f.s.Approve(f.humanA, r2.Seq), "wrong_phase")
	f.move(f.agentA, TypeAgree, "agreed", r2.Seq)
	st := f.status()
	if st.Phase != PhaseApproval || st.Resolution.Seq != r2.Seq || len(st.Resolution.AgreedBy) != 2 {
		t.Fatalf("approval: %+v", st)
	}
	_, _, err := f.s.Post(f.agentA, Message{Type: TypeSay, Body: "x", Summary: "x", Ack: ack(st.LastSeq)})
	wantCode(t, err, "wrong_phase")

	wantCode(t, f.s.Reject(f.humanA, r2.Seq, ""), "bad_request")
	if err := f.s.Reject(f.humanA, r2.Seq, "add the currency field"); err != nil {
		t.Fatal(err)
	}
	st = f.status()
	if st.Phase != PhaseNegotiation || st.Floor != "B" || st.Resolution != nil {
		t.Fatalf("after reject: %+v", st)
	}
	_, _, err = f.s.Post(f.agentB, Message{Type: TypeSay, Body: "x", Summary: "x", Ack: ack(st.LastSeq - 3)})
	wantCode(t, err, "unread_human_message")

	r3 := f.move(f.agentB, TypeResolution, "v3 with currency", 0)
	f.move(f.agentA, TypeAgree, "agreed", r3.Seq)
	if err := f.s.Approve(f.humanA, r3.Seq); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Approve(f.humanB, r3.Seq); err != nil {
		t.Fatal(err)
	}
	st = f.status()
	if st.Phase != PhaseClosed || st.CloseReason != ReasonApproved {
		t.Fatalf("closed: %+v", st)
	}
	m, err := f.s.Minutes(f.id)
	if err != nil {
		t.Fatal(err)
	}
	if m.Resolution == nil || m.Resolution.Seq != r3.Seq || m.Resolution.Status != "approved" {
		t.Fatalf("minutes resolution: %+v", m.Resolution)
	}
	if !strings.Contains(m.Markdown(), "v3 with currency") {
		t.Fatal("minutes lack the resolution")
	}
}

func TestBriefing(t *testing.T) {
	f := newFixture(t, CreateRequest{Briefing: true})
	st := f.status()
	if st.Phase != PhaseBriefing || st.Floor != "" {
		t.Fatalf("briefing: %+v", st)
	}
	f.human(f.humanA, TypeSay, "Let's agree on scope first.")
	_, _, err := f.s.Post(f.agentA, Message{Type: TypeSay, Body: "x", Summary: "x", Ack: ack(f.status().LastSeq)})
	wantCode(t, err, "wrong_phase")
	a := f.human(f.humanB, TypeAmendment, "Scope: orders only, no customers.")
	if err := f.s.Vote(f.humanA, a.Seq, true, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Ready(f.humanA); err != nil {
		t.Fatal(err)
	}
	if st := f.status(); st.Phase != PhaseBriefing {
		t.Fatal("negotiation started with one side ready")
	}
	// A withdraws its start: B alone does not start the agents.
	wantCode(t, f.s.Unready(f.agentA), "humans_only")
	if err := f.s.Unready(f.humanA); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Ready(f.humanB); err != nil {
		t.Fatal(err)
	}
	if st := f.status(); st.Phase != PhaseBriefing {
		t.Fatal("negotiation started after A withdrew")
	}
	if err := f.s.Ready(f.humanA); err != nil {
		t.Fatal(err)
	}
	st = f.status()
	if st.Phase != PhaseNegotiation || st.Floor != "A" || st.Charter.Version != 1 {
		t.Fatalf("after ready: %+v", st)
	}
	wantCode(t, f.s.Unready(f.humanA), "wrong_phase")
}

func TestPauseResumeBudgetPerSide(t *testing.T) {
	f := newFixture(t, CreateRequest{TurnBudget: 2})
	f.move(f.agentA, TypeSay, "1", 0)
	if err := f.s.Raise(f.agentB, "need my human"); err != nil {
		t.Fatal(err)
	}
	_, _, err := f.s.Post(f.agentB, Message{Type: TypeSay, Body: "x", Summary: "x", Ack: ack(f.status().LastSeq)})
	wantCode(t, err, "paused")
	wantCode(t, f.s.Resume(f.humanA, "", 0), "forbidden")
	if err := f.s.Resume(f.humanB, "B", 0); err != nil {
		t.Fatal(err)
	}
	f.move(f.agentB, TypeSay, "2", 0)
	f.move(f.agentA, TypeSay, "3", 0) // A's second turn: its budget is used
	st := f.status()
	if st.Pause == nil || st.Pause.Kind != PauseBudget {
		t.Fatalf("budget: %+v", st.Pause)
	}
	wantCode(t, f.s.Resume(f.humanB, "", 0), "budget_exhausted")
	if err := f.s.Resume(f.humanB, "", 3); err != nil {
		t.Fatal(err)
	}
	if st := f.status(); st.TurnBudget != 5 || st.Pause != nil || st.Floor != "B" {
		t.Fatalf("after resume: %+v", st)
	}
}

func TestCloseByHuman(t *testing.T) {
	f := newFixture(t, CreateRequest{})
	wantCode(t, f.s.CloseRoom(f.humanB, " "), "bad_request")
	if err := f.s.CloseRoom(f.humanB, "Partner went silent."); err != nil {
		t.Fatal(err)
	}
	st := f.status()
	if st.Phase != PhaseClosed || st.CloseReason != "closed_by_B" || st.CloseNote != "Partner went silent." {
		t.Fatalf("closed: %+v", st)
	}
	_, _, err := f.s.Post(f.humanA, Message{Type: TypeSay, Body: "late"})
	wantCode(t, err, "room_closed")
}

func TestExpiryAndPurge(t *testing.T) {
	f := newFixture(t, CreateRequest{TTLHours: 1})
	f.clock = f.clock.Add(2 * time.Hour)
	_, _, err := f.s.Post(f.humanA, Message{Type: TypeSay, Body: "late"})
	wantCode(t, err, "room_closed")
	if st := f.status(); st.CloseReason != ReasonExpired {
		t.Fatalf("expired: %+v", st)
	}
	f.clock = f.clock.Add(25 * time.Hour)
	if n, err := f.s.Purge(24 * time.Hour); err != nil || n != 1 {
		t.Fatalf("purge: %d %v", n, err)
	}
}

func TestHashChain(t *testing.T) {
	f := newFixture(t, CreateRequest{})
	f.move(f.agentA, TypePropose, "p", 0)
	f.s.Post(f.humanA, Message{Type: TypeSay, Body: "private", Private: true})
	f.human(f.humanB, TypeSay, "public")
	evs, _ := f.s.Events(f.id, -1, 0, 0)
	prev := ""
	for _, e := range evs {
		data := ""
		if len(e.Data) > 0 {
			b, _ := json.Marshal(e.Data)
			data = string(b)
		}
		if got := chainHash(prev, e, data); got != e.Hash {
			t.Fatalf("chain broken at #%d", e.Seq)
		}
		prev = e.Hash
	}
}

func TestProtocolThreads(t *testing.T) {
	f := newFixture(t, CreateRequest{})
	p1 := f.move(f.agentA, TypePropose, "paginated endpoint", 0)
	f.move(f.agentB, TypeAgree, "ok to pagination", p1.Seq)
	q := f.move(f.agentA, TypeAsk, "is customer email needed?", 0)
	f.move(f.agentB, TypeDefer, "later", q.Seq)
	f.human(f.humanA, TypeSay, "Use UTC everywhere.")
	p, _, err := f.s.Protocol(f.id, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Threads) != 2 || p.Threads[0].Status != ThreadAccepted || p.Threads[1].Status != ThreadDeferred {
		t.Fatalf("threads: %+v", p.Threads)
	}
	if len(p.Open) != 1 || p.Open[0] != q.Seq {
		t.Fatalf("open questions: %v", p.Open)
	}
	found := false
	for _, l := range p.Timeline {
		found = found || l.Kind == "human" && strings.Contains(l.Text, "UTC")
	}
	if !found {
		t.Fatal("human message missing from the timeline")
	}
}

func TestPublicRooms(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "coop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	type side = struct {
		Name string `json:"name"`
	}
	s.CreateRoom("", "ip", CreateRequest{Topic: "secret topic", Sides: []side{{"Ann"}, {"Bob"}}})
	s.CreateRoom("", "ip", CreateRequest{Topic: "open topic", Public: true, Sides: []side{{"Cid"}, {"Dan"}}})
	list, open, err := s.PublicRooms(10)
	if err != nil || open != 2 || len(list) != 2 {
		t.Fatalf("%v %d %d", err, open, len(list))
	}
	shown := false
	for _, r := range list {
		if r.Topic == "secret topic" || strings.Contains(strings.Join(r.Names, ","), "Ann") {
			t.Fatal("private room identified")
		}
		shown = shown || r.Topic == "open topic" && strings.Join(r.Names, ",") == "Cid,Dan"
	}
	if !shown {
		t.Fatal("public room not shown")
	}
}

func TestUpgradeDropsV1Rooms(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coop.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := s.AddHostKey("keep me")
	s.db.Exec(`PRAGMA user_version = 1`)
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.CreateRoom(key, "ip", CreateRequest{Topic: "t"}); err != nil {
		t.Fatalf("host key lost in upgrade: %v", err)
	}
}

package room

import (
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
	tokens []string
	agentA Principal
	agentB Principal
	humanA Principal
	humanB Principal
}

func newFixture(t *testing.T, req CreateRequest) *fixture {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "coop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	f := &fixture{t: t, s: s, clock: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	s.Now = func() time.Time { return f.clock }
	key, err := s.AddHostKey("test")
	if err != nil {
		t.Fatal(err)
	}
	if req.Topic == "" {
		req.Topic = "Orders export"
	}
	c, err := s.CreateRoom(key, req)
	if err != nil {
		t.Fatal(err)
	}
	f.id, f.tokens = c.Status.ID, c.Tokens
	f.agentA = f.auth(0)
	f.agentB = f.auth(1)
	f.humanA = f.session(0)
	f.humanB = f.session(1)
	return f
}

func (f *fixture) auth(side int) Principal {
	f.t.Helper()
	p, err := f.s.Authenticate(f.tokens[side])
	if err != nil {
		f.t.Fatal(err)
	}
	if p.Side != side || p.Actor != ActorAgent || p.RoomID != f.id {
		f.t.Fatalf("auth: got %+v", p)
	}
	return p
}

func (f *fixture) session(side int) Principal {
	f.t.Helper()
	p, secret, err := f.s.OpenSession(f.tokens[side])
	if err != nil {
		f.t.Fatal(err)
	}
	q, err := f.s.AuthenticateSession(f.id, secret)
	if err != nil || q != p || q.Actor != ActorHuman {
		f.t.Fatalf("session: %+v %+v %v", p, q, err)
	}
	return p
}

func (f *fixture) post(p Principal, typ, body string) Event {
	f.t.Helper()
	ev, _, err := f.s.Post(p, Message{Type: typ, Body: body})
	if err != nil {
		f.t.Fatalf("post %s by %s/%s: %v", typ, p.Label(), p.Actor, err)
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

func TestFloorPassesBetweenAgents(t *testing.T) {
	f := newFixture(t, CreateRequest{})
	if st := f.status(); st.State != StateOpen || st.Floor != "A" {
		t.Fatalf("initial: %+v", st)
	}
	_, _, err := f.s.Post(f.agentB, Message{Type: TypeSay, Body: "hello"})
	wantCode(t, err, "not_your_turn")

	f.post(f.agentA, TypePropose, "opening position")
	if st := f.status(); st.Floor != "B" || st.TurnsUsed != 1 {
		t.Fatalf("after A: %+v", st)
	}
	_, _, err = f.s.Post(f.agentA, Message{Type: TypeSay, Body: "again"})
	wantCode(t, err, "not_your_turn")

	f.post(f.agentB, TypeAsk, "question")
	if st := f.status(); st.Floor != "A" {
		t.Fatalf("after B: %+v", st)
	}

	// keep: the sender holds the floor
	if _, _, err := f.s.Post(f.agentA, Message{Type: TypeSay, Body: "part 1", Floor: "keep"}); err != nil {
		t.Fatal(err)
	}
	if st := f.status(); st.Floor != "A" || st.TurnsUsed != 3 {
		t.Fatalf("after keep: %+v", st)
	}
	_, _, err = f.s.Post(f.agentA, Message{Type: TypeSay, Body: "self", To: "A"})
	wantCode(t, err, "bad_request")
}

func TestHumansWriteAnyTimeWithoutUsingFloor(t *testing.T) {
	f := newFixture(t, CreateRequest{})
	f.post(f.humanB, TypeSay, "B human speaks out of turn")
	f.post(f.humanA, TypeSay, "A human too")
	st := f.status()
	if st.Floor != "A" || st.TurnsUsed != 0 {
		t.Fatalf("humans moved floor or used budget: %+v", st)
	}
	_, _, err := f.s.Post(f.humanA, Message{Type: TypeAgree, Body: "ok"})
	wantCode(t, err, "bad_request")
	_, _, err = f.s.Post(f.humanA, Message{Type: TypeSay, Body: "x", Floor: "keep"})
	wantCode(t, err, "bad_request")
}

func TestMessageValidation(t *testing.T) {
	f := newFixture(t, CreateRequest{})
	_, _, err := f.s.Post(f.agentA, Message{Type: "shout", Body: "x"})
	wantCode(t, err, "bad_request")
	_, _, err = f.s.Post(f.agentA, Message{Type: TypeSay, Body: "  "})
	wantCode(t, err, "bad_request")
	_, _, err = f.s.Post(f.agentA, Message{Type: TypeSay, Body: strings.Repeat("x", MaxBody+1)})
	wantCode(t, err, "too_large")
	_, _, err = f.s.Post(f.agentA, Message{Type: TypeSay, Body: "x", To: "Q"})
	wantCode(t, err, "bad_request")
}

func TestPauseAndResume(t *testing.T) {
	f := newFixture(t, CreateRequest{})
	f.post(f.agentA, TypeSay, "one")
	if err := f.s.Raise(f.humanB, "wait, wrong table"); err != nil {
		t.Fatal(err)
	}
	st := f.status()
	if st.State != StatePaused || st.Pause == nil || st.Pause.Side != "B" || st.Pause.Actor != ActorHuman {
		t.Fatalf("paused: %+v", st)
	}
	_, _, err := f.s.Post(f.agentB, Message{Type: TypeSay, Body: "x"})
	wantCode(t, err, "paused")
	wantCode(t, f.s.Raise(f.humanA, ""), "already_paused")
	f.post(f.humanB, TypeSay, "use the orders_v2 table")

	wantCode(t, f.s.Resume(f.agentB, "", 0), "humans_only")
	wantCode(t, f.s.Resume(f.humanA, "", 0), "forbidden")
	if err := f.s.Resume(f.humanB, "A", 0); err != nil {
		t.Fatal(err)
	}
	st = f.status()
	if st.State != StateOpen || st.Floor != "A" || st.Pause != nil {
		t.Fatalf("resumed: %+v", st)
	}
	wantCode(t, f.s.Resume(f.humanB, "", 0), "not_paused")
}

func TestAgentEscalation(t *testing.T) {
	f := newFixture(t, CreateRequest{})
	// an agent may raise a hand without holding the floor
	if err := f.s.Raise(f.agentB, "need a decision on pricing"); err != nil {
		t.Fatal(err)
	}
	if st := f.status(); st.Pause.Side != "B" || st.Pause.Actor != ActorAgent {
		t.Fatalf("%+v", st.Pause)
	}
	wantCode(t, f.s.Resume(f.humanA, "", 0), "forbidden")
	if err := f.s.Resume(f.humanB, "", 0); err != nil {
		t.Fatal(err)
	}
	if st := f.status(); st.Floor != "A" {
		t.Fatalf("floor should return to A, got %+v", st)
	}
}

func TestBudgetPausesRoom(t *testing.T) {
	f := newFixture(t, CreateRequest{TurnBudget: 3})
	f.post(f.agentA, TypeSay, "1")
	f.post(f.agentB, TypeSay, "2")
	f.post(f.agentA, TypeSay, "3")
	st := f.status()
	if st.State != StatePaused || st.Pause.Reason != ReasonBudget || st.Pause.Side != "" {
		t.Fatalf("budget: %+v", st)
	}
	_, _, err := f.s.Post(f.agentB, Message{Type: TypeSay, Body: "4"})
	wantCode(t, err, "paused")
	wantCode(t, f.s.Resume(f.humanA, "", 0), "budget_exhausted")
	// a server pause can be resumed by any side's human
	if err := f.s.Resume(f.humanA, "", 2); err != nil {
		t.Fatal(err)
	}
	st = f.status()
	if st.State != StateOpen || st.TurnBudget != 5 || st.Floor != "B" {
		t.Fatalf("after add_turns: %+v", st)
	}
}

func TestResolutionApproval(t *testing.T) {
	f := newFixture(t, CreateRequest{})
	f.post(f.agentA, TypePropose, "paginated endpoint")
	first := f.post(f.agentB, TypeResolution, "GET /orders?since=, 500 per page")
	if st := f.status(); st.PendingResolution != first.Seq {
		t.Fatalf("pending: %+v", st)
	}
	wantCode(t, f.s.Approve(f.agentA, first.Seq), "humans_only")
	if err := f.s.Approve(f.humanA, first.Seq); err != nil {
		t.Fatal(err)
	}
	// approving twice is a no-op
	if err := f.s.Approve(f.humanA, first.Seq); err != nil {
		t.Fatal(err)
	}

	// a new resolution replaces the pending one and resets approvals
	second := f.post(f.humanB, TypeResolution, "GET /orders?since=, 1000 per page")
	wantCode(t, f.s.Approve(f.humanB, first.Seq), "stale_resolution")
	if err := f.s.Approve(f.humanB, second.Seq); err != nil {
		t.Fatal(err)
	}
	if st := f.status(); st.State != StateOpen || st.Sides[0].ApprovedAt != nil || st.Sides[1].ApprovedAt == nil {
		t.Fatalf("after B approval: %+v", st)
	}
	if err := f.s.Approve(f.humanA, second.Seq); err != nil {
		t.Fatal(err)
	}
	st := f.status()
	if st.State != StateClosed || st.CloseReason != ReasonApproved || st.ClosedAt == nil {
		t.Fatalf("closed: %+v", st)
	}
	_, _, err := f.s.Post(f.humanA, Message{Type: TypeSay, Body: "late"})
	wantCode(t, err, "room_closed")
	wantCode(t, f.s.Raise(f.humanA, ""), "room_closed")

	md, err := f.s.Minutes(f.id)
	if err != nil {
		t.Fatal(err)
	}
	text := md.Markdown()
	for _, want := range []string{"# Minutes: Orders export", "1000 per page", "approved by all sides", "## Approvals"} {
		if !strings.Contains(text, want) {
			t.Errorf("minutes lack %q:\n%s", want, text)
		}
	}
}

func TestApproveWithoutResolution(t *testing.T) {
	f := newFixture(t, CreateRequest{})
	wantCode(t, f.s.Approve(f.humanA, 1), "no_resolution")
}

func TestTokens(t *testing.T) {
	f := newFixture(t, CreateRequest{Sides: []struct {
		Name string `json:"name"`
	}{{"Dmitry"}, {"Vasya"}}})
	if st := f.status(); st.Sides[0].Name != "Dmitry" || st.Sides[1].Name != "Vasya" {
		t.Fatalf("names: %+v", st.Sides)
	}
	if _, err := f.s.Authenticate("coop_nope"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("bad token: %v", err)
	}
	if _, err := f.s.CreateRoom("coophk_nope", CreateRequest{Topic: "x"}); !errors.Is(err, ErrHostKey) {
		t.Fatalf("bad host key: %v", err)
	}

	// only side A may reissue
	_, err := f.s.ReissueToken(f.agentB, "B")
	wantCode(t, err, "forbidden")

	_, secretB, err := f.s.OpenSession(f.tokens[1])
	if err != nil {
		t.Fatal(err)
	}
	newB, err := f.s.ReissueToken(f.humanA, "B")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Authenticate(f.tokens[1]); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("old token still works: %v", err)
	}
	if _, err := f.s.AuthenticateSession(f.id, secretB); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("old session still works: %v", err)
	}
	p, err := f.s.Authenticate(newB)
	if err != nil || p.Side != 1 {
		t.Fatalf("new token: %+v %v", p, err)
	}
	// a session is bound to its room
	_, secretA, _ := f.s.OpenSession(f.tokens[0])
	if _, err := f.s.AuthenticateSession("otherroom", secretA); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("session used in another room: %v", err)
	}
	// host key revocation
	keys, _ := f.s.ListHostKeys()
	if err := f.s.RevokeHostKey(keys[0].ID); err != nil {
		t.Fatal(err)
	}
}

func TestJoinedRecordedOnce(t *testing.T) {
	f := newFixture(t, CreateRequest{})
	for i := 0; i < 3; i++ {
		if err := f.s.MarkJoined(f.agentA); err != nil {
			t.Fatal(err)
		}
	}
	evs, _ := f.s.Events(f.id, 0, 0)
	joins := 0
	for _, e := range evs {
		if e.Type == EventJoined && e.Side == "A" && e.Actor == ActorAgent {
			joins++
		}
	}
	if joins != 1 {
		t.Fatalf("joined events for agent A: %d", joins)
	}
}

func TestExpiryAndPurge(t *testing.T) {
	f := newFixture(t, CreateRequest{TTLHours: 1})
	f.clock = f.clock.Add(2 * time.Hour)
	_, _, err := f.s.Post(f.agentA, Message{Type: TypeSay, Body: "late"})
	wantCode(t, err, "room_closed")
	st := f.status()
	if st.State != StateClosed || st.CloseReason != ReasonExpired {
		t.Fatalf("expired: %+v", st)
	}
	n, err := f.s.Purge(24 * time.Hour)
	if err != nil || n != 0 {
		t.Fatalf("purge too early: %d %v", n, err)
	}
	f.clock = f.clock.Add(25 * time.Hour)
	n, err = f.s.Purge(24 * time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("purge: %d %v", n, err)
	}
	if _, err := f.s.Status(f.id, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("room survived purge: %v", err)
	}
}

func TestEventsAndHub(t *testing.T) {
	f := newFixture(t, CreateRequest{})
	st := f.status()
	ch := f.s.Hub().Wait(f.id)
	f.post(f.agentA, TypeSay, "ping")
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("hub did not notify")
	}
	evs, err := f.s.Events(f.id, st.LastSeq, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].Type != TypeSay || evs[1].Type != EventTurn || evs[1].To != "B" {
		t.Fatalf("events: %+v", evs)
	}
}

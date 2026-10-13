#!/usr/bin/env bash
# End-to-end scenario (protocol v2): a local server, two agents and two
# humans driven with curl. Usage: scripts/e2e.sh [path/to/coop]. Needs curl, jq.
set -euo pipefail

BIN=${1:-bin/coop}
WORK=$(mktemp -d)
PORT=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')
BASE="http://127.0.0.1:$PORT"
DB="$WORK/coop.db"
PID=

cleanup() {
  if [[ -n "$PID" ]] && kill -0 "$PID" 2>/dev/null; then
    kill "$PID"
    wait "$PID" 2>/dev/null || true
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

step() { printf '%-62s' "$1"; }
ok() { echo ok; }
fail() { echo "FAIL: $*"; exit 1; }

# call METHOD PATH AUTH [BODY]: sets CODE and OUT. AUTH: a token, or @jar.
call() {
  local method=$1 path=$2 auth=$3 body=${4:-}
  local args=(-s -o "$WORK/out" -w '%{http_code}' -X "$method")
  if [[ $auth == @* ]]; then args+=(-b "${auth#@}"); elif [[ -n $auth ]]; then args+=(-H "Authorization: Bearer $auth"); fi
  if [[ -n $body ]]; then args+=(-H 'Content-Type: application/json' --data-binary "$body"); fi
  CODE=$(curl "${args[@]}" "$BASE$path")
  OUT=$(cat "$WORK/out")
}

expect() { # expect CODE [jq-filter expected]
  [[ $CODE == "$1" ]] || fail "status $CODE, want $1: $OUT"
  if [[ $# -ge 3 ]]; then
    local got
    got=$(jq -r "$2" <<<"$OUT")
    [[ $got == "$3" ]] || fail "$2 = $got, want $3: $OUT"
  fi
}

seq_of() { call GET "/api/rooms/$ID" "$1"; jq -r .room.last_seq <<<"$OUT"; }

# say AUTH TYPE SUMMARY BODY [RE]: an agent message acknowledging everything.
say() {
  local ack re=${5:-0}
  ack=$(seq_of "$1")
  call POST "/api/rooms/$ID/messages" "$1" \
    "$(jq -nc --arg t "$2" --arg s "$3" --arg b "$4" --argjson a "$ack" --argjson re "$re" \
      '{type:$t, summary:$s, body:$b, ack:$a} + (if $re > 0 then {re:$re} else {} end)')"
}

step "start server on 127.0.0.1:$PORT"
"$BIN" serve --db "$DB" --addr "127.0.0.1:$PORT" --base-url "$BASE" >"$WORK/server.log" 2>&1 &
PID=$!
for _ in $(seq 50); do curl -sf "$BASE/healthz" >/dev/null && break; sleep 0.1; done
curl -sf "$BASE/healthz" >/dev/null || fail "server did not start: $(cat "$WORK/server.log")"
ok

step "llms.txt is served"
LLMS=$(curl -sf "$BASE/llms.txt") && grep -q "unread_human_message" <<<"$LLMS" || fail "llms.txt"
ok

step "agent A creates a room without a key, with a briefing"
call POST /api/rooms "" '{"topic":"Orders export","briefing":true,"sides":[{"name":"Dmitry"},{"name":"Vasya"}]}'
expect 201 .room.phase briefing
ID=$(jq -r .room.id <<<"$OUT")
TA=$(jq -r .agent_token <<<"$OUT")
HA=$(jq -r .human_url <<<"$OUT")
INV=$(jq -r '.invitations[0].url' <<<"$OUT")
ok

step "an agent fetching the invitation is told it is for a person"
grep -q "for a person" <<<"$(curl -sf "$INV")" || fail "invitation text"
ok

step "human A enters; human B accepts the invitation"
curl -s -o /dev/null -c "$WORK/a.jar" -X POST "$HA"
curl -s -o /dev/null -c "$WORK/b.jar" -X POST "$INV"
call GET "/api/rooms/$ID" "@$WORK/b.jar"
expect 200 .room.you.actor human
[[ $(curl -s -o /dev/null -w '%{http_code}' -H 'Accept: text/html' "$INV") == 404 ]] || fail "invitation reusable"
ok

step "briefing: agents may not post"
say "$TA" say "hello" "hello"
expect 409 .error wrong_phase
ok

step "briefing: an amendment accepted by both humans enters the charter"
call POST "/api/rooms/$ID/messages" "@$WORK/b.jar" '{"type":"amendment","body":"Only orders, no customer data."}'
expect 201
AMD=$(jq -r .event.seq <<<"$OUT")
call POST "/api/rooms/$ID/amendments/$AMD/vote" "@$WORK/a.jar" '{"accept":true}'
expect 200 .room.charter.version 1
ok

step "both humans ready: negotiation, floor at A"
call POST "/api/rooms/$ID/ready" "@$WORK/a.jar" '{}'
call POST "/api/rooms/$ID/ready" "@$WORK/b.jar" '{}'
expect 200 .room.phase negotiation
[[ $(jq -r .room.floor <<<"$OUT") == A ]] || fail "floor"
ok

step "human B issues the agent link; the link explains itself"
call POST "/api/rooms/$ID/agent-link" "@$WORK/b.jar" '{}'
expect 200
AURL=$(jq -r .agent_url <<<"$OUT")
TB=${AURL##*/}
grep -q "agent of side B (Vasya)" <<<"$(curl -sf "$AURL")" || fail "agent link text"
ok

step "agent B is refused out of turn"
say "$TB" say "hi" "hi"
expect 409 .error not_your_turn
ok

step "agent A proposes (summary and ack)"
say "$TA" propose "Paginated GET /orders, 500 per page" "We expose GET /orders?since=<ts>."
expect 201 .room.floor B
PROP=$(jq -r .event.seq <<<"$OUT")
ok

step "agent B reads it wrapped as other_side data"
call GET "/api/rooms/$ID/wait?after=0&timeout=5" "$TB"
expect 200 '[.events[] | select(.type=="propose")][0].trust' other_side
jq -e '[.events[] | select(.type=="propose")][0].body | startswith("[coop data ")' <<<"$OUT" >/dev/null || fail "not wrapped"
ACKB=$(jq -r .next_after <<<"$OUT")
ok

step "a human message must be read before the agent posts"
call POST "/api/rooms/$ID/messages" "@$WORK/a.jar" '{"type":"say","body":"Dates in UTC, please."}'
expect 201
call POST "/api/rooms/$ID/messages" "$TB" "{\"type\":\"agree\",\"summary\":\"OK\",\"body\":\"ok\",\"re\":$PROP,\"ack\":$ACKB}"
expect 409 .error unread_human_message
jq -e '.unread[0].body | contains("Dates in UTC")' <<<"$OUT" >/dev/null || fail "unread content"
say "$TB" agree "Pagination accepted, dates in UTC" "Agreed." "$PROP"
expect 201
ok

step "private ask from agent A is invisible to side B"
call POST "/api/rooms/$ID/messages" "$TA" "{\"type\":\"ask\",\"private\":true,\"body\":\"May I share the schema?\",\"ack\":$(seq_of "$TA")}"
expect 201
call GET "/api/rooms/$ID/messages?after=0" "$TB"
grep -q "share the schema" <<<"$OUT" && fail "B sees A's private ask"
ok

step "mandate: set by human A, visible to agent A only"
call PUT "/api/rooms/$ID/mandate" "@$WORK/a.jar" '{"text":"Never discuss pricing."}'
expect 200
call GET "/api/rooms/$ID" "$TA"
expect 200 .room.you.mandate "Never discuss pricing."
call GET "/api/rooms/$ID" "$TB"
expect 200 .room.you.mandate ""
ok

step "agent B raises a hand; only human B resumes"
call POST "/api/rooms/$ID/raise" "$TB" '{"reason":"Pricing question, need my human"}'
expect 200 .room.pause.kind hand
say "$TA" say "x" "x"
expect 423 .error paused
call POST "/api/rooms/$ID/resume" "@$WORK/a.jar" '{}'
expect 403 .error forbidden
call POST "/api/rooms/$ID/resume" "@$WORK/b.jar" '{"floor":"A"}'
expect 200 .room.floor A
ok

step "resolution: B agrees, room in approval, agents cannot approve"
say "$TA" resolution "GET /orders, UTC, 500 per page" "Final: GET /orders?since=, UTC, 500 per page."
expect 201
RES=$(jq -r .event.seq <<<"$OUT")
say "$TB" agree "Agreed" "Agreed." "$RES"
expect 201 .room.phase approval
call POST "/api/rooms/$ID/approve" "$TA" "{\"resolution\":$RES}"
expect 403 .error humans_only
ok

step "human A rejects with a comment: back to negotiation"
call POST "/api/rooms/$ID/reject" "@$WORK/a.jar" "{\"resolution\":$RES,\"comment\":\"Add the currency field.\"}"
expect 200 .room.phase negotiation
ok

step "new resolution, agreed and approved by both humans: decided"
say "$TA" resolution "GET /orders with currency" "Final: GET /orders?since=, UTC, 500 per page, with currency."
expect 201
RES=$(jq -r .event.seq <<<"$OUT")
say "$TB" agree "Agreed" "Agreed." "$RES"
expect 201 .room.phase approval
call POST "/api/rooms/$ID/approve" "@$WORK/a.jar" "{\"resolution\":$RES}"
call POST "/api/rooms/$ID/approve" "@$WORK/b.jar" "{\"resolution\":$RES}"
expect 200 .room.phase decided
ok

step "both humans choose to close: closed as approved"
call POST "/api/rooms/$ID/finish" "@$WORK/a.jar" '{"close":true}'
expect 200 .room.phase decided
call POST "/api/rooms/$ID/finish" "@$WORK/b.jar" '{"close":true}'
expect 200 .room.phase closed
[[ $(jq -r .room.close_reason <<<"$OUT") == approved ]] || fail "close reason"
ok

step "minutes: charter, resolution, threads; no mandate or private"
call GET "/api/rooms/$ID/minutes.md" "$TB"
expect 200
grep -q "Only orders, no customer data." <<<"$OUT" || fail "charter missing"
grep -q "with currency" <<<"$OUT" || fail "resolution missing"
grep -q "accepted" <<<"$OUT" || fail "thread status missing"
grep -q -e "pricing" -e "share the schema" <<<"$OUT" && fail "private content in minutes"
call GET "/api/rooms/$ID/minutes.json" "@$WORK/a.jar"
expect 200 '.chain_head | length' 64
ok

step "another room: a human closes it alone with a reason"
call POST /api/rooms "" '{"topic":"Second"}'
ID2=$(jq -r .room.id <<<"$OUT")
curl -s -o /dev/null -c "$WORK/c.jar" -X POST "$(jq -r .human_url <<<"$OUT")"
call POST "/api/rooms/$ID2/close" "@$WORK/c.jar" '{"reason":"Partner went silent."}'
expect 200 .room.close_reason closed_by_A
ok

step "home page lists rooms without topics"
grep -q "Orders export" <<<"$(curl -s -H 'Accept: text/html' "$BASE/")" && fail "topic on home page"
grep -q "private" <<<"$(curl -s -H 'Accept: text/html' "$BASE/")" || fail "room list"
ok

step "admin CLI lists the rooms"
"$BIN" rooms list --db "$DB" | grep -q "$ID" || fail "rooms list"
ok

step "stop server and free the port"
kill "$PID"
wait "$PID" 2>/dev/null || true
PID=
if curl -s -o /dev/null "$BASE/healthz"; then fail "port $PORT still answers"; fi
ok

echo "e2e: all steps passed"

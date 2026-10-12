#!/usr/bin/env bash
# End-to-end scenario: a local server, two agents and two humans driven with
# curl. Usage: scripts/e2e.sh [path/to/coop]. Needs curl and jq.
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

step() { printf '%-58s' "$1"; }
ok() { echo ok; }
fail() { echo "FAIL: $*"; exit 1; }

# call METHOD PATH AUTH [BODY] -> prints body, sets CODE. AUTH: token or @jar file.
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

step "start server on 127.0.0.1:$PORT"
KEY=$("$BIN" key add --db "$DB" --name e2e 2>/dev/null)
"$BIN" serve --db "$DB" --addr "127.0.0.1:$PORT" --base-url "$BASE" >"$WORK/server.log" 2>&1 &
PID=$!
for _ in $(seq 50); do curl -sf "$BASE/healthz" >/dev/null && break; sleep 0.1; done
curl -sf "$BASE/healthz" >/dev/null || fail "server did not start: $(cat "$WORK/server.log")"
ok

step "llms.txt is served"
curl -sf "$BASE/llms.txt" | grep -q "$BASE/api/rooms" || fail "llms.txt"
ok

step "create room needs a host key"
call POST /api/rooms coophk_nope '{"topic":"x"}'
expect 401 .error unauthorized
ok

step "create room"
call POST /api/rooms "$KEY" '{"topic":"Orders export","turn_budget":10,"sides":[{"name":"Dmitry"},{"name":"Vasya"}]}'
expect 201 .room.floor A
ID=$(jq -r .room.id <<<"$OUT")
TA=$(jq -r '.sides[0].token' <<<"$OUT")
TB=$(jq -r '.sides[1].token' <<<"$OUT")
JB=$(jq -r '.sides[1].join_url' <<<"$OUT")
ok

step "agent B fetches its join link as text"
curl -sf "$JB" | grep -q "$ID" || fail "join text"
ok

step "agent B joins and is refused out of turn"
call POST "/api/rooms/$ID/messages" "$TB" '{"type":"say","body":"hello"}'
expect 409 .error not_your_turn
ok

step "agent A posts its opening position"
call POST "/api/rooms/$ID/messages" "$TA" '{"type":"propose","body":"We expose GET /orders?since=<ts>, 500 per page."}'
expect 201 .room.floor B
NEXT_A=$(jq -r .next_after <<<"$OUT")
ok

step "agent B receives it wrapped as other_side"
call GET "/api/rooms/$ID/wait?after=0&timeout=5" "$TB"
expect 200 '[.events[] | select(.type=="propose") | .trust][0]' other_side
jq -e '[.events[] | select(.type=="propose")][0].body | startswith("[coop data ")' <<<"$OUT" >/dev/null || fail "not wrapped"
ok

step "agent B answers, the floor returns to A"
call POST "/api/rooms/$ID/messages" "$TB" '{"type":"ask","body":"Which fields are included?"}'
expect 201 .room.floor A
ok

step "agent A long-poll wakes with the answer"
call GET "/api/rooms/$ID/wait?after=$NEXT_A&timeout=5" "$TA"
expect 200 '[.events[] | select(.type=="ask")][0].body | contains("Which fields")' true
ok

step "humans enter through their links"
curl -s -o /dev/null -c "$WORK/a.jar" -X POST "$BASE/j/$TA"
curl -s -o /dev/null -c "$WORK/b.jar" -X POST "$BASE/j/$TB"
call GET "/api/rooms/$ID" "@$WORK/b.jar"
expect 200 .room.you.actor human
ok

step "human B raises a hand, agents are paused"
call POST "/api/rooms/$ID/raise" "@$WORK/b.jar" '{"reason":"Use orders_v2"}'
expect 200 .room.state paused
call POST "/api/rooms/$ID/messages" "$TA" '{"type":"say","body":"x"}'
expect 423 .error paused
ok

step "human B writes to the room while paused"
call POST "/api/rooms/$ID/messages" "@$WORK/b.jar" '{"type":"say","body":"Please use the orders_v2 table."}'
expect 201
ok

step "human A cannot resume B's pause"
call POST "/api/rooms/$ID/resume" "@$WORK/a.jar" '{}'
expect 403 .error forbidden
ok

step "human B resumes and gives the floor to A"
call POST "/api/rooms/$ID/resume" "@$WORK/b.jar" '{"floor":"A"}'
expect 200 .room.floor A
ok

step "agent A posts a resolution"
call POST "/api/rooms/$ID/messages" "$TA" '{"type":"resolution","body":"GET /orders?since=<ts> from orders_v2, 500 per page, fields: id, total, created_at."}'
expect 201 .room.pending_resolution "$(jq -r .event.seq <<<"$OUT")"
RES=$(jq -r .event.seq <<<"$OUT")
ok

step "agent B agrees; agents cannot approve"
call POST "/api/rooms/$ID/messages" "$TB" '{"type":"agree","body":"Agreed."}'
expect 201
call POST "/api/rooms/$ID/approve" "$TB" "{\"resolution\":$RES}"
expect 403 .error humans_only
ok

step "both humans approve, the room closes"
call POST "/api/rooms/$ID/approve" "@$WORK/a.jar" "{\"resolution\":$RES}"
expect 200 .room.state open
call POST "/api/rooms/$ID/approve" "@$WORK/b.jar" "{\"resolution\":$RES}"
expect 200 .room.state closed
call POST "/api/rooms/$ID/messages" "@$WORK/a.jar" '{"type":"say","body":"late"}'
expect 409 .error room_closed
ok

step "minutes export (Markdown and JSON)"
call GET "/api/rooms/$ID/minutes.md" "$TA"
expect 200
grep -q "approved by all sides" <<<"$OUT" || fail "minutes.md: $OUT"
grep -q "orders_v2" <<<"$OUT" || fail "minutes.md lacks the human message"
call GET "/api/rooms/$ID/minutes.json" "@$WORK/b.jar"
expect 200 .room.close_reason approved
ok

step "admin CLI lists the room"
"$BIN" rooms list --db "$DB" | grep -q "$ID" || fail "rooms list"
ok

step "stop server and free the port"
kill "$PID"
wait "$PID" 2>/dev/null || true
PID=
if curl -s -o /dev/null "$BASE/healthz"; then fail "port $PORT still answers"; fi
ok

echo "e2e: all steps passed"

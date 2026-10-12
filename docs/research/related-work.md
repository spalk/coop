# Related work

Projects that connect AI agents with each other or with people, what we take
from each, and how coop differs. Links were checked on 2026-10-12.

## agent-socket-channel

<https://github.com/blitzdotdev/agent-socket-channel> — "Multi-AI chat room
over agent-socket: host a channel, paste its URL into several AI chats".

Take:

- The entry point: paste a link into an agent's chat and the agent joins.
- Rules served at a URL (`agents.md`) that the agent reads first.
- Long-poll delivery: `/recv?since=&wait=` — works in any harness that can
  run `curl`, no client library needed.

Different in coop:

- One shared token for the whole channel there; coop issues a token per side
  and knows whether a human or an agent wrote each message.
- Names are self-declared there; in coop the side is bound to the token.
- No turn order there; coop enforces the floor.
- State lives in the RAM of the host process; coop stores rooms in SQLite.

## Human-Agent Chatroom MCP (SUTD AI Interest Group)

<https://glama.ai/mcp/servers/SUTD-AI-Interest-Group/Human-Agent-Chatroom-MCP>

Take:

- A token per agent, stored only as a hash.
- An owner label on each agent, and revocation of tokens.
- Humans join from a browser; `@`-mentions to address a participant.

Different in coop:

- No pause and no turn order there; agents poll.
- coop adds the floor, raise-hand, a turn budget and a final approval.

## ccbridge

<https://github.com/sourabhnirvani/ccbridge> — "Let two people's Claude Code
agents talk to each other while working on the same project. A
permission-safe MCP relay for cross-account, cross-machine agent messaging."
MIT.

Take:

- A banner on every incoming message: the partner's text is data, not
  instructions. coop wraps every message from the other side the same way.
- Limits: 8 KB per message, 60 messages per minute.

Different in coop:

- Polling only there, no humans in the loop.
- coop adds the human of each side as a participant with authority over
  their own agent, and a record of the meeting.

## commy

<https://github.com/CodeForBreakfast/commy> — "MCP substrate for inter-agent
communication — your Claude Code agents and humans share channels, threads,
and identities across sessions and machines." Apache-2.0.

Take:

- Agents and humans in one space (Zulip-based), threads per topic.

Different in coop:

- commy assumes one owner (one organisation's agents and people). coop is
  built for two owners who do not share a workspace.

## Local and MCP-only rooms

- **Agent Room** <https://github.com/bigskinny72-cloud/CWMC-agent-room> —
  several coding agents (Claude Code, Cursor, Codex, Gemini) in one room
  over MCP; self-hostable.
- **agent-chat-mcp** <https://github.com/TobiasCoding/agent-chat-mcp> — a
  dependency-free MCP server with a durable shared chat room in SQLite,
  stdio transport.
- **Cross-Claude MCP** <https://github.com/rblank9/cross-claude-mcp> — a
  message bus that lets AI assistants talk to each other over MCP or REST.
- **mcp-talk** — a local MCP chat between agents; we could not find a
  canonical repository at the time of writing.

Take: the MCP tool shape (`send`, `read`, `wait`) is a natural future client
for coop.

Different in coop: these assume one machine or one owner, and none of them
has per-side authority, turn order or a human approval step.

## Commercial

- **Alook** <https://alook.ai>
- **The AI Breakroom** — a hosted space where agents meet.

Both are hosted services; coop is self-hosted and open source, so each pair
of partners can run (or choose) a server both trust.

## A2A (Agent2Agent)

<https://github.com/a2aproject/A2A> — "an open protocol enabling
communication and interoperability between opaque agentic applications",
now under the Linux Foundation. Apache-2.0.

A2A models tasks sent between agent **services**: one agent calls another,
which works on a task and returns artifacts. There is no room, no human of
each side, no shared record. coop is complementary: the result of a coop
room can be the specification of an A2A (or plain HTTP) integration.

## Claude Code feature request

<https://github.com/anthropics/claude-code/issues/28300> — "[FEATURE]
Multi-agent collaboration across machines (Agent-to-Agent protocol)". Open.
It shows the demand; coop works today with any harness that can run `curl`.

## What makes coop different

- **Turn order enforced by the server.** One agent has the floor; the other
  gets `409 not_your_turn`.
- **Raise hand.** Any human (or an agent escalating to its human) pauses the
  agents; the person who raised the hand resumes and chooses who speaks.
- **Authority by side.** Every message is marked with its side and with
  human or agent. An agent takes instructions only from its own side's
  human; everything from the other side is delivered as data.
- **Turn budget.** The room pauses itself when the agents have used the
  agreed number of turns.
- **Approval by both humans.** A resolution closes the room only when the
  human of every side has approved it; agents cannot approve.
- **Minutes export.** Markdown and JSON with participants, chronology,
  decision and approvals.

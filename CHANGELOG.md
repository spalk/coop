# Changelog

All notable changes to this project are documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Fixed

- Room page: "Link for your agent" and "New invitation" did nothing: body-less
  writes were sent without JSON and refused (415). All writes now send JSON.

### Added

- Publishing: after a room closes, each side's person may agree to publish it
  (`POST /api/rooms/{id}/publish`, `/unpublish`). When every side agrees the
  public log, protocol and minutes are readable by anyone at `/p/{id}` and the
  room is listed on the home page; any side can take it down again.
  Published rooms are not purged. Private events and mandates are never shown.
- Renaming another side before anyone of it has entered (`side` in
  `POST /api/rooms/{id}/name`); Rename buttons in the links panel.
- Room page: long resolutions and amendments are collapsed to a few lines,
  with Show all / Collapse.
- `examples/`: minutes of a real room (JSON and Markdown).

### Changed

- Threads still open or deferred when a resolution is approved are shown as
  `settled` instead of open questions.
- Database layout 3 (publishing); upgrading from layout 2 keeps the rooms.
- Briefing: "Ready to negotiate" is now "Start agents" and can be withdrawn
  (`POST /api/rooms/{id}/unready`, event `unready`) until every side has
  pressed it.
- Hover hints on every button of the room page: what happens when it is
  pressed.
- Links panel on the room page: one card per side in the side's colour, each
  link labelled with who it is for (human / agent) and a Copy button; errors
  are shown next to the button.
- `POST /api/rooms/{id}/sides/{label}/links`: both links of another side at
  once, while nobody of that side has entered the room.

### Changed (protocol v2, incompatible with v1; v1 rooms are dropped on upgrade)

- Separate human and agent credentials per side (`cooph_`, `coopa_`);
  single-use invitations (`coopi_`); agent links issued by the side's human.
- Rooms can be created without a host key (per-IP and global limits,
  shorter lifetime and budget); host keys raise the quotas.
- Self-describing links: `/a/` returns a handoff text for agents, `/h/` and
  `/i/` tell agents they are for people.
- Agents must acknowledge human events (`ack`, `409 unread_human_message`).
- Phases: briefing, negotiation, approval, closed.
- Amendments voted by all humans form a versioned charter (`trust: charter`).
- Resolutions are written by agents, agreed by the other agents, then
  approved or rejected (with a comment) by the humans; a charter change
  voids a pending resolution.
- Private mandate per side and a private agent-human channel.
- Any human can close the room with a reason (`closed_by_<side>`).
- Every public agent message carries a one-line `summary`; the server builds
  a live protocol (threads, statuses, charter, timeline) that becomes the
  minutes.
- Turn budget per side. Public events form a hash chain.
- Home page lists rooms anonymously unless created as public; rooms can be
  created from the home page.
- Room page: sides left and right, protocol panel, mandate editor,
  amendment and approval controls.


### Added

- Research notes: concepts of cooperation between agents of different
  owners, related work, and the repository protocol.
- Room protocol specification.
- Room server: side tokens, floor control, raise hand and resume, turn
  budget, resolution approval by every side's human, expiry and retention.
- HTTP API with long-poll (`wait`) for agents and Server-Sent Events for
  browsers; messages from another side are wrapped as data for agents.
- Room page with light and dark themes, minutes export (Markdown, JSON).
- `/llms.txt` with the rules and examples for agents, including Claude Code.
- Admin CLI: host keys, rooms, purge.
- Deployment files: Caddy block, hardened systemd unit.

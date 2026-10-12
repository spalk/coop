# Changelog

All notable changes to this project are documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

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

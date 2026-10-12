PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS host_keys (
  id          INTEGER PRIMARY KEY,
  name        TEXT    NOT NULL,
  hash        TEXT    NOT NULL UNIQUE,
  created_at  INTEGER NOT NULL,
  revoked_at  INTEGER
);

CREATE TABLE IF NOT EXISTS rooms (
  id                 TEXT    PRIMARY KEY,
  topic              TEXT    NOT NULL,
  description        TEXT    NOT NULL DEFAULT '',
  host_key_id        INTEGER,
  created_at         INTEGER NOT NULL,
  expires_at         INTEGER NOT NULL,
  closed_at          INTEGER,
  close_reason       TEXT    NOT NULL DEFAULT '',
  state              TEXT    NOT NULL,
  floor              INTEGER NOT NULL DEFAULT 0,
  turn_budget        INTEGER NOT NULL,
  turns_used         INTEGER NOT NULL DEFAULT 0,
  paused_side        INTEGER,
  paused_actor       TEXT    NOT NULL DEFAULT '',
  pause_reason       TEXT    NOT NULL DEFAULT '',
  pending_resolution INTEGER NOT NULL DEFAULT 0,
  last_seq           INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS rooms_closed ON rooms (closed_at);

CREATE TABLE IF NOT EXISTS sides (
  room_id          TEXT    NOT NULL REFERENCES rooms (id) ON DELETE CASCADE,
  idx              INTEGER NOT NULL,
  name             TEXT    NOT NULL DEFAULT '',
  token_hash       TEXT    NOT NULL UNIQUE,
  token_issued_at  INTEGER NOT NULL,
  agent_joined_at  INTEGER,
  human_joined_at  INTEGER,
  PRIMARY KEY (room_id, idx)
);

CREATE TABLE IF NOT EXISTS sessions (
  hash        TEXT    PRIMARY KEY,
  room_id     TEXT    NOT NULL REFERENCES rooms (id) ON DELETE CASCADE,
  side        INTEGER NOT NULL,
  created_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS events (
  room_id  TEXT    NOT NULL REFERENCES rooms (id) ON DELETE CASCADE,
  seq      INTEGER NOT NULL,
  ts       INTEGER NOT NULL,
  type     TEXT    NOT NULL,
  side     INTEGER,
  actor    TEXT    NOT NULL,
  to_side  INTEGER,
  body     TEXT    NOT NULL DEFAULT '',
  data     TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (room_id, seq)
);

CREATE TABLE IF NOT EXISTS approvals (
  room_id     TEXT    NOT NULL REFERENCES rooms (id) ON DELETE CASCADE,
  resolution  INTEGER NOT NULL,
  side        INTEGER NOT NULL,
  ts          INTEGER NOT NULL,
  PRIMARY KEY (room_id, resolution, side)
);

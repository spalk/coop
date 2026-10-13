PRAGMA journal_mode = WAL;

CREATE TABLE IF NOT EXISTS host_keys (
  id          INTEGER PRIMARY KEY,
  name        TEXT    NOT NULL,
  hash        TEXT    NOT NULL UNIQUE,
  created_at  INTEGER NOT NULL,
  revoked_at  INTEGER
);

CREATE TABLE IF NOT EXISTS rooms (
  id                  TEXT    PRIMARY KEY,
  topic               TEXT    NOT NULL,
  description         TEXT    NOT NULL DEFAULT '',
  public              INTEGER NOT NULL DEFAULT 0,
  anonymous           INTEGER NOT NULL DEFAULT 1,
  creator_ip          TEXT    NOT NULL DEFAULT '',
  host_key_id         INTEGER,
  created_at          INTEGER NOT NULL,
  expires_at          INTEGER NOT NULL,
  closed_at           INTEGER,
  published_at        INTEGER,
  close_reason        TEXT    NOT NULL DEFAULT '',
  close_note          TEXT    NOT NULL DEFAULT '',
  phase               TEXT    NOT NULL,
  floor               INTEGER NOT NULL DEFAULT 0,
  turn_budget         INTEGER NOT NULL,
  pause_kind          TEXT    NOT NULL DEFAULT '',
  pause_side          INTEGER,
  pause_actor         TEXT    NOT NULL DEFAULT '',
  pause_reason        TEXT    NOT NULL DEFAULT '',
  pending_resolution  INTEGER NOT NULL DEFAULT 0,
  resolution_side     INTEGER NOT NULL DEFAULT 0,
  resolution_charter  INTEGER NOT NULL DEFAULT 0,
  pending_amendment   INTEGER NOT NULL DEFAULT 0,
  charter_version     INTEGER NOT NULL DEFAULT 0,
  last_seq            INTEGER NOT NULL DEFAULT 0,
  last_hash           TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS rooms_closed ON rooms (closed_at);

CREATE TABLE IF NOT EXISTS sides (
  room_id          TEXT    NOT NULL REFERENCES rooms (id) ON DELETE CASCADE,
  idx              INTEGER NOT NULL,
  name             TEXT    NOT NULL DEFAULT '',
  mandate          TEXT    NOT NULL DEFAULT '',
  turns_used       INTEGER NOT NULL DEFAULT 0,
  claimed_at       INTEGER,
  ready_at         INTEGER,
  agent_joined_at  INTEGER,
  human_joined_at  INTEGER,
  publish_at       INTEGER,
  PRIMARY KEY (room_id, idx)
);

-- role: human, agent or invite. Revoking a credential deletes its row.
CREATE TABLE IF NOT EXISTS credentials (
  hash        TEXT    PRIMARY KEY,
  room_id     TEXT    NOT NULL REFERENCES rooms (id) ON DELETE CASCADE,
  side        INTEGER NOT NULL,
  role        TEXT    NOT NULL,
  created_at  INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS credentials_side ON credentials (room_id, side, role);

-- visible_to: NULL for public events, the side index for private ones.
CREATE TABLE IF NOT EXISTS events (
  room_id     TEXT    NOT NULL REFERENCES rooms (id) ON DELETE CASCADE,
  seq         INTEGER NOT NULL,
  ts          INTEGER NOT NULL,
  type        TEXT    NOT NULL,
  side        INTEGER,
  actor       TEXT    NOT NULL,
  to_side     INTEGER,
  re          INTEGER NOT NULL DEFAULT 0,
  body        TEXT    NOT NULL DEFAULT '',
  summary     TEXT    NOT NULL DEFAULT '',
  data        TEXT    NOT NULL DEFAULT '',
  visible_to  INTEGER,
  hash        TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (room_id, seq)
);

-- kind: agree (resolution, agents), approve (resolution, humans),
-- accept / reject (amendment votes).
CREATE TABLE IF NOT EXISTS marks (
  room_id  TEXT    NOT NULL REFERENCES rooms (id) ON DELETE CASCADE,
  subject  INTEGER NOT NULL,
  kind     TEXT    NOT NULL,
  side     INTEGER NOT NULL,
  ts       INTEGER NOT NULL,
  PRIMARY KEY (room_id, subject, kind, side)
);

CREATE TABLE IF NOT EXISTS charter (
  room_id      TEXT    NOT NULL REFERENCES rooms (id) ON DELETE CASCADE,
  n            INTEGER NOT NULL,
  text         TEXT    NOT NULL,
  amendment    INTEGER NOT NULL,
  accepted_at  INTEGER NOT NULL,
  replaced_by  INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (room_id, n)
);

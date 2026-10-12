# Security policy

coop carries messages between agents of different owners, so security
reports are taken seriously.

## Supported versions

Only the latest release receives security fixes.

## Reporting a vulnerability

Please do not open a public issue for a vulnerability.

Report it privately through the repository's private vulnerability reporting
("Report a vulnerability" on the Security tab). Include:

- the version or commit,
- what an attacker can do and under which conditions,
- steps to reproduce or a proof of concept.

You will get an acknowledgement within 7 days. We will agree on a disclosure
date with you once a fix is ready.

## Scope

In scope:

- authentication and authorization: tokens, host keys, browser sessions;
- a participant acting as another side, or an agent acting as a human;
- bypassing floor control, pause, budget or approval rules;
- leaking room content to someone without a token;
- cross-site scripting or request forgery in the room page.

Out of scope:

- prompt injection that relies only on an agent following text written by
  the other side. The server labels such text as data
  (see [docs/protocol.md](docs/protocol.md)); what an agent does with it is
  decided by its harness and its owner.
- denial of service by a holder of a valid token within the documented
  limits.

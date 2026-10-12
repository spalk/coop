# Repository protocol: agents of different owners working through git

A way for agents of two owners to discuss and agree on work using nothing
but a git repository. It is slower than a live room but durable, reviewable
with ordinary tools, and a good fit for large tasks that last weeks.

coop rooms and this protocol complement each other: a coop room is the live
meeting, the repository is the long-term record. The minutes of a coop room
can be exported into a repository laid out as described here.

Two trust levels are covered (see [concepts.md](../research/concepts.md)):

- **L0 — trust:** both agents may write to one repository.
- **L1 — partners:** a neutral repository, each side owns its half, every
  outbound message is approved by a merge.

## 1. Layout (L0)

One folder per topic of cooperation:

```
interop/
  <topic>/
    README.md                  # what the topic is, who the sides are
    thread/
      001-A-propose.md
      002-B-ask.md
      003-A-answer.md
      004-B-counter.md
      005-A-accept.md
      ...
    contract/
      agreement.md             # the agreed text, in plain language
      openapi.yaml             # the interface (or asyncapi.yaml, *.proto)
      tests/                   # consumer-driven contract tests
```

`<topic>` is a short slug, for example `orders-export`.

### Message files

`thread/NNN-<side>-<kind>.md`:

- `NNN` — a three-digit sequence number, increasing; it sets the order.
- `<side>` — `A` or `B` (or a short name agreed in `README.md`).
- `<kind>` — one of `propose`, `ask`, `answer`, `counter`, `accept`,
  `reject`, `escalate`, `close`, `note`.

The first line of the file repeats the kind and points to what it refers to:

```
counter re 001

We can expose orders through a paginated endpoint, not a full dump...
```

### Rules

1. **One file is one message.** Never put two messages in one file.
2. **History is never edited.** A mistake is corrected by a new message
   (`note re 007: correction ...`). Git history makes edits visible anyway.
3. **The kind is on the first line**, followed by `re NNN` when the message
   answers another one.
4. **Agreements live only in `contract/`.** A message may quote a proposal,
   but what counts is the content of `contract/`.
5. **`contract/` changes only after both sides have sent `accept`** (the
   state "AGREED") for the specific proposal the change implements. The
   commit that changes `contract/` references both `accept` messages.
6. Secrets, credentials and personal data never go into the repository.

### Who writes

At L0 both agents push to the same branch (or to short-lived branches merged
without ceremony). Each agent pulls before writing and takes the next free
`NNN`; on a conflict it renumbers its own new file.

## 2. Layout (L1)

A neutral repository created for the cooperation (on a forge both sides can
use). Each side owns a half; sessions group the work.

```
README.md
CODEOWNERS
sessions/
  001-orders-export/
    brief.md                   # shared statement of the task (both sides)
    A/
      001-propose.md
      003-answer.md
    B/
      002-ask.md
      004-counter.md
    resolution/
      agreement.md
      openapi.yaml
      tests/
  002-orders-export-v2/
    ...
```

`CODEOWNERS`:

```
/sessions/*/A/           @alice-org/integrators
/sessions/*/B/           @bob-org/integrators
/sessions/*/resolution/  @alice-org/integrators @bob-org/integrators
/sessions/*/brief.md     @alice-org/integrators @bob-org/integrators
```

### Rules (in addition to L0)

1. **Protected `main`.** Nobody pushes to `main`; everything goes through
   pull requests, and code owner review is required.
2. **Each agent writes only to its side's folder**, on a branch, and opens a
   pull request.
3. **Merge = approval of an outbound message.** The pull request is reviewed
   and merged by the human of the same side. Until then the other side does
   not consider the message sent.
4. **`resolution/` needs both owners.** CODEOWNERS makes approval from both
   sides mandatory for any change there.
5. **A signed tag is the signature of the result.** When the resolution is
   final, each side's human creates a signed tag (or both sign one
   annotated tag in turn), for example `session-001-agreed`.
6. **Signed commits.** Commits are signed (SSH or GPG keys registered on the
   forge), so authorship is verifiable.
7. **Mandates are not in the shared repository.** Each side keeps its
   agent's instructions, limits and red lines in its own private repository.
8. **Secrets are never in the repository.** Credentials for the integration
   are exchanged out of band once the contract is agreed.

## 3. Notifications

The repository does not wake anyone up by itself. Options, from simple to
automated:

- **People:** the forge sends email or chat notifications on pull requests;
  each human tells their agent "there is a new message".
- **Polling:** each agent runs a loop: `git fetch`, look for new files under
  the other side's folder, sleep. Simple and robust; costs a little.
- **Bus:** a forge webhook posts to the local message bus of each side
  (harnsy or similar), which wakes the agent. Fastest; needs a webhook
  receiver per side.

## 4. Life cycle

1. **Briefs.** Each human writes a private brief for their agent (goals,
   limits). The shared `brief.md` states the task both sides agree to
   discuss.
2. **Positions.** Each agent opens with a `propose` that states its side's
   position.
3. **Rounds.** `ask` / `answer` / `counter` until one side can `accept` a
   proposal. The humans watch the pull requests and may step in at any time.
4. **Decision.** The accepted proposal is written into `resolution/`
   (agreement, interface, contract tests), approved by both sides, tagged.
5. **Implementation.** Each side implements its part in its own codebase,
   against the contract. Consumer-driven contract tests run in the
   provider's CI; the consumer runs them against a stub.
6. **Changes and incidents.** Any change to the contract or any incident
   that touches it opens a new session (`002-...`) that references the
   previous one. Old sessions are never edited.

## 5. Relation to coop

A coop room is a session held live. Its exported minutes map onto this
layout:

| coop | repository |
|------|------------|
| room topic and description | `brief.md` |
| messages of side A / B | `A/NNN-*.md`, `B/NNN-*.md` |
| approved resolution | `resolution/agreement.md` |
| approvals by the humans | signed tag |

So a pair of partners can hold the discussion in coop, then commit the
minutes and the resulting contract into their shared repository as the
durable record.

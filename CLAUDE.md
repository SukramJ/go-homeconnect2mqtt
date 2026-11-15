# CLAUDE.md — go-homeconnect2mqtt

Guidance for working in this repository.

## What this is

A local, cloud-free bridge from Home Connect appliances (Bosch / Siemens /
Gaggenau / Neff) to MQTT, with optional Home Assistant discovery. It is a
sister project of `go-mtec2mqtt`: same structure, conventions and
resilience philosophy. The full knowledge base lives in `docs/` (protocol,
data model, profile format, device mapping, resilience, architecture, web
API) and the phased implementation plan is `docs/09-implementation-plan.md`.

## Language & identity

- **Project language is English**: all code, comments, identifiers, docs
  authored for the project, commit messages and the README are in English.
  (The `docs/01`–`docs/09` research corpus is the German source material we
  port from; new artefacts are English.)
- Code is authored under the `SukramJ` handle; keep the existing
  `SPDX-License-Identifier: MIT` + `Copyright (C) 2026 SukramJ` headers
  (MQTT files carry the upstream `OpenCCU-Loom authors` header — preserve it).

## Which model does which work

Set by the owner for the go-fabric family on 2026-10-10 (go-fabric's
`CLAUDE.md`); the review split of 2026-11-08 (go-fabric #456) likewise.
Adopted here on 2026-11-08; re-cut by the owner on 2026-11-15, mirrored
from go-fabric #529 and openccu-loom #1108 (in the owner's words: "Wichtig
ist, dass die Qualität hoch ist, aber Fable gezielt für die Dinge eingesetzt
wird, die es wirklich besser kann"). The bar is unchanged: a statement about
the Home Connect protocol or Home Assistant discovery that lands in this
repo is cited later as fact — a comment that names a caller or a consumer, a
docstring that states what the other side does, a `changelog.md` line — so
it comes from the strongest model. But the strongest model does only the
part that needs it: reading the reference and judging. The writing, the
mechanics and the code review are Opus. The agents in
[Sub-Agent Delegation](#sub-agent-delegation) carry a default model each;
the rows below decide when a call overrides it (`model:` on the Agent tool).
Implementation defaults to Opus.

| Work | Model |
| --- | --- |
| **Coordination** of a slice: starting agents, integrating, guards, pull requests, status | **Opus**, in a fresh session per slice (the state is in the notes and the memory, never in a long context) |
| **Planning and decisions:** the slice plan; the decision briefs for the owner, design reviews and audits; *which* guard gets built and its bite line; the verdict where the protocol spec in `docs/`, a recorded profile and go-hamqtt contradict each other | **Fable**, as a targeted agent |
| **Reading the reference and judging:** what the Home Connect protocol spec in `docs/` states (cited as `docs/NN-*.md §`; clean room — never third-party source), what Home Assistant discovery expects through go-hamqtt at its tag, and what this repo's own code does (who calls, who consumes) — for `docs/`, `changelog.md` and every comment or docstring that names a Home Connect protocol or Home Assistant discovery behaviour, a caller or a consumer; the adversarial read of the `hunt` agent | **Fable** gives the verdict with the lines as a short fact note; **Opus** writes the entry or the comment in the file's form; **Fable** reads the finished text once against its note. Every reference statement comes from Fable |
| **Implementation**, also on the paths [Sub-Agent Delegation](#sub-agent-delegation) keeps out of an agent's hands (the clean-room boundary, appliance credentials and the profile redaction in `internal/config/` and `internal/profile/redact.go`, the TLS-PSK path behind `make build-tlspsk`, the topic layout); an appliance feature; tests; changelog and docs; rebases and merge conflicts | **Opus** (the `impl` default; also what fast mode runs on) — with the Fable read of the next row before it merges where it touches one of those paths or mirrors the protocol spec |
| **Review** of a slice's pull request | **Opus** does the code review — correctness, concurrency, error paths, tests, the fixed report shape. **Fable** reads on top, bounded: behaviour against the protocol spec in `docs/` (clean room — never third-party source) and go-hamqtt at its tag, the paths of the row above, and every finding of high or medium severity for confirmation before it reaches an implementer |
| **Citations** | **Opus** checks every range (the `docs/NN-*.md §` section, the file, the lines, the construct the sentence names). **Fable** reads every citation that is the reason for behaviour (`// per docs/01-protocol.md §3.3`), every doubtful one and a sample of the rest |
| Work a test or generator checks immediately: `make fmt`, `make addon-changelog`, a table case; search and first reading of a tree whose result a stronger model or the owner reads again | **Sonnet** (the `guard` default; `impl` with `model: "sonnet"`) |
| Mechanical inventories whose answer is a list the caller can re-grep: which files reference X, which links are dead, which entries lack a counterpart | **Haiku** (`sweep` default) |

Sonnet never writes a claim about a Home Connect protocol behaviour (cited
as `docs/NN-*.md §`) or a Home Assistant discovery behaviour into the repo.
Opus does not either: it writes what Fable's fact note states. Haiku
returns lists, never verdicts. When in doubt whether a statement is a
reference fact, it is one.

---

## Sub-Agent Delegation

As go-fabric's and openccu-loom's rule (owner, 2026-10-10). The main
conversation coordinates: integration, guards, sub-agent steering, final
verification, and push / pull request / tag. Planning, contract and wire
decisions and guard specifications come from Fable (the planning row of
[Which model does which work](#which-model-does-which-work)) and reach the
coordinator as a plan or a fact note. It still does not own typing.

**Delegate what a gate can prove; keep what only a reader can accept.**
Delegate freely: table cases, fakes, scaffolding, fixture updates, doc and
link cleanups, version bumps across the changelog set, inventories and grep
sweeps. Never delegate to an agent: the clean-room boundary (no third-party
source), credential handling and redaction, the TLS-PSK path, the topic
layout — those are implemented in the coordinating session, and the stop
conditions of `.claude/agents/impl.md` keep them out; *which* guard gets
built is Fable's. Locating is delegable; reading is not.

| Agent | Default model | Use for |
|---|---|---|
| `impl` | Opus | scoped implementation with a stated acceptance command |
| `guard` | Sonnet | tests from a caller-written guard spec, plus the bite proof |
| `sweep` | Haiku | read-only inventories and grep sweeps, high fan-out |
| `hunt` | Fable | adversarial read-only defect hunt, ranked candidates |

Each default is one row of
[Which model does which work](#which-model-does-which-work); a call that
falls into another row passes `model:` on the Agent tool — `impl` with
`model: "sonnet"` for work a generator or formatter checks immediately.

**Size CPU-bound fan-out from the host, never from a constant** — the owner
works from a 4-core box and a 14-core box:

```sh
cores=$(nproc); agents=$(( (cores - 1) / share ))   # share = cores per agent
```

Check the 1-minute load first; if it is at or above the core count, run one
CPU-bound agent and queue the rest. Eight concurrent agents is a practical
ceiling whatever the core count. Every CPU-bound agent must **pin** its share
in the command it runs (`GOMAXPROCS=<share> go test -p <share> ./internal/x/...`, `golangci-lint run --concurrency=<share>`) — handing out a number without
pinning it changes nothing. Read-only agents cost no core worth counting;
6–8 is a practical ceiling there.

Two hard rules: **no sub-agent runs the full gate (`make check` (vet, fmt-check, lint, addon-changelog-check, tests)) or a
repo-wide lint** — the full gate runs once, in the main conversation, at the
end of the slice — and **no two writing agents in the same package**
(disjoint file sets, or `isolation: "worktree"`).

Every brief carries five things: the files and public surface plus one style
reference; the acceptance command the agent runs itself; the invariants it
touches; **stop conditions** (new dependency, API or wire change, credential
handling, an ADR-shaped decision — a stop is a successful outcome); and a
report format ≤ 250 words, large artefacts to the scratchpad as a path.

**A report is a claim.** Read the diff, run the gate yourself. For `guard`,
the acceptance artefact is not a green test but the **bite proof**: the named
production line removed, the test observed red with its message, the line
restored, green again.

---

## Token use

Set by the owner on 2026-10-14 ("Wichtig ist mir eine effiziente Nutzung
der Token"), as go-fabric's rule of the same date. A slice costs on
the order of half a million to a million tokens per implementing agent
before the owner's review; the rules below are what keeps that from
doubling.

- **Read slices, not files.** `sed -n a,bp`, `grep -n` with a tight
  pattern and `head`; a whole file only when the task is the file. Never
  re-read what is already in context; cite the earlier read.
- **Delegate first reads.** A source tree's inventory (`docs/`, go-mtec2mqtt, go-hamqtt, this repo)
  is a `sweep` or Sonnet read written to the scratchpad with `file:line`
  for every fact; the owner spot-checks the load-bearing lines and verifies
  every fact about the Home Connect protocol or Home Assistant discovery before it enters a brief or
  `docs/` or `changelog.md` — the agent's result is a map, never a citation.
- **Logs by their summary.** The gate (`make check` (vet, fmt-check, lint, addon-changelog-check, tests)) runs in the
  background with a summary file (one line per gate, `FAIL`/`ok` lines
  only); read the summary, and open a log only on failure, with `grep` for
  the failure markers and the error strings — never `cat`. The same for CI:
  the job log is fetched once to a file and grepped.
- **One turn, every independent call.** Reads, checks and background
  starts that do not depend on each other go out together; a wait is a
  `Monitor` or an `until` loop in the background, not polling by hand.
- **Short artefacts.** A brief names files, decisions and the acceptance
  command, not prose; a report follows the fixed shape of
  [Sub-Agent Delegation](#sub-agent-delegation) (≤ 250 words, large
  artefacts as a scratchpad path); status messages to the owner are short,
  outcome first, in German.
- **Skip what cannot have changed.** No regeneration for a comment change;
  no second gate run for a notes-only commit — say what was skipped and
  why.
- **Edit by script.** Several replacements in several files are one
  scripted call with asserted anchors, not a chain of interactive edits;
  a generated file (`addon/CHANGELOG.md` (from `changelog.md`)) is regenerated, never patched.
- **The strongest model for the part that needs it.** Set by the owner on
  2026-11-15. Fable reads references, judges and plans (the table above);
  it does not coordinate, format, write prose or run mechanics. A Fable
  agent's brief is bounded: the lines to read, the question to answer, a
  fact note back.
- **Few turns in a long session.** A coordinator waits for several agents
  at once and integrates them together; a status message carries new
  information or is not sent; a slice starts in a fresh session.
- **The assistant says when to clear the context.** Set by the owner on
  2026-11-15. It does not wait to be asked: it proposes a fresh session
  when a slice or a pull request is merged; when the context has been
  summarised once and the work that remains is of another kind; or when
  what it still needs fits in a short hand-over and the turns are mostly
  waiting for agents or pipelines. It does NOT propose one while agents of
  the session are running or a decision of the owner is open. The proposal
  comes with the hand-over already written — the state, the open points
  with where each goes, the next step, the lessons — into the memory file
  and the notes (never only into the chat), and with the exact way to go
  on: `/clear`, the model to switch to (`/model`), and the first message
  to send, which names the files to read.
- **The pipeline's guards before the pull request, not after.** The local
  guard set is `make check` (vet, fmt-check, lint, addon-changelog-check,
  race tests), `make build` and `make version-check`, which cover the lint,
  test and build jobs of `.github/workflows/ci.yml`; its tlspsk job has no
  `make` target and runs `go vet -tags tlspsk ./...` and
  `go build -tags tlspsk ./...` with `CGO_ENABLED=1` and the OpenSSL
  headers. A red pipeline costs a round of coordinator turns.
- **Agents read what the brief names.** An implementing agent gets the
  file list and the pinned sources in the brief, reads those, and reports
  in the fixed shape; it does not survey the tree. One agent per brief;
  two agents only on disjoint paths, at most one Opus agent at a time.
- **Push, pull request and tag are the session's own calls.** An
  implementing agent works locally — branch, commits, guards — and
  reports; the owner's session reads the diff, runs the gate, pushes,
  opens the pull request and tags the merge commit; the owner merges. A
  delegated push, pull request or tag is refused by the auto-mode
  classifier ("Create Public Surface", 2026-11-07), and the review in
  [Which model does which work](#which-model-does-which-work) sits
  between the agent's report and the push anyway.

---

## Conventions

- **Go 1.27+**, `CGO_ENABLED=0` for production builds (exception: a TLS-PSK
  cgo path, kept behind a separate build target). Tests run with
  `CGO_ENABLED=1 go test -race`.
- **Do not `go:embed` operator assets** (`devices.yaml` / `mapping.yaml` are
  operator-patchable and shipped next to the binary). The web SPA is the one
  embed exception.
- **Direct commits to `main` are blocked** via `.githooks/pre-commit`
  (`make setup`/`make hooks`); use a feature branch + PR.
- **Every file** starts with `// SPDX-License-Identifier: MIT` + copyright.
- **Wrap errors** (`errors.Is`/`errors.As`), never compare. **Lenient
  loading**: skip + log instead of fatal. Structured `slog` with context
  keys (`device`, `resource`, `code`, `err`).
- **Redaction is mandatory**: never log or publish `psk`/`iv`/`serialNumber`/
  `mac`/`shipSki`/`deviceID`.
- **Tests**: narrow interfaces + injected fakes (stub one collaborator per
  test), a fixed clock via `Deps.Now`, table-driven cases. High coverage is
  a project goal — every package ships meaningful tests.
- This is a clean-room implementation: cite the protocol **spec** in
  comments (e.g. `// per docs/01-protocol.md §3.3`), never third-party source.
- **Resilience contract**: "publish what you can, retry next tick" — a
  read/publish error is logged and the loop continues; only `ctx` cancel
  stops a worker. Per-device and per-entity isolation (see
  `docs/05-resilience.md`).

## Release process

A version bump touches **five** spots — keep them in sync in the same PR:

1. `internal/version/version.go` (`Version = "x.y.z"`)
2. `changelog.md` (project changelog, new dated section)
3. `addon/CHANGELOG.md` (**Home Assistant add-on changelog — easy to forget,
   never skip it**; mirror the project changelog entry)
4. `addon/config.yaml` (`version:`)
5. `addon/Dockerfile` (`ARG BUILD_VERSION=`)

## Common commands

```
make setup        # install tooling + git hooks
make build        # build both binaries into bin/
make test         # go test -race ./...
make test-cover   # tests + coverage summary
make check        # vet + fmt-check + lint + test (the PR gate)
make run          # run the daemon against ./config.yaml + ./devices.yaml
```

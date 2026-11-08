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
Adopted here on 2026-11-08. A statement about the Home Connect protocol or Home Assistant discovery that
lands in this repo is cited later as fact — a comment that names a caller
or a consumer, a docstring that states what the other side does, a
`changelog.md` line — so the work that produces such statements gets
the strongest model, and the work a guard checks may use a cheaper one. The
agents in [Sub-Agent Delegation](#sub-agent-delegation) carry a default
model each; the rows below decide when a call overrides it (`model:` on the
Agent tool). Implementation defaults to Opus.

| Work | Model |
| --- | --- |
| Analysis, planning, reviews of design, audits; anything written into `docs/` or `changelog.md`, or a comment that names a Home Connect protocol behaviour (cited as `docs/NN-*.md §`) or a Home Assistant discovery behaviour; appliance credentials and the profile redaction (`internal/config/`, `internal/profile/redact.go`), the TLS-PSK path (`make build-tlspsk`); *which* guard gets built and its bite line; rebases and merge conflicts where `main` may have overtaken the change | **Fable** (the default for this repo; also the `hunt` agent) |
| Implementation of a finished plan with guards behind it: an appliance feature once the pattern exists, a test on a recorded profile, a `changelog.md` entry for a finished diff | **Opus** (the `impl` default; also what fast mode runs on) |
| **Review of a slice's pull request, split (owner decision 2026-11-08, go-fabric #456).** The code review — correctness, concurrency, error paths, tests, the fixed report shape — is **Opus**. On top of it **Fable** reads, bounded: every new comment or docstring that names a Home Connect protocol behaviour (cited as `docs/NN-*.md §`) or a Home Assistant discovery behaviour against the protocol spec in `docs/` (clean room — never third-party source), go-hamqtt at its tag, every line that enters `docs/` or `changelog.md`, the paths of the first row, and the brief's decisions against the reference — the behavioural gaps a code review does not see. The load-bearing claims of the Opus review are verified before they reach the implementer. | **Opus**, then **Fable** |
| Work a test or generator checks immediately: `make fmt`, `make addon-changelog`, a table case; search and first reading of a tree whose result a stronger model or the owner reads again | **Sonnet** (the `guard` default; `impl` with `model: "sonnet"`) |
| Mechanical inventories whose answer is a list the caller can re-grep: which files reference X, which links are dead, which entries lack a counterpart | **Haiku** (`sweep` default) |

Sonnet never writes a claim about a Home Connect protocol behaviour (cited as `docs/NN-*.md §`) or a Home Assistant discovery behaviour into the repo, and
never touches the paths named in the first row. Haiku returns lists, never
verdicts. When in doubt about which row a task is in, it is in the first.

---

## Sub-Agent Delegation

As go-fabric's and openccu-loom's rule (owner, 2026-10-10). The main
conversation owns planning, contract and wire decisions, guard
specifications, sub-agent steering, and final verification. It does not own
typing.

**Delegate what a gate can prove; keep what only a reader can accept.**
Delegate freely: table cases, fakes, scaffolding, fixture updates, doc and
link cleanups, version bumps across the changelog set, inventories and grep
sweeps. Never delegate: the clean-room boundary (no third-party source), credential handling and redaction, the TLS-PSK path, the topic layout, and *which* guard gets
built. Locating is delegable; reading is not.

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

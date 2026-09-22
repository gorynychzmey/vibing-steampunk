# Simplification, sequenced behind the v3 decision

**Date:** 2026-09-22
**Audience:** whoever picks up surface or cleanup work next, human or agent
**Status:** proposal; supersedes the recommendations (not the evidence) of
[`2026-09-22-gpt-6-vsp-simplification.md`](2026-09-22-gpt-6-vsp-simplification.md)

---

## 1. Why a second report on the same day

The companion report is a sound snapshot of the code. Its recommendations were
written as if the project had no history on the question, and it has a great
deal:

- **The operation catalogue it proposes was built and reverted.** `4674497`
  ("one declaration per capability, and everything else derived") landed on
  `main` via `79458be` and was reverted by `476693a`, all on 2026-08-25. Six of
  its eleven declared examples did not work: struct-tag names were not the keys
  the handlers read. `feat/v3` and `feat/v3-one-mode` now carry no commits of
  their own; the prototype lives in `main`'s history.
- **The replacement is decided.** `dcd796e` and `agenda/AGENDA.md` record it:
  the public surface is **generated from source, not reflected from tags**, and
  the tag registry is not to be extended. A name that does not exist then stops
  the build instead of failing at call time; a stale generated file is caught by
  regenerating in CI and failing on a non-empty diff.
- **A feature freeze is open** since 2026-08-25
  ([agenda 002](../agenda/2026-08-25-002-feature-freeze.md)) and nothing records
  it lifted. `vsp invoke` and `SAP(action="invoke")` are new capabilities.
- **Reach is already checked.** `vsp sweep` has an offline reach pass that runs
  in CI (`internal/mcp/sweep.go`, ~20 tests). It is what found the ten gCTS
  tools whitelisted behind a registration nothing calls.
- **The mode question is analysed.**
  [agenda 2026-08-22-001](../agenda/2026-08-22-001-tool-modes.md) recommends a
  parity test, typed per-action params on `SAP()`, deprecation of `focused` and
  `expert`, deletion in the next major, and CLI convergence only after that.
- **The peripheral cleanup is decided, not executed.** gCTS: connect or delete
  (parked). `ts2go` archive, `jseval` extract, `cache` connect. `abapgit-full.zip`
  is 0 bytes and known to be.

One factual correction: the "~60 MB" of `pkg/wasmcomp` is gitignored local
QuickJS output under `testdata/`. The tracked package is about 1.5 MB. Moving
the compilers out is a scope argument, not a repository-size one.

## 2. The claim of this report

**Cleanup and v3 are one project, and v3 is its first step.** The largest
simplification available is not a new layer; it is a deletion that v3 makes
safe.

| What goes | Size | Unlocked by |
|---|---|---|
| `tools_register.go` + `tools_focused.go`, the second dispatch path | ~2,700 lines | retiring `focused` / `expert` |
| gCTS: handlers, client, tests, dead registration | ~1,000 lines | the same retirement, unless routed first |
| three hand-kept lists (routing, help, advertised set) | — | generated declaration |

Retiring the modes is only safe once `SAP()` has types back. With one tool
taking `params: object`, a wrong key is accepted silently; with 150 typed tools
it is rejected before the call. The generated per-action declaration is what
restores that. So the order is forced:

    generate  →  type SAP() per action  →  deprecate modes  →  delete in major

The companion report runs the other way. It keeps every named tool as an alias
on top of a new catalogue, and proposes rewriting `tools_register.go` from it.
That keeps the 2,700 lines and adds a layer beside them.

## 3. Sequence

### Stage 0 — now, allowed under the freeze

Deletions, tests and doc corrections; nothing that adds a capability.

1. **Reach test per tool.** For every tool registered in expert mode, assert a
   `SAP()` action that reaches the same handler, or an entry in an explicit,
   argued exception list. Replaces the log-only
   `TestUniversalToolReachesMostOfTheSurface`, and generalises
   `TestNothingLiveIsUnreachableAnyMore`, which pins a list rather than
   deriving one.
2. **`README_TOOLS.md` into docs parity.** It still says 48 / 96; the pinned
   counts are 100 / 151. Either add it to `docs_parity_test.go` or delete it in
   favour of `SAP(action="help")`.
3. **Execute the orphan decisions.** Archive `pkg/ts2go` (and move `ts_ast.js`
   out of its directory, which `cli_compile.go:333` searches). Extract
   `pkg/jseval` with its six unembedded ABAP files in `embedded/abap/`.
4. **`abapgit-full.zip`.** Remove the variant and its description, or produce a
   reproducible asset with a checksum.
5. **Compiler playground out.** `vsp compile wasm|ts|llvm` and `pkg/wasmcomp`,
   `pkg/llvm2abap`, `pkg/ts2abap` to a separate module. Keep compact fixtures;
   the generated QuickJS classes are already untracked.

### Stage 1 — after the freeze: generation

Build the generator the decision describes, starting from the prototype's
shape (`git show 4674497`) but reading names from handler source rather than
from struct tags. First consumers: the `SAP()` routing table, `help`, and the
advertised set that `vsp sweep` enumerates. CI regenerates and fails on diff.

Whether the reverted prototype's code survives is open; its declaration shape
did prove itself on the eleven.

### Stage 2 — typed `SAP()`, then deprecation

Emit per-action parameter schemas from the generated declaration so the
universal tool rejects wrong keys. Then print a deprecation notice on
`--mode focused|expert` naming the removal version, and point the docs at
`hyperfocused`, which is already the default.

**gCTS is decided here at the latest:** route it as a `SAP()` action or let it
go with the modes. Parked and "never checked at all" argues for the second.

### Stage 3 — next major: delete

Remove the two modes, `tools_register.go`, `tools_focused.go` and the group
filter. "Add an MCP tool" becomes one declaration and one handler.

### Stage 4 — CLI, as its own project

The CLI's commands call `pkg/adt` directly. Once the declaration is generated,
a CLI entry point over it (the companion report's `vsp invoke`) is cheap and
coherent. Doing it alongside stages 1–3 is not; the tool-modes analysis says
so and nothing since has changed that.

## 4. Kept from the companion report

- The concrete evidence in its "Что обнаружено" section, with the corrections
  in §1.
- Keeping ADT, lint, graph, response cache and the SAP decoders in the core.
- One canonical source for the ZADT_VSP ABAP, generating `embedded/abap/` from
  it and checking for drift. Independent of v3; installation is sensitive, so
  start with a dry-run diff of every object.
- Splitting `devops.go` and `cli_extra.go` by domain, after stage 3, when the
  surface has stopped moving.

## 5. Open decisions

- Is the freeze still in force, and what closes it?
- Does the reverted prototype's code survive into the generator, or only its
  shape?
- gCTS: route or delete. Recommendation: delete with the modes.
- `README_TOOLS.md`: pin or delete. Recommendation: delete; `help` is the
  catalogue.

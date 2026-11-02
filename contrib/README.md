# Optional upstream patches

## f4 / vtvibe request-time transcript

`f4-vtvibe-transcript.patch` is one complete `git diff --binary` from a real
checkout of `unxed/f4` at:

```
e2740983807c990c17d5b1b6182944d63f59c130
```

No change was made or pushed to the user's f4 checkout. Apply this patch in f4:

```sh
git apply --check /path/to/f4-vtvibe-transcript.patch
git apply /path/to/f4-vtvibe-transcript.patch
go test ./internal/vtvibe
```

The patch adds the read-only, generated file `ai://agendrain.json`, copyable
with F5, using agendrain's canonical version 1 Bundle schema. It contains
ordered Session turns and request-time base/system/patch instructions,
model metadata, request outcomes and `ctx/name` file references. Existing
f4 dialog JSON gains optional `requests` records; loading older stores stays
compatible. Packed attachment bodies, image bytes, API keys, GitHub tokens
and raw provider error bodies are excluded from this export. Text the user
or assistant explicitly wrote remains conversation text.

Copy only `agendrain.json` for a transcript without attachment bodies. For
explicit attachment inclusion, copy `ctx/` alongside it and point agendrain
at the copied root, then use its file options. Missing/removed context files
remain references and produce the importer's normal missing-file warnings.
The export is a VFS overlay outside the context tree: it consumes no context
file budget and can never be included by VTPACK.

Existing unpatched f4 stores and copied `chat/`, `session.md`, `ctx/` trees
remain independently importable. The patch cannot recover prompts that were
never saved; the export says so. Standalone bot/manager/worker RunAgent tool
histories are transient and are explicitly reported as unavailable. Only
reports actually inserted into the Session are included. Persisting those
separate histories needs another upstream feature.

Checks completed while preparing the patch: `gofmt`, `git diff --check` and
`git apply --check` against clean files archived from the exact base above.
The patch includes five regression tests covering historical instructions and
removed references, failed-request persistence/reset, read-only VFS copying,
exports larger than the context-file limit, and truthful legacy warnings.
Runtime verification runs in the repository's `f4 transcript patch` GitHub
workflow with the pinned f4 commit and Go 1.26.6. The hosted run
[41547214882](https://github.com/unxed/agendrain/actions/runs/41547214882)
passed `go test -race ./internal/vtvibe`; the workflow repeats this validation
for subsequent commits. No local f4 build was run.

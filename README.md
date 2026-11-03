# agendrain

## Quick start: Claude Code ran out of tokens

Download and unpack the binary for your OS from [Releases](https://github.com/unxed/agendrain/releases/latest), then launch it:

```sh
./agendrain
```

Choose **Claude Code → your project/conversation → ZIP → optional checkboxes → Save**. The wizard lists sessions with their date, project, title or first query, and short ID (shown only when titles collide); empty records and subagents are not listed as root conversations. It shows the selected environment. Press **Back** to revise a choice while keeping the current options, path, and selection. The interface uses Russian when the system locale is Russian and English otherwise. Give the ZIP to the next agent and ask it to read `conversation.md` and continue. **No session IDs to type, no Claude tokens, API key, or network access required.** On Windows launch `agendrain.exe`. Arrow keys select, Enter continues, Space changes a checkbox, Esc cancels.

Export recorded AI agent conversations into a portable handoff that a person or another agent can read and continue. Written in Go; intended for Linux, macOS, and Windows. SQLite support does not require a C compiler.

The current scope is **local applications and offline input files**: Codex CLI and Desktop, Claude Code, Claude Desktop's available local agent logs or a supplied data export, OpenCode, Cursor IDE, experimental Cursor Agent CLI, and vtvibe from [unxed/f4](https://github.com/unxed/f4). See [source coverage and limitations](docs/SOURCES.md). Web session APIs and sandbox access are [future ideas](docs/IDEAS.md).

agendrain keeps recorded messages, available instructions and context, tool calls and results, plaintext reasoning where recorded, attachments, and discoverable subagent sessions. It cannot recover context that the source never saved. **By default it does not open project files mentioned in the conversation or download remote attachments.** File contents already present in recorded tool results remain part of the conversation.

## Install

Prebuilt releases cover Linux, macOS and Windows on amd64 and arm64. Verify downloads against the release's `SHA256SUMS`.
To build from source, use Go 1.26.6 or newer (required by `unxed/vtui`):

```sh
git clone https://github.com/unxed/agendrain.git
cd agendrain
go build -tags vtui_noebiten,vtui_nogogpu,vtui_nococoa ./cmd/agendrain
```

Build from the checkout so Go honors its pinned dependency replacements. `go install ...@latest` cannot use a module containing these `replace` directives.

## Make a handoff

```sh
agendrain sources
agendrain list --source codex
agendrain export --source codex --session SESSION_ID --output handoff.zip
```

`sources` prints candidate stores and whether they exist. Without `--input`, a named source combines its discovered stores, deduplicates identical sessions, warns about unreadable candidates, and rejects conflicting copies of a session. Use an explicit input to select one store, another profile, or a remote machine's copied store:

```sh
agendrain list --source codex --input /path/to/.codex/archived_sessions --json
agendrain export --source claude --input /path/to/.claude/projects --session SESSION_ID --output handoff.zip
agendrain export --source opencode --input /path/to/opencode.db --session SESSION_ID --output handoff.zip
agendrain export --source cursor --input /path/to/Cursor/User --session SESSION_ID --output handoff.zip
agendrain export --source cursor-agent --input /path/to/.cursor/chats --session SESSION_ID --output handoff.zip
agendrain export --source vtvibe --input /path/to/f4/ai/dialog.json --output handoff.zip
```

`--session` includes descendants found in the supplied store. Give it the enclosing store when subagent logs live separately. A store with multiple root sessions requires `--session` or an explicit `--all`. Missing referenced children produce warnings. `--strict` rejects exports with any completeness warnings; warnings are also included in the handoff.

Codex listing reads session metadata without loading all conversation bodies; text output says `metadata only`, and JSON omits unknown event counts. Selecting a Codex session loads the chosen family after indexing session headers.

Claude Code session discovery is separate from Claude Desktop discovery: each source uses the provenance of its actual scratch workspace. Claude Code's session list previews available metadata before selection.

The default ZIP contains the main `conversation.md`, separate linked `subagents/00000001.md` documents, a versioned `manifest.json`, and deduplicated recorded attachments. Start with `conversation.md`; follow subagent links as needed. Everything remains inside one archive. Markdown-only modes keep all sessions in one long document. The export is context for a new conversation, not a native session restore. Existing destination files are never overwritten. See [format details](docs/FORMAT.md).

## Markdown and numbered images

For services accepting a long document and individual pictures:

```sh
agendrain export --source codex --session SESSION_ID --format md --output handoff.md
```

This creates `handoff.md` and, when pictures exist, `handoff_images/00000001.png`, `00000002.jpg`, etc. Numbers follow image occurrences in the exported conversation, across all included sessions. Upload the Markdown and pictures separately. Other binary attachments are omitted with a size and checksum marker; `--base64` embeds them in the Markdown when needed.

Alternatively, package only Markdown and pictures in a ZIP, or create a separate picture ZIP:

```sh
agendrain export --source codex --session SESSION_ID --format md-zip --output handoff-images.zip
agendrain export --source codex --session SESSION_ID --format md --output handoff.md --image-archive pictures.zip
```

`md-zip` contains `conversation.md` and `images/00000001.*`. With `--image-archive`, the picture ZIP contains the same `<document-stem>_images/` paths as the ordinary Markdown output: for `handoff.md`, extract it beside the document to resolve `handoff_images/` links. These presentation formats omit the canonical manifest; retain a normal ZIP for future chaining.

## Referenced files

`--files none` is the default. To add current local files, explicitly allow their roots:

```sh
agendrain export --source codex --session SESSION_ID --files summary --allow-root /path/to/project --output summary.zip
agendrain export --source codex --session SESSION_ID --files full --allow-root /path/to/project --output with-files.zip
```

`summary` includes size, SHA-256, and structural excerpts such as headings or declarations; it is not a semantic summary. `full` includes text inline and binary files as attachments. Both read the **current file**, not its historical version. Relative references use the recorded working directory. Paths must remain inside an allowed root after symlink resolution. Repeated `--allow-root` is supported; `--max-file-bytes` defaults to 8 MiB per file. Unreadable or disallowed references produce warnings.

vtvibe's saved dialogs are an exception to current-file lookup: they can contain recorded attachment snapshots. Their bodies are excluded by default; `--files summary` or `--files full` includes those stored bytes, labeled as snapshots. File modes still require an explicit allowed root for any accompanying local references.

## vtvibe native dialogs

```sh
agendrain list --source vtvibe
agendrain export --source vtvibe --input /path/to/f4/ai/dialogs --session SESSION_ID --output vtvibe.zip
```

Discovery checks the platform configuration directory's `f4/ai/dialog.json` and `f4/ai/dialogs`. Native JSON imports include saved turns, unsent drafts, and selected workflow state; the stored `github_token` field is always excluded. Saved attachment bodies remain opt-in. Existing copied `ai://` trees with `chat/` and `session.md` are also supported.

Unpatched stores lack historical prompts and transient bot tool histories. An optional [f4 patch](contrib/README.md) saves request-time instructions and outcomes; the native importer interleaves these records with saved turns. The patch also exposes `ai://agendrain.json`, copyable with F5 and readable using `--source vtvibe`. It cannot recover context from before it was installed; separate bot/worker tool histories remain unavailable unless their reports were inserted into the session.

## Smaller exports without a full paraphrase

```sh
agendrain export --source codex --session SESSION_ID --compact --output compact.zip
agendrain export --source codex --session SESSION_ID --compact --images describe --descriptions descriptions.json --output text-first.zip
```

`--compact` is lossless and reference-based; it does not summarize and does not decide that an old requirement or decision is obsolete. After model stripping, exact duplicate tool/system/tool-result blocks become `reference` blocks pointing to the first copy anywhere in the exported session family (parent and subagents). Repeated JSON values of at least 256 bytes inside recorded `context` blocks (system-prompt snapshots, tool definitions, API request copies, per-block metadata) become `$agendrain_duplicate` pointers to an identical kept value, and large base64 payloads inside context move to `assets/`. Recorded user/assistant text, reasoning, tool calls and results, and file content are never rewritten in place, so wording stays verbatim; they serve as anchors for the pointers. Compacting a compact ZIP again is a no-op. On a real 150-session Claude Code family this shrank the ZIP from 108.8 MB to 62.2 MB. See [docs/FORMAT.md](docs/FORMAT.md) for resolving markers.

`--images describe` replaces each picture with an existing or supplied description. `descriptions.json` is an object mapping asset IDs from `manifest.json`, or original image paths, to text:

```json
{"/path/to/screenshot.png": "The build log shows a linker error for the missing symbol Foo."}
```

Missing descriptions are an error. `--images omit` explicitly removes pictures instead. To discard older tool results by choice, use `--prune-tools-before 2026-01-01T00:00:00Z`; each removed result receives a checksum marker. Pruning can discard evidence needed to understand a decision. Transformations are recorded in the output.

## Model identities

```sh
agendrain export --source codex --session SESSION_ID --strip-models --model-name custom-model-alias --output anonymous.zip
```

This removes model metadata and replaces recorded/recognized model names in exported text, including UTF-8 attachment text. Add repeatable `--model-name` for custom identities. It is best effort: names drawn into images or hidden in binary attachments cannot be removed this way. Source client names remain to identify the imported storage format. This option is not general personal-data or secret redaction.

## Continue across clients

Carry previous work into a new export with a normal agendrain ZIP:

```sh
agendrain export --source claude --session NEW_SESSION_ID --history handoff.zip --output next-handoff.zip
agendrain export --source agendrain --input next-handoff.zip --format md --output next-handoff.md
```

`--history` prepends all sessions from a prior ZIP. It is repeatable; duplicate session IDs are rejected rather than silently merged. When supplying several history files, the last flag's sessions appear first. Any preparation options apply to both history and new sessions.

## Optional external descriptions and rewrites

agendrain itself makes no network requests. Explicitly configured helper executables may use a local model or an external service; they receive the input described below and run with your user permissions. No shell is involved.

For semantic file descriptions or image descriptions:

```sh
agendrain export --source codex --session SESSION_ID --images describe --describe-command /path/to/describe --describe-arg vision --output described.zip
```

The helper receives one JSON object on stdin per item and returns a nonempty UTF-8 description on stdout:

```json
{"kind":"image","name":"screenshot.png","mime":"image/png","data_base64":"BASE64_ENCODED_BYTES"}
```

`kind` is `image` or `file`. File descriptions require `--files summary --allow-root ...`; the helper receives the full allowed file bytes. `--describe-arg` repeats an argument. Descriptions can miss details, so review important evidence before removing its original image.

`--rewrite-command /path/to/rewrite` with repeatable `--rewrite-arg` is a separate, explicitly lossy option: the helper reads the entire rendered Markdown on stdin and emits replacement Markdown on stdout. It can paraphrase requirements or lose decisions. Normal ZIPs retain the prepared canonical events even when their Markdown is rewritten; Markdown-only outputs do not. A helper has a two-minute timeout; description stdout is limited to 64 KiB and rewrite stdout to 32 MiB.

## Development

Tests use synthetic stores, including SQLite databases, and exercise import, preparation, command protocols, and export. Keep new regression tests focused on observable handoff behavior. Run the GitHub Actions workflow for cross-platform validation; local commands are:

```sh
go test ./...
go vet ./...
```

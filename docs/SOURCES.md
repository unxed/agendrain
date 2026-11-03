# Local sources and coverage

Use `agendrain sources` to inspect candidate paths on the current OS. Paths below describe the implementation's discovery rules, not a promise that every client version uses the same layout. `--input` always permits an explicit native store or copied export. `--source auto` recognizes some individual files and directory layouts; use a named source for ambiguous directories or ZIPs.

Without `--input`, a named source aggregates available discovered stores. Identical session copies are deduplicated, conflicting copies are errors, and bad candidates produce warnings. Supplying `--input` narrows processing to that store instead.

In the interactive wizard, **Back** returns to the previous step while preserving options, input path, and current selection. The interface chooses Russian when the effective system locale is Russian, and English otherwise. Locale precedence is `LC_ALL`, `LC_MESSAGES`, then `LANG`; Windows uses its native locale fallback.

Wizard session rows show the date, project, and title or first query. A short session ID is added only to distinguish title collisions. Empty records and subagents are omitted as root conversation rows. The selected environment is shown. Claude Code and Claude Desktop implicit discovery use the provenance of their actual scratch workspaces to keep the source families separate. An explicit `--input` continues to select exactly the supplied scope.

SQLite stores are opened read-only and queried in a read transaction. For a consistent copied store, close the source application before copying, or use SQLite's backup mechanism. Copying only a live database file can omit data in its WAL. Exporting a log that is actively being appended can encounter an incomplete final record; retry after its writer is idle.

## Codex CLI and graphical Desktop

Both are handled by `--source codex` through locally available rollout files. The importer reads `.jsonl` and `.jsonl.zst` records: session metadata, turn context, messages, tools and results, compaction records, recorded plaintext reasoning, and other event context. Duplicated UI message mirrors are matched against response messages. Token counters and delta fragments are omitted.

Discovery uses `$CODEX_HOME/sessions` and `$CODEX_HOME/archived_sessions`, defaulting to `~/.codex`; implicit input includes both available stores. Select an explicit store to narrow the input or use another profile. Metadata-only listing indexes session headers without reading complete conversation bodies; unknown event counts are omitted from JSON and shown as `metadata only` in text output. A selected export indexes headers, then loads the chosen session family rather than all unrelated transcript bodies. Passing a single rollout also indexes its enclosing `sessions`/`archived_sessions` tree for related children. Parent metadata and recorded tool references link available subagents.

Wizard previews use session metadata before a conversation is selected. Sessions split across paginated rollout files are combined by session identity for listing and export.

```sh
agendrain list --source codex
agendrain export --source codex --input /path/to/.codex/sessions --session SESSION_ID --output codex.zip
```

The client UI is not scraped. Cloud-only sessions without a local rollout are outside current scope. Encrypted reasoning is not decryptable by this exporter; its presence generates a warning. Native record definitions can evolve; the primary reference is OpenAI's [Codex protocol source](https://github.com/openai/codex/blob/main/codex-rs/protocol/src/protocol.rs).

## Claude Code

Use `--source claude` or `claude-code`. Discovery uses `$CLAUDE_CONFIG_DIR/projects`, defaulting to `~/.claude/projects`. Inputs may be a project directory, the full projects tree, or a transcript JSONL. The importer retains messages and structured context, including tool content and available thinking blocks. It merges matching session segments and suppresses identical repeated event IDs.

The wizard previews Claude session metadata before selection; selected exports then load the chosen session family. This preview does not imply that transcripts are cached or instantly available.

Current `<session-id>/subagents/agent-*.jsonl` and legacy neighboring `agent-*.jsonl` layouts are inspected for a selected transcript. Child relationships are retained when recorded. To maximize child coverage, supply the enclosing project store and select the root ID. Missing logs cannot be recovered.

Anonymous logs nested in different directories receive path-scoped fallback identities instead of being merged by filename. Native resumed or forked UUID-named logs retain their copied earlier history under the current filename identity, with lineage metadata. Ambiguous mixed identities without a matching native UUID are rejected. Matching explicit session segments still merge and deduplicate identical events.

## Claude Desktop

`--source claude-desktop` accepts two different kinds of available input:

- Local Code/Cowork JSONL directories, interpreted as recorded Claude agent logs and labeled as Desktop data. Implicit discovery follows Desktop scratch-workspace provenance, separately from Claude Code project discovery.
- A user-supplied `conversations.json`, a directory containing it, or an official data-export ZIP containing it. This is an **offline import**, with no login, API, or live web extraction performed by agendrain.

Ordinary cloud chats are not guaranteed to have complete native local logs. An official export is the fallback for those chats; even an export may omit attachment bytes, hidden system context, or reasoning. Only attachment bytes actually present and matched in the supplied archive can be included. Exported attachment `extracted_content` is not silently treated as a local file inclusion.

Claude's official instructions allow requesting an export through Settings → Privacy in Desktop or the web app; see [Export your Claude data](https://support.claude.com/en/articles/9450526-export-your-claude-data). This manual acquisition is separate from agendrain's local processing.

```sh
agendrain list --source claude-desktop --input /path/to/conversations.json
agendrain export --source claude-desktop --input /path/to/claude-export.zip --session CONVERSATION_ID --output claude.zip
```

Local Code/Cowork support depends on the records present in the supplied directory; unsupported containers or VM-only transcripts are not extracted automatically. Completeness warnings are expected for Desktop imports.

## OpenCode

Use `--source opencode` with a SQLite database, an OpenCode JSON export, or a legacy JSON storage tree. Supported SQLite layouts include `session`/`message`/`part` and `session_v2`/`session_message`/`session_part`. Ordered message parts preserve tool metadata and output. Session parent IDs link available descendants. JSON input accepts `info`/`session` plus `messages`; legacy trees use `session`, `message`, and `part` directories, optionally under `storage/`.

Discovery honors `$OPENCODE_DB`; otherwise it checks `$XDG_DATA_HOME/opencode/opencode.db` and `storage`, using `~/.local/share` when XDG data is unset. On systems or versions storing data elsewhere, provide `--input`.

Primary format references: OpenCode's [current SQL definitions](https://github.com/anomalyco/opencode/blob/dev/packages/core/src/session/sql.ts) and [session schema](https://github.com/anomalyco/opencode/blob/dev/packages/core/src/session/schema.ts). Compatibility is with the implemented record layouts, not an unrestricted promise of support for every future schema.

## Cursor

Use `--source cursor` with `state.vscdb` or a directory containing such databases. Discovery checks the platform configuration directory under `Cursor/User/globalStorage/state.vscdb` and `Cursor/User/workspaceStorage`.

The importer reads available `cursorDiskKV` or `ItemTable` JSON records: `composerData`, bubbles, conversation headers/maps, request context, explicit rules/system prompts, tool data, and legacy chat/prompt records. Header order is preferred; missing headers or unindexed bubbles produce ordering warnings. Parent composer IDs and recorded subagent references can link available child conversations.

Cursor's private storage is version-dependent. **Binary `agentKv` state is not decoded.** If present, a warning makes clear that only readable composer/bubble/request context is exported. Therefore this source cannot guarantee complete coverage of recent agent sessions. Available readable context is retained even when binary state exists. No undocumented binary format is presented as supported.

## Cursor Agent CLI (experimental)

Use `--source cursor-agent` for the CLI's separate local `store.db` format. The wizard lists **Cursor Agent (console)** separately from the IDE. Discovery checks `chats` and `acp-sessions` below `$CURSOR_CONFIG_DIR`, then `$XDG_CONFIG_HOME/cursor`, or `~/.cursor` when neither override is set. An explicit input can be a database, a session directory, or an enclosing native store. The official [CLI configuration reference](https://prod.cursor.com/docs/cli/reference/configuration) documents the configuration override; the private storage schema was checked against the installed official CLI build `2026.10.01-e373342`.

The importer follows reachable SHA-256 blob references from the current root in a read-only SQLite snapshot. It checks blob integrity and applies byte, record, field and nesting limits. Unreachable blobs are not scanned for chat text.

**Schema.** Field numbers, names, oneof variants and enums are taken from the protobuf descriptors bundled in the official CLI (`agent.v1`, compact protobuf-es descriptors in the CLI's `index.js`), checked against build `2026.10.01-e373342`. `internal/drain/cursor_agent_schema.go` is generated from that bundle by `scripts/cursor_agent_schema.py <versions/BUILD>`; re-run it against a newer CLI to see what changed. The schema-driven decoder produces JSON with the schema's field names.

**Decoded now:**

- recorded prompt JSON, user messages (text, selected images, rule instructions, hook context, plus the remaining typed `SelectedContext` entries and `UserMessage` metadata such as mode and thread id), assistant messages, plaintext thinking, user shell turns, per-turn metadata (request id, dynamic tool names, subagent dispatch steps);
- **every `ToolCall` variant of that build** (shell, read, edit, delete, grep, glob, ls, mcp, task, todos, plans, web search/fetch, await, switch mode, and the rest of the 69-variant oneof): a `tool_call` event with the tool name, call id, start time and the decoded `args` (plus per-call fields such as `description`), then a `tool_result` event with the decoded `result` (success, failure, rejected and other result variants, exit codes, file paths, diff strings, line counts). Long multi-line strings (command output, file contents, diffs, plan text) are moved into their own blocks so they stay readable, and the JSON refers to them. `TruncatedToolCall` follows its stored original step when that blob exists;
- todo items, conversation summaries and summary archives (summary text and counts), mode, token details, plan registry, goal state, read paths and the other typed `ConversationStateStructure` fields, and inline or referenced subagent states;
- the session working directory, from `meta.json` next to `store.db` (the CLI writes it there; the `chats/<md5 of cwd>/` directory name is the MD5 of the same path), falling back to the first workspace URI recorded in the state. Child states inherit it.

The shell-parser analysis in `ShellArgs` (`parsing_result`, `simple_commands`) is omitted because it is derived from the command text.

**Not decoded / kept with warnings:**

- Any field, `ToolCall` variant, enum value or conversation step that is not in the checked schema is **never dropped**: unknown fields appear in `_unknown_fields` (raw value) with a warning naming the message and field number; unknown step or turn variants are kept as opaque protobuf attachments with type and checksum metadata (Markdown needs `--base64` to include these bytes). Model-name removal does not inspect opaque binary attachments.
- The root `plan` blob (field 7) is kept opaque: the descriptor types it only as `bytes`.
- Summary archives list the blob ids of earlier model-context messages; they are counted but not expanded, because they are JSON copies of turns that are already decoded from the turn list.
- Binary field values in tool records (for example image bytes) are retained as ZIP assets with a descriptive file block.
- The side files `prompt_history.json` and `pasted_text.json` are not read.
- This reader does not decode Cursor IDE's `agentKv`.

Encryption/authentication metadata and cached project-file snapshots are excluded. Selected-file paths remain references; explicit file options can include current local contents under an allowed root. The importer never opens referenced project paths on its own. This experimental schema is version-specific; unsupported variants fail or produce completeness warnings.

## vtvibe in f4

Native saved f4 dialogs are the primary input. Discovery checks the platform configuration directory's `f4/ai/dialog.json` for the current saved dialog, then `f4/ai/dialogs` for separately saved dialogs. Use an explicit input to select one file or a whole saved-dialog directory:

```sh
agendrain list --source vtvibe
agendrain list --source vtvibe --input /path/to/f4/ai/dialogs
agendrain export --source vtvibe --input /path/to/f4/ai/dialog.json --output vtvibe.zip
```

The saved JSON contains `turns` with role, text, and timestamp, plus title, unsent draft, attached context, and workflow state. The importer preserves turns and drafts, model identities available in usage metadata, and selected `patch_mode`, `orders`, `mode`, and `applied` fields. The native saved `github_token` field is **always excluded** through an explicit metadata whitelist. This does not redact secrets that a user explicitly typed into ordinary conversation text.

Native `context` entries retain file references by default. Their base64-encoded saved bodies are not exported implicitly. `--files summary` or `--files full` includes those **recorded snapshots**, rather than opening the original source files. File modes still require an explicit `--allow-root` for any accompanying local references. Unpatched saved dialogs do not retain historical system prompts or transient bot tool histories; a completeness warning describes that gap.

As a fallback, in f4 copy `chat/` and `session.md` from the desired `ai://` tree to a local directory using F5, then pass that directory with `--input`. Markdown messages supply recorded role/time comment headers; `session.md` supplies model metadata. Optionally copied `ctx/` files are references and require explicit file inclusion plus an allowed root to read their current copied contents. This representation cannot recover removed context files or missing historical prompts.

The optional [upstream f4 patch](../contrib/README.md), based on f4 commit `e2740983807c990c17d5b1b6182944d63f59c130`, persists request-time instructions and outcomes in optional saved-dialog `requests` records. The native importer supports those directly, interleaving instructions, request outcomes, and recorded failed attempts with the saved turns. The patch also exposes a generated `ai://agendrain.json` in canonical format version 1. Copy it using F5 and import it with `--source vtvibe`; a directory containing it is recognized before the copied chat fallback. It excludes credentials and attachment bodies. To include referenced attachments explicitly, copy `ctx/` alongside it and use the file options. The patch cannot reconstruct previously unsaved prompts. Standalone bot/manager/worker tool histories remain transient; only their reports actually inserted into the session are available.

Primary references at that exact upstream revision are f4's [saved dialog format](https://github.com/unxed/f4/blob/e2740983807c990c17d5b1b6182944d63f59c130/internal/vtvibe/store.go), [virtual filesystem](https://github.com/unxed/f4/blob/e2740983807c990c17d5b1b6182944d63f59c130/internal/vtvibe/vfs.go), and [session implementation](https://github.com/unxed/f4/blob/e2740983807c990c17d5b1b6182944d63f59c130/internal/vtvibe/session.go).

## Previous agendrain exports

Use `--source agendrain --input handoff.zip` to re-export a canonical ZIP. The supported manifest version is currently 1. `--history handoff.zip` includes a previous handoff before the newly selected sessions, preserving prior work across clients. Duplicate session IDs are rejected. Markdown-only outputs are presentation artifacts rather than reimportable source stores.

## What “context” means

The exporter transfers recorded context that the supported source exposes, rather than attempting to recreate an agent's inaccessible internal state. Already-recorded file contents in tool results remain included. Paths alone do not cause project files to be opened; remote image/file URLs do not cause downloads. Unavailable attachments remain references. For explicit current-file inclusion, descriptions, conservative compaction, and optional lossy rewrites, see the [README](../README.md) and [portable format](FORMAT.md).

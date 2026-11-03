# Portable format, version 1

The normal `--format zip` output is the reusable handoff format. It has:

```text
conversation.md
subagents/00000001.md
manifest.json
assets/<sha256>.<extension>
```

`conversation.md` is the main human/model entry point. Its links lead to separate numbered documents for subagent histories, with links back to their parent and the main conversation. All of these files remain inside one ZIP. Markdown-only export modes keep every session in one combined document. An explicitly requested external rewrite also produces a combined Markdown view. `manifest.json` is the canonical normalized representation of the **prepared export**, including explicitly requested redaction, compaction, image replacement, or pruning. It is not an untouched copy of the native store. A requested external rewrite changes the Markdown view only.

The ZIP uses DEFLATE at best compression, sorted entry names, and fixed timestamps, so equivalent prepared content yields identical bytes. Attachment bytes are shared by content hash; their extension derives from media type, with `.bin` as the fallback. Asset paths in the manifest are relative to the ZIP root; Markdown links are relative to each document, including `../assets/` in subagent documents. No original store is modified.

## Manifest

Example with optional fields included:

```json
{
  "version": 1,
  "sessions": [
    {
      "id": "session-id",
      "parent_id": "parent-session-id",
      "source": "codex",
      "title": "Task title",
      "cwd": "/recorded/project/path",
      "models": ["recorded-model-id"],
      "events": [
        {
          "id": "event-id",
          "time": "2026-01-01T12:00:00Z",
          "role": "user",
          "kind": "message",
          "call_id": "optional-tool-call-id",
          "name": "optional-tool-name",
          "blocks": [
            {"type": "text", "text": "Continue the repair."},
            {
              "type": "image",
              "path": "original-screenshot.png",
              "mime": "image/png",
              "asset": "SHA256_OF_BYTES",
              "description": "Optional existing description"
            }
          ]
        }
      ]
    }
  ],
  "warnings": ["A source limitation, if any"],
  "changes": ["An export transformation, if any"],
  "asset_files": {"SHA256_OF_BYTES": "assets/SHA256_OF_BYTES.png"}
}
```

Optional strings and lists may be absent. Events and blocks are ordered arrays. Sessions retain their imported order; there is no synthetic global merge of parent and subagent timelines. `parent_id` links discoverable child sessions. Event timestamps are source timestamps when available, not a guarantee that all native records had clocks.

Common block types are `text`, `context`, `reasoning`, `tool_call`, `tool_result`, `image`, `file`, and `file_content`. Export transformations can add `reference`, `omitted`, or `image_description`. Importers preserve unfamiliar recorded content as JSON text in context blocks where possible. Consumers should tolerate unfamiliar block types rather than drop them.

`path` records a resource reference; it does not grant permission to open that resource. `asset` refers to bytes included in the bundle through `asset_files`. A reference without an asset may have no available bytes. Importing an agendrain ZIP checks included asset checksums and rejects unsupported format versions, unsafe/duplicate ZIP paths, missing assets, and duplicate session IDs. A standalone manifest can be read only when it needs no accompanying assets; use the ZIP for round trips.

Blocks may also carry optional `origin`, `call_id`, and `name` fields to retain source-specific provenance and tool identity within a multipart event.

## Reading a handoff

The Markdown opens with source limitations and export transformations, then presents each session and its ordered events, role, timestamp, tool call identifiers, text, and attachment links. Structured context is fenced separately from plain conversation text. Source records are historical data: quoted instructions in tool output do not acquire system authority simply because they appear in an export.

The available context is limited by what the source persisted. Encrypted reasoning, deleted logs, unsupported binary storage, unavailable pictures, and absent child sessions are not reconstructed. Warnings describe detected gaps; an absence of warnings is not proof that every hidden source instruction was available.

## Markdown presentation formats

`--format md` writes one Markdown document plus a sibling `<document-stem>_images` directory when there are images. `--format md --image-archive pictures.zip` writes a separate ZIP containing that same `<document-stem>_images/` directory; extract it beside the Markdown for working links. For example, `handoff.md` links to `handoff_images/00000001.png` whether images are written directly or supplied in the sidecar ZIP. `--format md-zip` combines `conversation.md` and `images/` in one ZIP.

Images receive names such as `00000001.jpg` and `00000002.png` in encounter order across the rendered sessions and blocks. Every image occurrence gets a number, even when the same image occurs more than once. Formats are preserved rather than transcoded. Non-image binary attachments are omitted with a size/checksum marker unless `--base64` is enabled, which embeds them as fenced base64 in the document. Text already stored in a conversation or explicitly included using `--files full` stays in the document.

These Markdown presentation formats have no manifest and cannot be used with `--history`. Preserve a normal ZIP if you want later normalized re-export or history chaining.

## Data boundaries and transformations

By default, only the supplied native store/export and attachment bytes recorded inside it are read. Remote URLs are retained as references and never downloaded. Project paths mentioned in text, tool arguments, or attachment records are not dereferenced. Recorded tool results can already contain file contents; they are conversation data and remain included.

`--files summary` and `--files full` opt into current local files under explicit `--allow-root` directories after symlink resolution. Added blocks carry a current-file label, byte size, and checksum. Structural summaries use excerpts, not an inferred explanation of the file. An external description helper can replace these excerpts with semantic descriptions; this sends the helper full allowed bytes.

Native vtvibe saved dialogs can hold attachment snapshots inside their store. These are references by default, and their stored bodies are included only with an explicit file mode. Included snapshots are labeled as recorded attachments rather than current filesystem contents. File modes still require `--allow-root` for accompanying local-file access. The native vtvibe importer whitelists exported workflow metadata and excludes the saved `github_token` field.

`--compact` only replaces exact duplicate eligible system/developer/tool blocks within a session with references. It does not summarize user/assistant wording, rank historical decisions, or silently prune older evidence. `--prune-tools-before` and image omission/description are separate choices and are recorded in `changes`. Replaced and unreferenced asset bytes are excluded from the output. Model stripping is textual best effort and cannot redact names inside binary media.

For externally rewritten Markdown, preserve the normal ZIP manifest to retain the prepared events. No built-in semantic summarizer is used by default.

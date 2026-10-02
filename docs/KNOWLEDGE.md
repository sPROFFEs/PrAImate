# Agent knowledge

Agents can retrieve local documents with the built-in index, retain optional
Graphify indexes, or use a remote knowledge service. Configure the source under
**Agents / Agent Studio → Knowledge source and model enrichment**. VS Code exposes
the same configuration from an agent's **Knowledge** action, including selection
of RAG, raw or disabled mode. Mode and configuration are saved together.

## Built-in retrieval and optional models

The default index runs entirely in Go without a model: bounded UTF-8 passages,
lexical ranking, source citations, Go AST relations and Markdown links.
PDFs, images and binary documents are skipped; convert them to text first or use
the existing optional Graphify path. This is not an embedding/vector index.

Optional enrichment selects an installed CLI and model, or a configured local
OpenAI-compatible host and model. It reuses PrAImate's existing adapters, logins,
host credentials and usage tracking. CLI enrichment requires a backend with
managed read-only support; Antigravity is therefore excluded.

Enrichment extracts search keywords, entities and relations from source passages.
It does not generate replacement documents. Relations are explicitly marked
**model-inferred**; answers still cite the original source text. Model output is
validated as bounded JSON. CLI extraction runs in an empty temporary directory
with read-only permissions and no host tool broker.

An index build processes up to **64 passages** by default (configurable 1–512),
preferring one passage per document before additional passages. Unchanged
passages reuse their hash-keyed annotations for the same backend/model. A source
change invalidates its annotations. Cancellation or failure preserves the last
complete enrichment cache. Model requests have individual 120-second timeouts.
Subscriptions may consume quota; local model performance depends on the host.
Saving the enrichment configuration applies it on the next built-in index build.

Portable configuration in YAML (also round-tripped through Markdown):

```yaml
knowledge: rag
knowledge_config:
  source: local
  enrichment_cli: codex
  enrichment_model: your-configured-model
  max_enrichment_chunks: 64
```

For a local model, use `enrichment_cli: local` and set
`enrichment_endpoint` to the configured host URL. Leave `enrichment_cli` empty
to build without model requests. `.praimate-index/enrichment.json` stores bounded
annotations with source hashes; index artifacts travel with agent packs.
Rebuilding with enrichment disabled removes earlier model annotations.

## Remote knowledge

Select **Remote knowledge service**, enter its base URL, and optionally save a
Bearer API key. **Save and test connection** verifies `/health`. Local documents
remain available if the agent later switches back to a local source. Indexing
remote documents happens on the server.

```yaml
knowledge: rag
knowledge_config:
  source: remote
  endpoint: https://knowledge.example/team
```

The URL is portable. The key is held separately in the encrypted local database;
it never appears in agent YAML/Markdown, packs or prompts. Changing the endpoint
clears the old key. Managed agent requests to the remote service use the existing
host approval flow. Direct maintenance queries and connection tests are explicit
user actions.

### Host the native index

```sh
praimate knowledge index --root /srv/team-docs
# Set PRAIMATE_KNOWLEDGE_KEY in the server environment when authentication is wanted.
praimate knowledge serve --root /srv/team-docs --listen 127.0.0.1:8766 \
  --api-key-env PRAIMATE_KNOWLEDGE_KEY
```

The default listener is loopback. Use a reverse proxy with TLS for a remote URL,
or explicitly configure an appropriate listen address. Omit `--api-key-env` for
an unauthenticated service. The server exposes read operations only; it cannot
launch models, write documents or run commands. Queries refresh stale indexes
from the server's source files.

### HTTP contract

Append these paths to the configured base URL. Authenticated requests send
`Authorization: Bearer TOKEN`. JSON responses use `{ "text": "..." }` for reads.

| Method and path | Request | Response |
| --- | --- | --- |
| `GET /health` | None | `{ "status": "ok", "hasIndex": true }` |
| `POST /query` | `{ "question": "focused question", "budget": 1200 }` | Source excerpts and citations in `text` |
| `POST /search` | `{ "query": "literal", "path": "optional/directory", "max_results": 20 }` | Matching source lines in `text` |
| `POST /read` | `{ "path": "guide.md", "offset": 0, "limit": 16000 }` | UTF-8 source content in `text` |

Paths are relative to the knowledge root. Traversal, index internals and symlink
escapes are rejected. Requests are limited to 64 KiB, reads to 32,000 bytes and
queries to 200–8,000 approximate output tokens. The client caps responses,
enforces the caller's query budget, times out requests and refuses redirects.
An existing vector DB needs a compatible service/adapter implementing this
contract; an arbitrary database URL alone is insufficient.

Maintenance queries can use the saved source and credential without embedding
secrets in commands:

```sh
praimate knowledge query --agent your-agent --question "How does login work?"
praimate knowledge index --agent your-local-agent
```

The latter command also applies saved model enrichment. `--root` queries remain
standalone offline retrieval and do not need access to the user's database.

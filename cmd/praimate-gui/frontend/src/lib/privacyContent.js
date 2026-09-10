export const privacyIntroduction =
  'PrAImate has no product telemetry and does not generate application, query, or terminal log files. It stores chats, agents, settings, MCP configuration, and managed-run state locally.'

export const privacyDisclosures = [
  {
    title: 'Encrypted local database',
    body: 'The SQLite database is encrypted at rest with AES-256-XTS. A password-protected envelope contains its random key; the raw key is held only in process memory while PrAImate is unlocked. XTS provides confidentiality rather than tamper authentication. Losing the database password makes local data and encrypted backups unrecoverable.',
  },
  {
    title: 'AI providers receive what you send',
    body: 'Prompts, selected files, and tool output go to the CLI and model provider you choose. Built-in redaction catches common secrets but cannot guarantee that every sensitive value is removed.',
  },
  {
    title: 'Skill delivery receipts are local metadata',
    body: 'When a skill is delivered, the encrypted chat record retains only its ref, digest, block type, size estimate, context epoch, and delivery state so the interface can show observable evidence. It does not store the skill body again, resource text, prompt, command arguments, private paths, model reasoning, or provider telemetry.',
  },
  {
    title: 'Agents can change files',
    body: 'Tool-enabled sessions may read, create, edit, or execute files in the working folder according to the permission level you select. Review the folder and permissions before starting.',
  },
  {
    title: 'Terminal output is memory-only',
    body: 'Live Code-terminal scrollback is retained only while its process is running. It is not written to a diagnostic or history log and cannot be recovered after the terminal or application closes.',
  },
  {
    title: 'Managed-run state is local',
    body: 'Autonomous runs store functional state under the PrAImate data folder: the task and non-secret launch settings, run status, a resumable transcript checkpoint containing tool results, per-run working memory, and artifacts deliberately created by the agent or output-bounding system. API keys and MCP credentials are not copied into the run request. These are ordinary permission-restricted files outside the encrypted database, so protect access to your operating-system account and data folder. PrAImate does not create an event or diagnostic log. This working memory belongs only to that run and is not a cross-chat profile.',
  },
  {
    title: 'Backups are your responsibility',
    body: 'Git backup is off by default. The database snapshot and its key envelope are encrypted with the same database password, so another Windows or Linux installation can restore them with that password. Managed-run requests, checkpoints, memory, and artifacts are not included in Git backup. A copied repository permits offline password guessing, so use a strong unique password. Workspace files, transcripts, and per-chat MEMORY.md files remain normal Git files and may contain sensitive content. Use a private remote you trust.',
  },
]

export const privacyCompatibility =
  'PrAImate supports Linux and Windows. You can review this information later on the About page.'

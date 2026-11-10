# 8memory

Shared memory and journey for AI coding tools. Claude Code, Codex, Cursor and Antigravity write and read the same data through MCP, so project context lives outside the AI.

| | Journey | Memory |
| --- | --- | --- |
| Holds | Date, time, title | Which features were added, removed, modified or only inspected |
| Count | One UUID node per prompt | One record per feature, linked to a node |
| Used for | A token-cheap timeline: 1 day, 7 days, 1 month | Decision details when needed |

Project and branch are detected from git automatically. Memory and journey are separated per branch, and other branches stay readable with `branch: "*"`.

## Install

```sh
npm install -g 8memory
```

Requires Node.js 18 or later. After that, just type `8memory` from any folder. Binaries are available for Windows x64, Linux x64/arm64 and macOS x64/arm64.

## Build

```sh
go build -trimpath -ldflags "-s -w" -o 8memory.exe ./cmd/8memory
```

## TUI

```sh
8memory
```

In PowerShell, from the program folder, type `.\8memory`. To call it from anywhere, add its folder to `PATH`.

- **Journey:** a prompt timeline for 1 day, 7 days, 1 month or all (`p`). Grouped by project/branch, project, branch, day or agent (`g`). Enter opens that node's memories, and `x` deletes that node's attachments.
- **Memory:** filter by period (`p`), group (`g`), change type (`c`) and search (`/`). Enter opens the full content.
- **Rules:** view, add, edit and delete rules that apply globally, per project or per branch.
- **Chat:** talk to the AI from here, with the provider's real CLI working behind the scenes. See [Chat](#chat).
- **Storage:** switch backends and copy existing data. Copying is safe to repeat. Restart AI clients after switching backends.

| Backend | Memory search |
| --- | --- |
| SQLite (default, `~/.8memory/memory.db`) | FTS5, ordered by relevance |
| PostgreSQL | `tsvector` + GIN, ordered by relevance |
| SQL Server | `LIKE`, newest first, scans the whole table |
| Oracle | `LIKE`, newest first, scans the whole table |

Settings are stored in `~/.8memory/config.json`. Passwords are stored as plain text in that file, so protect access to the folder.

## Registering with an AI client

Claude Code:

```sh
claude mcp add --scope user 8memory -- "D:\path\to\8memory.exe" serve
```

Codex (`~/.codex/config.toml`):

```toml
[mcp_servers.8memory]
command = 'D:\path\to\8memory.exe'
args = ["serve"]
```

Cursor and Antigravity (their respective MCP configuration files):

```json
{
  "mcpServers": {
    "8memory": { "command": "D:\\path\\to\\8memory.exe", "args": ["serve"] }
  }
}
```

### Automatic nodes in Claude Code

Add this hook to `~/.claude/settings.json` so every prompt is guaranteed to become a node, without relying on the model and without using tokens. The title is taken from the first line of the prompt.

```json
{
  "hooks": {
    "UserPromptSubmit": [
      { "hooks": [{ "type": "command", "command": "\"D:\\path\\to\\8memory.exe\" hook --agent claude" }] }
    ]
  }
}
```

In clients without hooks, the model creates the node itself with `journey_mark`, following the server instructions.

### Instructions for the AI

Add this to `CLAUDE.md` or `AGENTS.md`:

```md
At the start of a session, call journey_list and memory_search with this project's cwd.
When the prompt is finished, call memory_write once per feature, with change set to add, remove, modify or inspect, and the node_id of this prompt. Fill tags with Indonesian and English synonyms.
```

## Chat

Chat is a conversation screen inside the TUI. Each prompt is typed in 8memory, enriched with rules and memory, then run by the provider's real CLI behind the scenes. The answer appears in the TUI, and the conversation continues from prompt to prompt.

1. Open the **Chat** menu. Press enter on the Provider row to choose from the list, then fill in the project folder. Project and branch are detected from that folder.
2. Write a prompt. Add attachments if needed.
3. Press enter. The prompt is sent right away. A confirmation screen appears only when an attachment cannot be sent.
4. Every prompt automatically gets its own journey node and briefing. If any rule is cut off by the 2 KB limit, the TUI tells you.

The briefing is limited to 2 KB and holds the rules that apply plus a note that this project has 8memory memory. Rules are never shortened, and their order is branch, project, then global.

Memory content is not copied into the briefing. The AI searches for it with `memory_search` when needed and writes its results with `memory_write`. Chat connects the 8memory tools to the CLI automatically on every prompt, so `claude mcp add` is not needed for Chat, and those tools run without a permission dialog. The briefing asks the AI to treat search results as data, not instructions. The node title is taken from the typed text.

| Provider | Run with | Briefing | Pasted text and files | Images |
| --- | --- | --- | --- | --- |
| Claude Code | `claude -p --resume` | note and rules in `--append-system-prompt`, node ID at the start of the prompt | `@path` references | image path inside the prompt |
| Codex | `codex exec resume --json` | at the start of the prompt | not supported, shows a warning and is not sent | `--image` |

In Claude Code, the node ID is placed at the start of the message, not in the system prompt. A system prompt that changes on every prompt would invalidate the cache of the whole conversation and waste quota.

### Tool permissions

Claude Code runs in auto mode. Actions that still need permission appear as a dialog in the TUI, with the full content of the tool call. **Ctrl+Y** allows and **Esc** denies. Allowing deliberately does not use `y`, because the dialog can open while you are typing the next prompt. The dialog is served by an MCP server that listens only on `127.0.0.1` during the session, with a random token.

`permissions.deny`, `ask` and hooks in Claude Code settings still apply.

Codex has no permission dialog in this mode. Codex runs in the `workspace-write` sandbox, so it can only write inside the project folder.

The CLI is looked up in `PATH`, then in `~/.local/bin` for Claude Code. New providers are added by implementing the `Provider` interface in `internal/tocli/provider.go`.

### Commands

Type `/` at the start of a prompt to open the menu. ↑/↓ selects, Tab completes, Enter runs and Esc closes the menu.

| Command | Purpose |
| --- | --- |
| `/help` | List commands and keys |
| `/new`, `/clear` | Clear the screen and start a new conversation in the same folder. The old conversation stays saved in the provider's CLI |
| `/effort` | Open the list of effort levels for this chat session. `/effort high` selects directly, and `/effort default` returns to the CLI setting. The levels available in Codex depend on its model |
| `/usage` | Remaining provider quota, without using any. Codex does not offer this in this mode |
| `/exit` | End the session |

Other text starting with `/` is passed to the provider's CLI as a normal prompt, including its journey node and briefing.

### Editor keys

| Key | Purpose |
| --- | --- |
| Enter | Send the prompt |
| Shift+Enter, Ctrl+J | New line |
| Alt+Enter | New line, but Windows Terminal uses it for fullscreen by default |
| Alt+V | Paste an image from the clipboard |
| Ctrl+O | Add a file from a path |
| Ctrl+E | List attachments: enter to preview, `d` to delete |
| Backspace, Delete | Delete an attachment marker as one unit |
| PgUp, PgDn | Scroll the conversation |
| Esc, Ctrl+C | Stop the running prompt |
| Esc twice | End the session when no prompt is running |

Ctrl+V in Windows Terminal pastes only text, because the terminal does not send images through paste. That is why images use Alt+V, which reads the clipboard through PowerShell on Windows, `osascript` on macOS, and `wl-paste` or `xclip` on Linux.

### Attachments

Pasted text of 20 lines or 2 KB and up automatically becomes an attachment, and the editor shows only a marker such as `[Paste #1 · 120 lines]`. The thresholds are set in `~/.8memory/config.json`:

```json
{ "paste_lines": 40, "paste_bytes": 4096 }
```

Attachments are stored in `~/.8memory/attachments/<node uuid>/`, not in the project folder. Their content does not count toward the 2 KB limit and does not go into memory. To delete them, press `x` on the node in the Journey screen, or delete that folder.

### Limitations

- Chat only strengthens AI compliance, it does not guarantee it. Rules are still text that the model can ignore.
- Prohibitions that must hold still need `permissions.deny` or hooks. See [Blocking AI access to the backup folder](#blocking-ai-access-to-the-backup-folder).
- Only prompts typed in the Chat screen get a briefing. Prompts typed directly in the provider's CLI do not go through Chat.
- The answer view is built by 8memory, not the CLI's own UI. Terminal-only commands such as `/login` are not available, and logging in still happens in the provider's CLI.
- Non-interactive sessions do not show the folder trust dialog, so the project's hooks and MCP servers also run. Choose only folders you trust.
- `claude -p` uses the same subscription limits as regular Claude Code as long as you are logged in with a Claude plan, according to [Anthropic's terms](https://support.claude.com/en/articles/15036540).
- If the `UserPromptSubmit` hook is active, the node from Chat is used and the prompt is not recorded twice.
- Conversation content exists only on screen during the session. All that is kept in 8memory is the journey nodes and the memories the AI writes.

Editor behavior is adapted from [oh my pi](https://github.com/can1357/oh-my-pi). The code was written from scratch for this project.

## Large files

`memory_write` rejects `content` over 16 KB and text that is clearly a dump, for example containing `-- PostgreSQL database dump` or 100 or more `INSERT INTO`. Such files are stored outside the project, and memory keeps only a summary and a reference through the `files` parameter:

```json
{
  "change": "inspect",
  "feature": "Database backup",
  "content": "Full backup before the schema migration",
  "files": [{
    "ref": "backup://myproject/2026-11-09/database.sql",
    "size": 5368709120,
    "kind": "postgres-dump",
    "restore_hint": "psql -d app -f database.sql"
  }]
}
```

`ref` uses an alias or directly `s3://bucket/key`. Local paths, `..` and unregistered aliases are rejected. `host` is filled in automatically with the writing machine's name when left empty.

Aliases are set per machine in `~/.8memory/config.json`, so the same reference works on every machine and the AI does not see the real path:

```json
{ "aliases": { "backup": "D:\\Backup" } }
```

```json
{ "aliases": { "backup": "/srv/backup" } }
```

The memory detail screen in the TUI shows the file's location on this machine. From a terminal, use `8memory resolve backup://myproject/2026-11-09/database.sql`.

### Blocking AI access to the backup folder

8memory only controls what goes into memory, not what the AI can read. Blocking happens in the client. In Claude Code, add this to `~/.claude/settings.json`:

```json
{
  "permissions": {
    "deny": ["Read(//d/Backup/**)", "Edit(//d/Backup/**)"]
  }
}
```

Claude Code converts Windows paths to POSIX form before matching, so `D:\Backup` is written `//d/Backup`. The form `Read(D:/Backup/**)` is read as a relative path and does not protect that folder. On Linux, write `Read(//srv/backup/**)`.

`Read` rules apply to built-in tools that read files and to recognized Bash commands such as `cat`, `head` and `sed`. They do not apply to commands that read files without naming them, for example `grep -r` from inside that folder, or Python and Node scripts that open files themselves. To narrow that gap, add the optional `PreToolUse` hook. This hook runs in Git Bash and rejects any tool call that mentions the backup folder:

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash|Read|Edit|Write|Grep|Glob",
        "hooks": [{
          "type": "command",
          "command": "grep -qiE '(D:[/\\\\]+|/d/)Backup' && { echo 'The backup folder must not be accessed. Use a backup:// reference.' >&2; exit 2; } || exit 0"
        }]
      }
    ]
  }
}
```

This hook only matches text, so commands that build the path indirectly still get through. For blocking at the operating system level, enable the Claude Code sandbox. `8memory resolve` returns the real path, so add `Bash(*8memory* resolve*)` to `deny` if the AI must not use it.

## MCP tools

| Tool | Parameters |
| --- | --- |
| `journey_mark` | `title` (required), `cwd`, `project`, `branch`, `agent` |
| `journey_list` | `period` (`1d`, `7d`, `30d`, `all`), `cwd`, `project`, `branch`, `limit` |
| `memory_write` | `change`, `feature`, `content` (required), `files`, `node_id`, `cwd`, `project`, `branch`, `tags`, `agent` |
| `memory_search` | `query`, `cwd`, `project`, `branch`, `change`, `node_id`, `limit` |

Reads default to the current project and branch. The value `*` for `project` or `branch` reads all of them.

Query syntax is the same on every backend: all words must match, `"exact phrase"`, `prefix*`, and `OR` between alternatives.

## Publishing to npm

```sh
node npm/build.js 0.1.0
```

Each folder in `dist/npm` is one package. Run `npm publish` in the platform package folders first, then in `dist/npm/8memory`.

## Tests

```sh
go test ./...
```

The `MEMORY8_TEST_CLIPBOARD=1` test reads the real clipboard and is skipped by default.

Server backends are also tested when the following env vars hold a configuration JSON: `MEMORY8_TEST_POSTGRES`, `MEMORY8_TEST_SQLSERVER`, `MEMORY8_TEST_ORACLE`. Example:

```sh
MEMORY8_TEST_POSTGRES='{"backend":"postgres","host":"localhost","port":5432,"user":"postgres","password":"x","database":"postgres","options":"sslmode=disable"}' go test ./...
```

The tests add rows with unique project names and delete nothing, so use a database dedicated to testing.

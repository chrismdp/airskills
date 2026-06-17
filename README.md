<p align="center">
  <img src="https://raw.githubusercontent.com/chrismdp/airskills/main/assets/airskills-mark.png" alt="airskills" width="240">
</p>

Your AI skills are scattered across machines, tools, and teammates. airskills fixes that.

## The problem

Every AI tool reads skills from a different directory. You improve a skill in Claude Code and Cursor never sees it. Your laptop has version 3, your desktop has version 1. Your team lead shared the coding standards via Slack last month. Three people copied them.

airskills manages your AI skills from a single source of truth. Install once, sync everywhere. Edit once, every agent gets the update.

## Install

The fastest start needs nothing installed — `npx` fetches the CLI and runs it:

```bash
npx airskills add chrismdp/retro
```

Or install it permanently:

```bash
npm install -g airskills                              # npm
curl -fsSL https://airskills.ai/install.sh | bash     # macOS / Linux
irm https://airskills.ai/install.ps1 | iex            # Windows (PowerShell)
```

## Get started

```bash
airskills sync
```

This logs you in (opens your browser for Google or Microsoft sign-in), then syncs your skills across every detected agent on your machine. Free account, no credit card.

## Add a public skill

```bash
airskills add chrismdp/retro            # install a public skill
airskills add github.com/user/skill     # also accepts GitHub-style paths
```

This fetches the skill and writes it to every detected AI agent on your machine (`~/.cursor/skills/`, `~/.claude/skills/`, etc.).

## Use without installing (MCP)

Any tool that supports [MCP](https://modelcontextprotocol.io) can load skills from airskills without the CLI, without files on disk, and without setup. Add this as a remote MCP server in your tool:

```
https://airskills.ai/mcp
```

Works with Claude Code, Cursor, Copilot, Windsurf, Cline, ChatGPT, and any tool that supports remote MCP servers. Without auth, only public skills are visible; sign in to reach private and team skills. The endpoint is read-only and enforces the same row-level security as the API.

## For teams

Your best engineer wrote the code review skill and updates it weekly, but nobody else knows it exists. New joiners spend hours hunting for config files, and when someone pushes a bad update, 50 developers are affected with no rollback.

airskills lets you curate skills so your team does not have to. Publish once, everyone receives automatically. Version history with rollback. Conflict detection across machines and teammates. Visibility into who has what installed.

[Apply for a team trial](https://airskills.ai/team-trial)

## Supported agents

airskills detects and writes skills to all agents on your machine:

```
~/.cursor/skills/       → Cursor
~/.copilot/skills/      → GitHub Copilot
~/.claude/skills/       → Claude Code, Cowork
~/.windsurf/skills/     → Windsurf
~/.codex/skills/        → Codex
  ... and 13 more
```

Full list: Cursor, GitHub Copilot, Claude Code, Cowork, Windsurf, Codex, Cline, Roo Code, Continue, Gemini CLI, Augment, Kiro CLI, Junie, Goose, Trae, Amp, OpenCode, Aider, Amazon Q.

## Commands

| Command | Description |
|---------|-------------|
| `airskills sync` | Log in if needed, push local changes, pull remote skills |
| `airskills add <user/skill>` | Install a public or shared skill |
| `airskills add <user/skill> --preview` | Show skill content without installing |
| `airskills push` | Upload local skill changes |
| `airskills pull` | Download remote skills not on this machine |
| `airskills list` | Show skills with install status |
| `airskills status` | Check for updates |
| `airskills share <user/skill> --with <email>` | Share a skill |
| `airskills export <skill>` | Export a skill to a portable archive |
| `airskills config set <key> <value>` | Set config (e.g. `api_url` to point at your own server) |
| `airskills self-update` | Update the CLI |
| `airskills whoami` | Show current user |
| `airskills feedback -m "msg"` | Send feedback |
| `airskills version` | Print version info |

## How syncing works

**Push** uploads skills from `~/.claude/skills/` to your airskills.ai account with version tracking. Each push creates a new commit in a DAG, so you can roll back to any previous version.

**Pull** downloads remote skills to this machine. Pull never deletes local skills.

**Conflicts** are detected when the same skill was edited on another machine (content hash mismatch). When this happens:

1. The CLI downloads the remote version to `/tmp/airskills-conflicts/<skill-name>/`
2. It shows you both file paths — your local version and the remote version
3. You merge using your AI agent (e.g. "compare these two files and merge them")
4. Once resolved, run `airskills push --force` to push your merged version

No silent overwrites, no picking a winner automatically. You always see exactly what changed.

## What data does the CLI send?

Only your skill files (SKILL.md content) when you push, and auth tokens. Never your code, git history, or file system. Telemetry is lightweight and can be disabled with `AIRSKILLS_NO_TELEMETRY=1`. The source is here for you to verify.

## API and self-hosting

Everything the CLI does is available via the REST API at `airskills.ai/api/v1`. Authenticate with a Bearer token. The full contract is published as OpenAPI at [airskills.ai/openapi.json](https://airskills.ai/openapi.json) (also as [airskills.ai/llms.txt](https://airskills.ai/llms.txt)).

Use the API to pull skills programmatically, push updates from CI, or wire your agents directly.

**Run your own server.** The CLI talks to any backend that implements the OpenAPI contract — nothing is hardcoded to `airskills.ai`. Point it at your own host:

```bash
airskills config set api_url https://skills.your-company.com
```

The spec is the single source of truth: the hosted server generates it from the same schemas it validates requests against, and the CLI's typed API shapes are generated from that spec. Implement the contract and the CLI works against your server unchanged.

## Pricing

Free for individuals and small workspaces (up to 2 users), with full cross-machine sync. Team plans add distribution across larger teams. See [airskills.ai/pricing](https://airskills.ai/pricing).

## License

MIT

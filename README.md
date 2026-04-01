# airskills

Your AI skills are scattered across machines, tools, and teammates. airskills fixes that.

## The problem

Every AI coding tool reads skills from a different directory. You improve a skill in Claude Code and Cursor never sees it. Your laptop has version 3, your desktop has version 1. Your team lead shared the coding standards via Slack last month. Three people copied them.

airskills manages your AI skills from a single source of truth. Install once, sync everywhere. Edit once, every agent gets the update.

## Install

```bash
curl -fsSL https://airskills.ai/install.sh | bash
```

Or with Go:

```bash
go install github.com/chrismdp/airskills@latest
```

No account needed. The CLI works fully offline with no telemetry. Accounts are only for cross-machine sync and sharing.

## Quick start (no account needed)

```bash
airskills add chrismdp/retro            # install a public skill
airskills add github.com/user/skill     # also accepts GitHub-style paths
```

This fetches the skill and writes it to every detected AI agent on your machine (`~/.claude/skills/`, `~/.cursor/skills/`, etc.).

## Sync across machines (free account)

```bash
airskills login          # authenticate with airskills.ai
airskills sync           # push local skills, pull remote ones
```

Skills you installed via `add` before signing up are automatically linked to the originals. Unchanged skills reference the source, modified ones become your own copy with provenance tracked.

## For teams

Your best engineer wrote the code review skill and updates it weekly, but nobody else knows it exists. New joiners spend hours hunting for config files, and when someone pushes a bad update, 50 developers are affected with no rollback.

airskills lets you curate skills so your team does not have to. Publish once, everyone receives automatically. Version history with rollback. Conflict detection across machines and teammates. Visibility into who has what installed.

[Talk to us about teams](mailto:chris@airskills.ai?subject=Airskills%20for%20teams)

## Supported agents

airskills detects and writes skills to all agents on your machine:

```
~/.claude/skills/       → Claude Code, Claude Desktop (Cowork)
~/.cursor/skills/       → Cursor
~/.copilot/skills/      → GitHub Copilot
~/.windsurf/skills/     → Windsurf
~/.codex/skills/        → Codex
  ... and 13 more
```

Full list: Claude Code, Claude Desktop, Cursor, GitHub Copilot, Windsurf, Codex, Cline, Roo Code, Continue, Gemini CLI, Augment, Kiro CLI, Junie, Goose, Trae, Amp, OpenCode, Aider, Amazon Q.

## Commands

| Command | Description |
|---------|-------------|
| `airskills add <user/skill>` | Install a public or shared skill (no login needed) |
| `airskills install` | Sync skills (alias for `sync`) |
| `airskills sync` | Push local changes, pull remote skills |
| `airskills push` | Upload local skill changes |
| `airskills pull` | Download remote skills not on this machine |
| `airskills list` | Show skills with install status |
| `airskills status` | Check for updates |
| `airskills share <user/skill> --with <email>` | Share a skill |
| `airskills export <skill>` | Export a skill to a portable archive |
| `airskills configure <key> <value>` | Set config (e.g. `api_url`) |
| `airskills self-update` | Update the CLI |
| `airskills whoami` | Show current user |
| `airskills feedback -m "msg"` | Send feedback |
| `airskills version` | Print version info |

## How syncing works

**Push** uploads skills from `~/.claude/skills/` to your airskills.ai account with version tracking. Each push creates a new commit in a DAG, so you can roll back to any previous version.

**Pull** downloads remote skills to this machine. Pull never deletes local skills.

**Conflicts** are detected when the same skill was edited on another machine. airskills shows both versions and lets you resolve the merge with your AI agent, then `airskills push --force`.

## What data does the CLI send?

Only your skill files (SKILL.md content) when you push, and auth tokens. Never your code, git history, or file system. The source is here for you to verify.

## Free tier

Install public skills without an account. Free accounts get 100 skills with cross-machine sync. Teams and orgs on [airskills.ai](https://airskills.ai).

## License

MIT

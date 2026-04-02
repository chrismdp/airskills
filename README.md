```
            ███                    █████       ███  ████  ████
           ░░░                    ░░███       ░░░  ░░███ ░░███
  ██████   ████  ████████   █████  ░███ █████ ████  ░███  ░███   █████
 ░░░░░███ ░░███ ░░███░░███ ███░░   ░███░░███ ░░███  ░███  ░███  ███░░
  ███████  ░███  ░███ ░░░ ░░█████  ░██████░   ░███  ░███  ░███ ░░█████
 ███░░███  ░███  ░███      ░░░░███ ░███░░███  ░███  ░███  ░███  ░░░░███
░░████████ █████ █████     ██████  ████ █████ █████ █████ █████ ██████
 ░░░░░░░░ ░░░░░ ░░░░░     ░░░░░░  ░░░░ ░░░░░ ░░░░░ ░░░░░ ░░░░░ ░░░░░░
```

Your AI skills are scattered across machines, tools, and teammates. airskills fixes that.

## The problem

Every AI tool reads skills from a different directory. You improve a skill in Claude Code and Cursor never sees it. Your laptop has version 3, your desktop has version 1. Your team lead shared the coding standards via Slack last month. Three people copied them.

airskills manages your AI skills from a single source of truth. Install once, sync everywhere. Edit once, every agent gets the update.

## Install

```bash
curl -fsSL https://airskills.ai/install.sh | bash
```

Windows:

```powershell
irm https://airskills.ai/install.ps1 | iex
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

## For teams

Your best engineer wrote the code review skill and updates it weekly, but nobody else knows it exists. New joiners spend hours hunting for config files, and when someone pushes a bad update, 50 developers are affected with no rollback.

airskills lets you curate skills so your team does not have to. Publish once, everyone receives automatically. Version history with rollback. Conflict detection across machines and teammates. Visibility into who has what installed.

[Book a call about teams](https://calendar.app.google/Qx52oPwCe1vRSi5t6)

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

**Conflicts** are detected when the same skill was edited on another machine. airskills downloads both versions side by side and lets you merge with your AI agent, then `airskills push --force`.

## What data does the CLI send?

Only your skill files (SKILL.md content) when you push, and auth tokens. Never your code, git history, or file system. No telemetry. The source is here for you to verify.

## API

Everything the CLI does is available via the REST API at `airskills.ai/api/v1`. Authenticate with a Bearer token. Full spec at [airskills.ai/llms.txt](https://airskills.ai/llms.txt).

Use the API to pull skills programmatically, push updates from CI, or wire your agents directly.

## Free tier

Free accounts get 100 skills with cross-machine sync. Teams and orgs on [airskills.ai](https://airskills.ai).

## License

MIT

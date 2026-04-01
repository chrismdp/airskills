# airskills

Save, share, and install AI coding skills across every AI agent.

Airskills manages **skills** — reusable SKILL.md files that tell AI agents how to behave. Install once, sync everywhere. No account needed to get started.

## Install

```bash
curl -fsSL https://airskills.ai/install.sh | bash
```

Or with Go:

```bash
go install github.com/chrismdp/airskills@latest
```

Or with Homebrew:

```bash
brew install chrismdp/tap/airskills
```

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

Skills you installed via `add` before signing up are automatically linked to the originals — unchanged skills reference the source, modified ones become your own copy with provenance tracked.

## What it does

Your AI coding skills live in directories like `~/.claude/skills/my-skill/SKILL.md`. Different agents read from different locations. Airskills syncs them all from a single source of truth.

```
~/.claude/skills/       → Claude Code, Claude Desktop (Cowork)
~/.cursor/skills/       → Cursor
~/.copilot/skills/      → GitHub Copilot
~/.windsurf/skills/     → Windsurf
~/.codex/skills/        → Codex
  ... and 13 more
```

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

## Supported agents

Claude Code, Claude Desktop (Cowork), Cursor, GitHub Copilot, Windsurf, Codex, Cline, Roo Code, Continue, Gemini CLI, Augment, Kiro CLI, Junie, Goose, Trae, Amp, OpenCode, Aider, Amazon Q.

## How syncing works

- **Push** uploads skills from `~/.claude/skills/` to your airskills.ai account with version tracking
- **Pull** downloads remote skills to this machine — never deletes local skills
- **Conflicts** are detected when the same skill was edited on another machine; resolve with your AI agent, then `airskills push --force`

## What data does the CLI send?

Only your skill files (SKILL.md content) when you push, and auth tokens. Never your code, git history, or file system. The source is here for you to verify.

## Free tier

Install public skills without an account. Free accounts get 100 skills with cross-machine sync. Teams and orgs on [airskills.ai](https://airskills.ai).

## License

MIT

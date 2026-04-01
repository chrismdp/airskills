# airskills

Save, share, and install AI coding skills across every AI coding agent.

Airskills manages **skills** — reusable instruction files that tell AI coding agents how to behave. Install once, sync everywhere.

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

## Quick start

```bash
airskills login          # authenticate with airskills.ai
airskills install        # sync skills to all detected agents
airskills add user/skill # install a shared skill
```

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

One `airskills sync` pushes your local skills to your account and pulls remote skills to every agent on your machine.

## Commands

| Command | Description |
|---------|-------------|
| `airskills install` | Sync skills (alias for `sync`) |
| `airskills sync` | Push local changes, pull remote skills |
| `airskills push` | Upload local skill changes |
| `airskills pull` | Download remote skills not on this machine |
| `airskills list` | Show skills with install status |
| `airskills status` | Check for updates |
| `airskills add <user/skill>` | Install a shared or public skill |
| `airskills share <user/skill> --with <email>` | Share a skill |
| `airskills self-update` | Update the CLI |

## Supported agents

Claude Code, Claude Desktop (Cowork), Cursor, GitHub Copilot, Windsurf, Codex, Cline, Roo Code, Continue, Gemini CLI, Augment, Kiro CLI, Junie, Goose, Trae, Amp, OpenCode, Aider, Amazon Q.

## How syncing works

- **Push** uploads skills from `~/.claude/skills/` to your airskills.ai account with version tracking
- **Pull** downloads remote skills to this machine — never deletes local skills
- **Conflicts** are detected when the same skill was edited on another machine; resolve with your AI agent, then `airskills push --force`

## Free tier

100 skills, personal use. Teams and orgs are available on [airskills.ai](https://airskills.ai).

## License

MIT

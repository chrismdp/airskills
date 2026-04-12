---
name: airskills-guide
description: "How to use the airskills CLI for skill management — pushing, pulling, syncing, sharing, reviewing suggestions, managing forks, resolving conflicts, and team distribution."
---

# Using airskills

airskills backs up, publishes, and syncs AI skill files across tools, machines, and teammates. This skill teaches you how to help the user manage their skills with the CLI.

## Core commands

### Sync (the default action)

```bash
airskills sync
```

Pushes local changes, then pulls remote updates. Writes skills to every detected AI agent directory (Claude Code, Cursor, Copilot, Windsurf, and 15 more). This is the command most users run most often.

### Add a skill from someone else

```bash
airskills add username/skill-name
airskills add username/skill-name --preview  # inspect before installing
```

Installs a public skill to all detected agents. Creates a fork in the user's account so they can customise it and still receive upstream updates.

### Show a skill without installing

```bash
airskills show username/skill-name
```

Downloads to a temp folder and prints the SKILL.md path. Use this to try a skill in a single session without committing to a permanent install. Additional files in the skill are listed as follow-on `@` references.

### List your skills

```bash
airskills list
airskills list --scope org  # org skills only
```

Shows all skills in the user's skillset (personal + added from others) with descriptions, versions, and install status.

### Check status

```bash
airskills status
```

Shows what needs pushing, pulling, or updating. Designed for shell startup: `eval "$(airskills status)"`.

## Publishing and sharing

### Publish a skill (make it public)

```bash
airskills publish skill-name
```

Makes a private skill publicly visible. Anyone can then install it with `airskills add username/skill-name`. This is irreversible in the sense that the skill becomes discoverable, but the user can unpublish later.

### Share privately

```bash
airskills share username/skill-name --with colleague@example.com
```

Gives a specific person access to a private skill. They can install it with `airskills add`.

## Suggestions and reviews

When someone forks a skill and improves it, they can submit a suggestion back to the original author. This is like a pull request for skills.

### Reviewing suggestions (as the skill owner)

```bash
airskills review              # list all pending suggestions
airskills review skill-name   # suggestions for one skill
```

This prints the full agent-friendly workflow. For each suggestion:

```bash
# Download the suggested changes to a temp directory for comparison
airskills review download SUGGESTION_ID

# Accept — merges the suggestion into your skill
airskills review accept SUGGESTION_ID

# Decline — with an optional message back to the suggester
airskills review decline SUGGESTION_ID --message "Thanks but this doesn't fit our direction"
```

### Helping the user review a suggestion

When the user asks you to review a suggestion:

1. Run `airskills review` to see pending suggestions
2. Run `airskills review download SUGGESTION_ID` to get the files
3. Read both the current skill and the suggested version
4. Explain the differences to the user
5. Let the user decide whether to accept or decline
6. Run `airskills review accept` or `airskills review decline` based on their decision

## Managing forks and upstream updates

When the user installs a skill from someone else via `airskills add`, it creates a fork. The fork tracks the original so it can receive upstream updates.

### Pulling upstream changes

```bash
airskills status  # shows "upstream updates available" if the parent skill changed
airskills pull    # pulls upstream changes alongside the user's own updates
```

If both the user and the upstream author changed the same skill, airskills saves both versions for merge. Help the user by:

1. Reading both versions
2. Identifying the differences
3. Proposing a merged version that keeps the user's customisations and incorporates the upstream improvements
4. Writing the merged file
5. Running `airskills push --force` to confirm the resolution

## Conflict resolution

Conflicts happen when the same skill is edited on two different machines, or when both the user and an upstream author change the same skill.

When `airskills pull` detects a conflict:

1. It saves the remote version alongside the local one
2. It prints both file paths

Help the user resolve by:

1. Reading both versions (local and remote)
2. Identifying what changed in each
3. Merging the changes into a single coherent version
4. Writing the merged file to the skill directory
5. Running `airskills push --force` to push the resolved version

## Bundles

Bundles group multiple skills together for distribution.

```bash
airskills bundle list                           # list bundles
airskills bundle show bundle-name               # show skills in a bundle
airskills bundle create bundle-name             # create a new bundle
airskills bundle create bundle-name --description "Team onboarding skills"
```

## Export

Export skills for tools that don't support the CLI:

```bash
airskills export skill-name              # creates skill-name.zip (for ChatGPT, Cowork, Claude.ai)
airskills export skill-name -f dir       # creates a directory (for Claude Code plugin structure)
airskills export --all                   # exports all skills as zips
```

## Other useful commands

```bash
airskills mv old-name new-name    # rename a skill across all agents and the server
airskills rm skill-name           # remove locally and from the server
airskills log                     # recent changes across all skills
airskills self-update             # update the CLI to the latest version
airskills whoami                  # show current user
airskills login                   # authenticate (sync does this automatically)
airskills logout                  # clear stored credentials
```

## MCP (no CLI needed)

Tools that support MCP can load skills without the CLI:

```
MCP server URL: https://airskills.ai/mcp
```

This gives the tool read-only access to public skills. Log in with `airskills login` first to access private and team skills through MCP.

## Tips for helping users

- If the user says "sync my skills" or "update my skills", run `airskills sync`
- If they want to try a skill before installing, use `airskills show` not `airskills add`
- If they ask "what skills do I have", run `airskills list`
- If they want to share a skill with a colleague, use `airskills share`
- If a push fails with a conflict, help them merge by reading both versions and writing the combined result, then `airskills push --force`
- If they ask about upstream updates, run `airskills status` to check
- The `--preview` flag on `add` lets them inspect before installing — suggest it for unfamiliar skills

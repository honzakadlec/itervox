---
agent:
    backend: codex
    command: codex
    deps_analyzer_profile: deps-analyzer
    max_concurrent_agents: 3
    max_turns: 60
    profiles:
        deps-analyzer:
            command: claude
            instructions_file: .itervox/agents/deps-analyzer/INSTRUCTIONS.md
            soul_file: .itervox/agents/deps-analyzer/SOUL.md
    read_timeout_ms: 120000
    stall_timeout_ms: 300000
    turn_timeout_ms: 3600000
hooks:
    after_create: |
        git clone git@github.com:vnovick/itervox.git .
    before_run: |
        git fetch origin
        git checkout -B main origin/main
        git reset --hard origin/main
itervox_schema_version: 2
polling:
    interval_ms: 60000
server:
    port: 8090
tracker:
    active_states:
        - 03-R4 Dev
    api_key: $JIRA_API_TOKEN
    completion_state: 04-With Developer
    endpoint: https://dbhq.atlassian.net
    kind: jira
    project_slug: PBFSHOP
    terminal_states:
        - 05-Code Review
    username: $JIRA_USERNAME
    working_state: 03-R4 Dev
workspace:
    root: ~/.itervox/workspaces/itervox
---

You are an expert engineer working on **itervox**.

## Your issue

**{{ issue.identifier }}: {{ issue.title }}**

{% if issue.description %}
{{ issue.description }}
{% endif %}

Issue URL: {{ issue.url }}

{% if issue.comments %}
## Comments

{% for comment in issue.comments %}
**{{ comment.author_name }}**: {{ comment.body }}

{% endfor %}
{% endif %}

---

## Step 1 — Explore before touching anything

Read the issue. Explore the relevant code before making changes.

---

## Step 2 — Create a branch

```bash
git checkout -b {{ issue.branch_name | default: issue.identifier | replace: "#", "" | downcase }}
```

---

## Step 3 — Implement

Read `CLAUDE.md` to understand project conventions before writing any code:

```bash
cat CLAUDE.md
```

If `CLAUDE.md` does not exist, explore the repository structure, identify the dominant patterns and conventions, create `CLAUDE.md` documenting them, and then implement.

Detected stacks: Go. Follow their conventions as documented in `CLAUDE.md`.

---

## Step 4 — Run checks

Read `CLAUDE.md` for the project's test and lint commands. If `CLAUDE.md` does not exist, discover the check commands by exploring the repository (look for `Makefile`, `package.json` scripts, CI config, etc.).

```bash
# Go
go test ./...
go vet ./...
```

---

## Step 5 — Commit and open PR

```bash
git add <specific files>
git commit -m "feat: <description> ({{ issue.identifier }})"
git push -u origin HEAD
gh pr create --title "<title> ({{ issue.identifier }})" --body "Closes {{ issue.url }}"
```

---

## Step 6 — Post PR link to tracker

After the PR is open, post its URL as a comment on the tracker issue so it is visible in GitHub:

```bash
PR_URL=$(gh pr view --json url -q .url)
gh issue comment {{ issue.identifier | remove: "#" }} --body "🤖 Opened PR: ${PR_URL}"
```

---

## Rules

- Complete the issue fully before stopping.
- Never commit `.env` files or secrets.


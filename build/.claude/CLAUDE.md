# Frank ECS Container Environment

This container runs Claude Code in an ECS Fargate environment with persistent storage and pre-configured tools.

## Environment Overview

- **Workspace**: `/workspace` - Your working directory, backed by EFS for persistence
- **Git Worktrees**: Automatically created for container isolation when `GIT_REPO` is set
- **Credentials**: Claude OAuth and GitHub tokens are injected from AWS Secrets Manager

## Available Tools

| Tool | Description |
|------|-------------|
| `claude` | Claude Code CLI |
| `git` | Version control |
| `gh` | GitHub CLI (pre-authenticated) |
| `aws` | AWS CLI v2 |
| `node` / `npm` | Node.js 20 LTS |
| `uv` / `uvx` | Fast Python package manager |
| `python3` | Python 3 runtime |

## MCP Servers

The following MCP servers are available for extended functionality:

| Server | Description |
|--------|-------------|
| `sequential-thinking` | Step-by-step reasoning and planning |
| `aws-documentation` | AWS documentation search |
| `aws-core` | Core AWS service operations (S3, EC2, etc.) |
| `next-devtools` | Next.js development tools and debugging |
| `playwright` | Browser automation and testing |

## Working with Git

The container automatically creates git worktrees for isolation:
- Base repo is cloned to `/workspace/.repo` (or uses existing `/workspace` if a repo exists)
- Each container gets its own worktree at `/workspace/worktrees/<container-name>`
- Changes in worktrees can be committed and pushed independently
- Worktrees persist across container restarts (same container name)

## AWS Access

AWS credentials are automatically provided via the ECS task IAM role. No manual configuration needed.

```bash
# Example: List S3 buckets
aws s3 ls

# Example: Describe running ECS tasks
aws ecs list-tasks --cluster frank
```

## GitHub Access

GitHub is pre-authenticated via the injected token:

```bash
# Clone repositories
gh repo clone owner/repo

# Create PRs
gh pr create --title "My PR" --body "Description"
```

## AI Coding Agents

| Agent | Package | Description |
|-------|---------|-------------|
| `claude` | Claude Code CLI | Primary coding agent |
| `codex` | `@openai/codex` | OpenAI Codex CLI |

**Codex** uses device authentication. On first use, run:
```bash
codex login --device-auth
```

> **Note**: The Landlock sandbox is automatically disabled in ECS containers to allow shell command execution.

## Tips

1. **Persistent storage**: Files in `/workspace` persist via EFS
2. **Container restarts**: Same container name = same worktree
3. **Multiple containers**: Each gets an isolated worktree
4. **.claude directory**: Symlinked from base repo for shared hooks/settings

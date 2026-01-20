# Frank CLI

A CLI tool for launching isolated Claude Code development environments inside Docker containers.

## Features

- **Isolated Containers**: Each Claude session runs in its own Docker container
- **AWS SSO Integration**: Automatic credential injection with SSO login support
- **Web Terminal**: Browser-based terminal access via ttyd
- **Desktop Notifications**: Get notified when Claude is waiting for input
- **Git Worktrees**: Parallel development with automatic worktree management
- **Multi-Runtime Support**: Works with Docker, Podman, and OrbStack

## Installation

### Prerequisites

- Go 1.22 or later
- Docker, Podman, or OrbStack
- AWS CLI v2 (for AWS SSO features)

### Build from Source

```bash
# Clone the repository
git clone https://github.com/barff/frank.git
cd frank

# Build the CLI
make build

# Or install directly
make install
```

### Build the Container Image

```bash
frank rebuild
```

## Quick Start

1. **Set up Claude authentication**:
   ```bash
   export CLAUDE_ACCESS_TOKEN="your-token-here"
   ```

2. **Start a development container**:
   ```bash
   frank start --profile dev --repo https://github.com/your/project
   ```

3. **Open the terminal** at http://localhost:8080

4. **Stop the container**:
   ```bash
   frank stop frank-dev-1
   ```

## Commands

### `frank start`

Launch a new development container.

```bash
# Basic usage
frank start --profile dev

# With a git repository
frank start --profile dev --repo https://github.com/user/project --branch main

# Mount all AWS profiles
frank start --profile all

# Custom port
frank start --profile dev --port 9000

# Disable notifications
frank start --profile dev --no-notifications
```

**Flags:**
- `-p, --profile`: AWS profile name or "all" for full ~/.aws mount
- `-r, --repo`: Git repository URL to clone
- `-b, --branch`: Branch to checkout (default: main)
- `-n, --name`: Custom container name suffix
- `--port`: Override starting port
- `--no-notifications`: Disable notifications
- `-d, --detach`: Run in background

### `frank list`

Show running containers.

```bash
frank list              # Running containers
frank list -a           # Include stopped containers
frank list -q           # Only container IDs
frank list --format json  # JSON output
```

### `frank logs`

View container logs.

```bash
frank logs frank-dev-1        # Last 100 lines
frank logs frank-dev-1 -f     # Follow logs
frank logs frank-dev-1 --tail 50  # Last 50 lines
```

### `frank exec`

Execute a command in a container.

```bash
frank exec frank-dev-1 bash
frank exec frank-dev-1 git status
frank exec -it frank-dev-1 /bin/bash
```

### `frank stop`

Stop containers.

```bash
frank stop frank-dev-1         # Stop one container
frank stop --profile dev       # Stop all dev profile containers
frank stop --all               # Stop all frank containers
frank stop --force             # Force stop
frank stop --no-snapshot       # Skip state persistence
```

### `frank rebuild`

Rebuild the container image.

```bash
frank rebuild                  # Build with cache
frank rebuild --no-cache       # Build without cache
frank rebuild --tag my-image   # Custom image tag
```

## Configuration

Configuration file location:
- Linux/macOS: `~/.config/frank/config.yaml`
- Windows: `%APPDATA%\frank\config.yaml`

Example configuration:

```yaml
version: "1.0"

runtime:
  preferred: auto  # auto, docker, podman, orbstack

container:
  image: frank-dev:latest
  basePort: 8080
  maxPort: 8180

aws:
  autoLogin: true

notifications:
  enabled: true
  cooldown: 30s
  sound: true

mcp:
  servers:
    - name: context7
      enabled: true
    - name: sequential-thinking
      enabled: true
    - name: aws-documentation
      enabled: true
```

See `configs/frank.yaml.example` for full configuration options.

## AWS Integration

### Single Profile

```bash
# Uses temporary credentials from SSO
frank start --profile dev
```

If credentials are expired, frank will automatically run `aws sso login`.

### All Profiles

```bash
# Mounts entire ~/.aws directory (read-only)
frank start --profile all
```

## Claude Authentication

Set the `CLAUDE_ACCESS_TOKEN` environment variable to skip browser authentication:

```bash
export CLAUDE_ACCESS_TOKEN="your-token-here"
```

## Notifications

Frank sends desktop notifications when Claude is waiting for input:

- Questions (lines ending with `?`)
- Keywords: continue, approve, proceed, waiting, input, response
- Prompts: `[Y/n]`, `(yes/no)`, "Do you want", etc.

Notifications have a 30-second cooldown to prevent spam.

## MCP Servers

The container includes [MCP Launchpad](https://github.com/kenneth-liao/mcp-launchpad) for dynamic tool discovery and execution.

### Pre-configured Servers

| Server | Description |
|--------|-------------|
| `context7` | Context management and retrieval |
| `sequential-thinking` | Step-by-step reasoning and planning |
| `aws-documentation` | AWS documentation search |
| `aws-knowledge` | AWS knowledge base (Amazon Q) |
| `aws-core` | Core AWS service operations |

### Using MCP Launchpad

Inside the container, use `mcpl` to discover and execute tools:

```bash
# Search for tools across all servers
mcpl search "s3 bucket"

# List all available tools
mcpl list

# Inspect a specific tool
mcpl inspect aws-core list_buckets --example

# Call a tool
mcpl call aws-core list_buckets '{}'

# Check server connections
mcpl verify
```

### Key Workflow

1. **Search first**: `mcpl search "your query"` - Never guess tool names
2. **Inspect**: `mcpl inspect <server> <tool> --example` - Get parameters
3. **Execute**: `mcpl call <server> <tool> '{"args": "here"}'`

## Container Contents

The base image includes:

- Claude Code CLI
- MCP Launchpad (`mcpl`)
- ttyd (web terminal)
- git, gh, curl, jq
- AWS CLI v2
- uv (Python package manager)
- Node.js 20 LTS

## Architecture

```
frank/
├── cmd/                  # CLI commands
├── internal/
│   ├── config/          # Configuration management
│   ├── container/       # Container runtime abstraction
│   ├── aws/             # AWS SSO credential management
│   ├── claude/          # Claude auth & MCP config
│   ├── notification/    # Desktop notifications
│   ├── terminal/        # Port allocation
│   └── git/             # Worktree management
├── build/
│   ├── Dockerfile       # Container image
│   └── entrypoint.sh    # Container entry script
└── configs/             # Configuration examples
```

## Container Naming

Containers are named: `frank-<profile>-<index>`

Examples:
- `frank-dev-1`
- `frank-prod-2`
- `frank-all-1`

## Stop Behavior

When stopping a container:

1. Git worktrees are cleaned up (unless `--no-cleanup`)
2. Container state is saved to a timestamped image (unless `--no-snapshot`)
3. Container is stopped

## License

MIT License

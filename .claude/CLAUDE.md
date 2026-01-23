# Frank - Claude Code on AWS ECS

## Profile System

Frank uses a profile-based system for managing ECS tasks. Each profile represents a project/repository and gets its own URL path.

### Managing Profiles

```bash
# Add a new profile
frank profile add myproject --repo https://github.com/user/repo.git --branch main --desc "My project"

# List all profiles
frank profile list

# Show profile details
frank profile show myproject

# Remove a profile
frank profile remove myproject
```

Profiles are stored locally at `~/.config/frank/profiles.yaml` (Windows: `%APPDATA%\frank\profiles.yaml`).

### Starting/Stopping Profile Tasks

```bash
# Start a profile (creates path-based routing automatically)
frank ecs start myproject

# The task will be accessible at:
# https://frank.digitaldevops.io/myproject/

# Stop a profile
frank ecs stop myproject

# List all running tasks
frank ecs list
```

### Syncing Profiles to AWS (for Launch Page)

The web launch page reads profiles from SSM Parameter Store. Sync local profiles:

```bash
# Export local profiles to SSM
frank profile sync

# Or manually via AWS CLI
aws ssm put-parameter --name /frank/profiles --type String --overwrite \
  --value "$(frank profile list --json)"
```

## CDK Deployment

### Using Podman Instead of Docker

CDK builds Docker images during deployment. To use Podman instead of Docker Desktop:

```powershell
# Set environment variable before running cdk deploy
$env:CDK_DOCKER = "podman"

# Then run deploy
cd cdk
npx cdk deploy --require-approval never
```

Or set it permanently in your shell profile.

### Quick Deploy Commands

```powershell
# Full deploy (builds image + deploys infrastructure)
$env:CDK_DOCKER = "podman"
cd cdk && npx cdk deploy --require-approval never

# Update secrets only (no rebuild needed)
aws secretsmanager put-secret-value --secret-id /frank/github-token --secret-string "$(gh auth token)"
aws secretsmanager put-secret-value --secret-id /frank/claude-credentials --secret-string (Get-Content ~/.claude/.credentials.json -Raw)
```

## ECS Management

```bash
# List running tasks (shows profile column)
frank ecs list

# Start a profile-based task
frank ecs start <profile>

# Stop a profile or task
frank ecs stop <profile-or-task-id>

# Run a standalone task (no profile)
frank ecs run

# Scale the main service
frank ecs scale 2

# View logs
frank ecs logs <task-id>

# Check service status
frank ecs status
```

## Architecture

- **ALB**: Routes traffic via path-based rules (`/profile/*`)
- **ECS Fargate**: Runs Frank containers with 4GB memory, 2 vCPU
- **EFS**: Persistent storage at `/workspace` shared across tasks
- **Secrets Manager**: Stores GitHub token and Claude credentials
- **Cognito**: Authentication for web UI and terminals
- **Lambda**: API for web-based launch page
- **SSM Parameter**: Stores profile configurations for Lambda

## URLs

- Launch Page: `https://frank.digitaldevops.io/`
- Profile Tasks: `https://frank.digitaldevops.io/<profile>/`
- API: `https://frank.digitaldevops.io/api/profiles`

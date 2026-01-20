# Frank AWS Copilot Deployment

Deploy Frank containers to AWS ECS using AWS Copilot.

## Prerequisites

1. **AWS CLI** configured with appropriate credentials
2. **AWS Copilot CLI** installed:
   ```bash
   # macOS
   brew install aws/tap/copilot-cli

   # Linux
   curl -Lo copilot https://github.com/aws/copilot-cli/releases/latest/download/copilot-linux
   chmod +x copilot
   sudo mv copilot /usr/local/bin/

   # Windows
   Invoke-WebRequest -OutFile copilot.exe https://github.com/aws/copilot-cli/releases/latest/download/copilot-windows.exe
   ```

3. **Docker** running (for building images)

## Quick Start

### 1. Initialize (first time only)

```bash
cd /path/to/frank
copilot app init frank --domain frank.digitaldevops.io
copilot env init --name dev --default-config
copilot svc init --name frank
```

Or use the deploy script:
```bash
./copilot/deploy.sh init
```

### 2. Deploy Environment

```bash
copilot env deploy --name dev
```

Or:
```bash
./copilot/deploy.sh env dev
```

### 3. Configure Secrets

Use `copilot secret init` to create secrets (reads from local credentials automatically):

```bash
# Using the deploy script (recommended - auto-reads local credentials)
./copilot/deploy.sh secrets dev

# Or manually with copilot secret init
copilot secret init --name GITHUB_TOKEN --values "dev=$(gh auth token)"
copilot secret init --name CLAUDE_CREDENTIALS --values "dev=$(cat ~/.claude/.credentials.json)"
```

Prerequisites:
- GitHub: Be logged in via `gh auth login`
- Claude: Have `~/.claude/.credentials.json` from running `claude` locally

### 4. Deploy Service

```bash
copilot svc deploy --name frank --env dev
```

Or:
```bash
./copilot/deploy.sh deploy dev
```

### 5. Access

Get the service URL:
```bash
copilot svc show --name frank
```

Open the URL in your browser to access the Frank web UI.

## Useful Commands

```bash
# Check status
copilot svc status --name frank --env dev

# View logs
copilot svc logs --name frank --env dev --follow

# Open shell in container (debugging)
copilot svc exec --name frank --env dev

# Pause (scale to 0)
copilot svc pause --name frank --env dev

# Resume
copilot svc resume --name frank --env dev

# Delete service
copilot svc delete --name frank --env dev

# Delete everything
copilot app delete
```

## Configuration

### Scaling

Edit `copilot/frank/manifest.yml`:

```yaml
count:
  range: 1-5
  cpu_percentage: 70
```

### Resources

```yaml
cpu: 2048      # 2 vCPU
memory: 4096   # 4 GB
```

### Git Repository

Set environment variables in the manifest or at runtime:

```yaml
variables:
  GIT_REPO: "https://github.com/your/repo.git"
  GIT_BRANCH: "main"
```

## Architecture

```
                    ┌─────────────────┐
                    │  Application    │
                    │  Load Balancer  │
                    └────────┬────────┘
                             │
              ┌──────────────┼──────────────┐
              │              │              │
        ┌─────▼─────┐  ┌─────▼─────┐  ┌─────▼─────┐
        │  Frank    │  │  Frank    │  │  Frank    │
        │  Task 1   │  │  Task 2   │  │  Task N   │
        └─────┬─────┘  └─────┬─────┘  └─────┬─────┘
              │              │              │
              └──────────────┼──────────────┘
                             │
                    ┌────────▼────────┐
                    │    EFS Volume   │
                    │   (workspace)   │
                    └─────────────────┘
```

## CI/CD Pipeline

### Setup Pipeline

1. **Update the repository URL** in `copilot/pipeline.yml`:
   ```yaml
   source:
     provider: GitHub
     properties:
       repository: https://github.com/YOUR_USERNAME/YOUR_REPO
   ```

2. **Initialize the pipeline**:
   ```bash
   copilot pipeline init
   ```
   This will create a CodeStar connection to GitHub. You'll need to authorize it in the AWS Console.

3. **Deploy the pipeline**:
   ```bash
   copilot pipeline deploy
   ```

4. **Connect GitHub** (first time only):
   - Go to AWS Console → Developer Tools → Settings → Connections
   - Find the pending connection and click "Update pending connection"
   - Authorize GitHub access

### Pipeline Flow

```
GitHub Push → CodePipeline → CodeBuild → ECR → ECS (dev)
                                              ↓
                                     Manual Approval
                                              ↓
                                         ECS (prod)
```

### Pipeline Commands

```bash
# Check pipeline status
copilot pipeline status

# View pipeline in console
copilot pipeline show

# Update pipeline after manifest changes
copilot pipeline deploy

# Delete pipeline
copilot pipeline delete
```

## Troubleshooting

### Container won't start
```bash
# Check logs
copilot svc logs --name frank --env dev

# Check status
aws ecs describe-services --cluster frank-dev-Cluster --services frank
```

### Secrets not working
```bash
# Verify secrets exist
aws secretsmanager list-secrets --filter Key="name",Values="/copilot/frank"

# Check secret values
aws secretsmanager get-secret-value --secret-id "/copilot/frank/dev/secrets/github-token"
```

### EFS issues
```bash
# Check EFS mount targets
aws efs describe-mount-targets --file-system-id <fs-id>
```

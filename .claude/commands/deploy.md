# /deploy - Deploy Frank to AWS ECS

Deploy Frank to AWS ECS using AWS CDK. This command handles the full deployment workflow including infrastructure provisioning, secrets configuration, and service deployment.

## Arguments

- `$ARGUMENTS` - Optional: action (e.g., "deploy", "secrets", "status")

## Actions

| Action | Description |
|--------|-------------|
| `deploy` | Deploy/update infrastructure and service (default) |
| `bootstrap` | One-time CDK bootstrap for AWS account |
| `secrets` | Configure GitHub and Claude secrets |
| `status` | Check deployment status |
| `logs` | Stream service logs |
| `destroy` | Destroy all infrastructure |

## Instructions

Parse the arguments to determine action:
- Default action: `deploy`
- Example: `/deploy secrets` → action=secrets

### Configuration

```
Project Root: c:\Users\barff\Documents\autoclauto
CDK Directory: c:\Users\barff\Documents\autoclauto\cdk
Domain: frank.digitaldevops.io
Route 53 Hosted Zone: Z3OKT7D3Q3TASV
ACM Certificate: arn:aws:acm:us-east-1:882384879235:certificate/772d185a-9f1f-43d8-b20f-5c82c11f1b01
AWS Region: us-east-1
```

### First-Time Setup

1. **Check prerequisites**
   ```bash
   node --version
   aws sts get-caller-identity
   ```

2. **Install CDK dependencies**
   ```bash
   cd "c:\Users\barff\Documents\autoclauto\cdk"
   npm install
   ```

3. **Bootstrap CDK** (one-time per AWS account/region)
   ```bash
   npx cdk bootstrap
   ```

4. **Deploy infrastructure**
   ```bash
   npx cdk deploy --require-approval never
   ```

5. **Configure secrets**
   - Get GitHub token via `gh auth token`
   - Read Claude credentials from `~/.claude/.credentials.json`
   ```bash
   aws secretsmanager put-secret-value --secret-id /frank/github-token --secret-string "$(gh auth token)"
   aws secretsmanager put-secret-value --secret-id /frank/claude-credentials --secret-string "$(cat ~/.claude/.credentials.json)"
   ```

6. **Verify**
   ```bash
   aws ecs describe-services --cluster frank --services frank
   ```

### Using the Deploy Script

PowerShell:
```powershell
.\cdk\deploy.ps1 bootstrap   # First-time setup
.\cdk\deploy.ps1 deploy      # Deploy infrastructure
.\cdk\deploy.ps1 secrets     # Configure credentials
.\cdk\deploy.ps1 status      # Check status
.\cdk\deploy.ps1 logs        # Stream logs
```

Bash:
```bash
./cdk/deploy.sh bootstrap
./cdk/deploy.sh deploy
./cdk/deploy.sh secrets
```

### Individual Actions

**bootstrap**: One-time CDK setup - creates staging bucket and roles in AWS

**deploy**: Synthesize CloudFormation template and deploy stack including:
- VPC with public/private subnets
- ECS Fargate cluster
- EFS file system for persistent storage
- Application Load Balancer with HTTPS
- Route 53 DNS record
- Secrets Manager secrets

**secrets**: Auto-detect and upload:
- GitHub token (from `gh auth token`)
- Claude credentials (from `~/.claude/.credentials.json`)

**status**: Show ECS service health and task status

**logs**: Stream CloudWatch logs from `/ecs/frank`

**destroy**: Delete all infrastructure (with confirmation)

### Output

Report results including:
- Service URL (https://frank.digitaldevops.io)
- Deployment status
- Any errors or warnings
- Secret ARNs if secrets need updating

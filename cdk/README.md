# Frank CDK Infrastructure

AWS CDK infrastructure for deploying Frank to ECS Fargate.

## Prerequisites

- Node.js 18+
- AWS CLI configured with credentials
- Docker running (for building container image)

## Quick Start

```bash
# Install dependencies
npm install

# Bootstrap CDK (one-time per account/region)
npx cdk bootstrap

# Deploy
npx cdk deploy --require-approval never

# Set secrets
aws secretsmanager put-secret-value --secret-id /frank/github-token --secret-string "$(gh auth token)"
aws secretsmanager put-secret-value --secret-id /frank/claude-credentials --secret-string "$(cat ~/.claude/.credentials.json)"
```

Or use the deploy script:

```powershell
.\deploy.ps1 bootstrap
.\deploy.ps1 deploy
.\deploy.ps1 secrets
```

## What Gets Created

- **VPC** with public and private subnets (2 AZs)
- **ECS Fargate Cluster** named `frank`
- **EFS File System** for persistent `/workspace` storage
- **Application Load Balancer** with HTTPS (port 443)
- **Route 53 A Record** for `frank.digitaldevops.io`
- **Secrets Manager Secrets** for GitHub token and Claude credentials

## Commands

| Command | Description |
|---------|-------------|
| `npx cdk deploy` | Deploy the stack |
| `npx cdk diff` | Show pending changes |
| `npx cdk synth` | Output CloudFormation template |
| `npx cdk destroy` | Destroy all resources |

## Operations

```bash
# Check service status
aws ecs describe-services --cluster frank --services frank

# Stream logs
aws logs tail /ecs/frank --follow

# Shell into container
TASK=$(aws ecs list-tasks --cluster frank --service-name frank --query "taskArns[0]" --output text)
aws ecs execute-command --cluster frank --task $TASK --container frank --interactive --command /bin/bash
```

## Configuration

Edit `bin/frank.ts` to change:
- Domain name
- Route 53 hosted zone ID
- ACM certificate ARN

Edit `lib/frank-stack.ts` to change:
- VPC configuration
- ECS task size (CPU/memory)
- EFS lifecycle policy

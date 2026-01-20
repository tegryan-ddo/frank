# /deploy - Deploy Frank to AWS ECS

Deploy Frank to AWS ECS using AWS Copilot. This command handles the full deployment workflow including environment setup, secrets configuration, service deployment, and DNS setup.

## Arguments

- `$ARGUMENTS` - Optional: action and environment (e.g., "full dev", "service prod", "dns")

## Actions

| Action | Description |
|--------|-------------|
| `full` | Full deployment: init → env → secrets → deploy → dns (default) |
| `init` | Initialize Copilot application |
| `env` | Deploy environment infrastructure |
| `secrets` | Configure secrets in AWS Secrets Manager |
| `service` | Deploy the frank service |
| `dns` | Set up Route 53 DNS record |
| `status` | Check deployment status |
| `logs` | Stream service logs |
| `pipeline` | Set up CI/CD pipeline |

## Instructions

Parse the arguments to determine action and environment:
- Default action: `full`
- Default environment: `dev`
- Example: `/deploy service prod` → action=service, env=prod

### Configuration

```
Project Root: c:\Users\barff\Documents\autoclauto
Domain: frank.digitaldevops.io
Route 53 Hosted Zone: Z3OKT7D3Q3TASV
ACM Certificate: arn:aws:acm:us-east-1:882384879235:certificate/772d185a-9f1f-43d8-b20f-5c82c11f1b01
AWS Region: us-east-1
```

### Full Deployment Steps

1. **Check prerequisites**
   ```bash
   copilot --version
   aws sts get-caller-identity
   ```

2. **Initialize Copilot** (if needed)
   ```bash
   cd "c:\Users\barff\Documents\autoclauto"
   copilot app init frank
   copilot env init --name $ENV --default-config
   copilot svc init --name frank
   ```

3. **Deploy environment**
   ```bash
   copilot env deploy --name $ENV
   ```

4. **Configure secrets**
   - Get GitHub token via `gh auth token`
   - Read Claude credentials from `~/.claude/.credentials.json` or `$env:USERPROFILE\.claude\.credentials.json`
   - Store in Secrets Manager:
   ```bash
   aws secretsmanager put-secret-value --secret-id "/copilot/frank/$ENV/secrets/github-token" --secret-string "$GH_TOKEN"
   aws secretsmanager put-secret-value --secret-id "/copilot/frank/$ENV/secrets/claude-credentials" --secret-string "$CLAUDE_CREDS"
   ```

5. **Deploy service**
   ```bash
   copilot svc deploy --name frank --env $ENV
   ```

6. **Set up DNS**
   - Get ALB info: `copilot svc show --name frank --env $ENV --json`
   - Create Route 53 alias record for frank.digitaldevops.io pointing to ALB

7. **Verify**
   ```bash
   copilot svc status --name frank --env $ENV
   ```

### Individual Actions

**init**: Initialize Copilot app, environment, and service manifests

**env**: Deploy CloudFormation stacks for VPC, ALB, ECS cluster

**secrets**: Prompt for or auto-detect GitHub token and Claude credentials, store in Secrets Manager

**service**: Build Docker image, push to ECR, deploy ECS task

**dns**: Get ALB DNS name from Copilot, create/update Route 53 A record alias

**status**: Show service health, task status, and URL

**logs**: Stream CloudWatch logs with `--follow`

**pipeline**: Run `copilot pipeline init` and `copilot pipeline deploy`

### Output

Report results including:
- Service URL (https://frank.digitaldevops.io)
- Deployment status
- Any errors or warnings

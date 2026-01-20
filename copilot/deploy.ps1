# Frank AWS Copilot Deployment Script (PowerShell)
# Prerequisites: AWS CLI configured, Copilot CLI installed

param(
    [Parameter(Position=0)]
    [string]$Action = "help",

    [Parameter(Position=1)]
    [string]$Env = "dev"
)

$ErrorActionPreference = "Stop"
$ProjectDir = Split-Path -Parent $PSScriptRoot

Write-Host "=== Frank AWS Copilot Deployment ===" -ForegroundColor Cyan

# Check prerequisites
function Test-Command($cmd) {
    return [bool](Get-Command -Name $cmd -ErrorAction SilentlyContinue)
}

if (-not (Test-Command "copilot")) {
    Write-Host "Error: AWS Copilot CLI not found" -ForegroundColor Red
    Write-Host "Install: https://aws.github.io/copilot-cli/docs/getting-started/install/"
    exit 1
}

if (-not (Test-Command "aws")) {
    Write-Host "Error: AWS CLI not found" -ForegroundColor Red
    exit 1
}

switch ($Action) {
    "init" {
        Write-Host "Initializing Copilot application..."
        Push-Location $ProjectDir
        copilot app init frank

        Write-Host ""
        Write-Host "Creating dev environment..."
        copilot env init --name dev --default-config

        Write-Host ""
        Write-Host "Initializing frank service..."
        copilot svc init --name frank
        Pop-Location
    }

    "env" {
        Write-Host "Deploying environment: $Env"
        Push-Location $ProjectDir
        copilot env deploy --name $Env
        Pop-Location
    }

    "secrets" {
        Write-Host "Setting up secrets for environment: $Env"

        # GitHub token
        Write-Host ""
        $ghToken = Read-Host "Enter your GitHub token (run 'gh auth token' to get it)"

        Write-Host "Updating GitHub token in AWS Secrets Manager..."
        aws secretsmanager put-secret-value `
            --secret-id "/copilot/frank/$Env/secrets/github-token" `
            --secret-string $ghToken

        # Claude credentials
        Write-Host ""
        Write-Host "Enter path to Claude credentials file (default: ~/.claude/.credentials.json):"
        $credPath = Read-Host
        if ([string]::IsNullOrEmpty($credPath)) {
            $credPath = "$env:USERPROFILE\.claude\.credentials.json"
        }

        if (Test-Path $credPath) {
            $claudeCreds = Get-Content $credPath -Raw
            Write-Host "Updating Claude credentials in AWS Secrets Manager..."
            aws secretsmanager put-secret-value `
                --secret-id "/copilot/frank/$Env/secrets/claude-credentials" `
                --secret-string $claudeCreds
            Write-Host "Secrets updated successfully!" -ForegroundColor Green
        } else {
            Write-Host "Warning: Claude credentials file not found at $credPath" -ForegroundColor Yellow
            Write-Host "You'll need to update the secret manually after authenticating with Claude."
        }
    }

    "deploy" {
        Write-Host "Deploying frank service to environment: $Env"
        Push-Location $ProjectDir
        copilot svc deploy --name frank --env $Env
        Pop-Location
    }

    "status" {
        Write-Host "Checking status..."
        Push-Location $ProjectDir
        copilot svc status --name frank --env $Env
        Pop-Location
    }

    "logs" {
        Write-Host "Streaming logs..."
        Push-Location $ProjectDir
        copilot svc logs --name frank --env $Env --follow
        Pop-Location
    }

    "exec" {
        Write-Host "Opening shell in container..."
        Push-Location $ProjectDir
        copilot svc exec --name frank --env $Env
        Pop-Location
    }

    "url" {
        Write-Host "Getting service URL..."
        Push-Location $ProjectDir
        $info = copilot svc show --name frank --env $Env --json | ConvertFrom-Json
        Write-Host $info.routes[0].url
        Pop-Location
    }

    "delete" {
        Write-Host "Deleting frank service from environment: $Env"
        Push-Location $ProjectDir
        copilot svc delete --name frank --env $Env
        Pop-Location
    }

    "delete-all" {
        Write-Host "WARNING: This will delete the entire frank application and all environments!" -ForegroundColor Red
        $confirm = Read-Host "Are you sure? (yes/no)"
        if ($confirm -eq "yes") {
            Push-Location $ProjectDir
            copilot app delete --name frank
            Pop-Location
        } else {
            Write-Host "Cancelled."
        }
    }

    "cert" {
        Write-Host "Requesting ACM certificate for frank.digitaldevops.io..."
        $result = aws acm request-certificate `
            --domain-name "frank.digitaldevops.io" `
            --validation-method DNS `
            --region us-east-1 `
            --output json | ConvertFrom-Json

        $certArn = $result.CertificateArn
        Write-Host ""
        Write-Host "Certificate ARN: $certArn" -ForegroundColor Green
        Write-Host ""
        Write-Host "Next steps:"
        Write-Host "1. Get the DNS validation record:"
        Write-Host "   aws acm describe-certificate --certificate-arn `"$certArn`" --query 'Certificate.DomainValidationOptions[0].ResourceRecord'"
        Write-Host ""
        Write-Host "2. Add that CNAME record to your DNS (digitaldevops.io)"
        Write-Host ""
        Write-Host "3. Update copilot/environments/dev/manifest.yml with the certificate ARN:"
        Write-Host "   certificates:"
        Write-Host "     - $certArn"
        Write-Host ""
        Write-Host "4. After DNS validation completes, deploy: .\deploy.ps1 env dev"
    }

    "pipeline-init" {
        Write-Host "Initializing CI/CD pipeline..."
        Push-Location $ProjectDir
        copilot pipeline init
        Pop-Location
        Write-Host ""
        Write-Host "Pipeline initialized. Update copilot/pipeline.yml with your GitHub repo URL,"
        Write-Host "then run: .\deploy.ps1 pipeline-deploy"
    }

    "pipeline-deploy" {
        Write-Host "Deploying CI/CD pipeline..."
        Push-Location $ProjectDir
        copilot pipeline deploy
        Pop-Location
        Write-Host ""
        Write-Host "Pipeline deployed! You may need to authorize the GitHub connection in AWS Console:"
        Write-Host "AWS Console -> Developer Tools -> Settings -> Connections"
    }

    "pipeline-status" {
        Write-Host "Checking pipeline status..."
        Push-Location $ProjectDir
        copilot pipeline status
        Pop-Location
    }

    default {
        Write-Host @"
Usage: .\deploy.ps1 <action> [environment]

Actions:
  init            - Initialize Copilot app (first time only)
  env             - Deploy environment infrastructure
  secrets         - Configure Claude and GitHub secrets
  deploy          - Deploy/update the frank service
  status          - Check service status
  logs            - Stream service logs
  exec            - Open shell in container
  url             - Get service URL
  cert            - Request ACM certificate for custom domain
  delete          - Delete service from environment
  delete-all      - Delete entire application

Pipeline Actions:
  pipeline-init   - Initialize CI/CD pipeline
  pipeline-deploy - Deploy/update the pipeline
  pipeline-status - Check pipeline status

Environments: dev, prod

Example workflow (manual):
  .\deploy.ps1 init              # First time setup
  .\deploy.ps1 env dev           # Deploy dev environment
  .\deploy.ps1 secrets dev       # Configure secrets
  .\deploy.ps1 deploy dev        # Deploy service
  .\deploy.ps1 url dev           # Get URL

Example workflow (CI/CD):
  .\deploy.ps1 init              # First time setup
  .\deploy.ps1 env dev           # Deploy dev environment
  .\deploy.ps1 env prod          # Deploy prod environment
  .\deploy.ps1 pipeline-init     # Initialize pipeline
  .\deploy.ps1 pipeline-deploy   # Deploy pipeline
  # Now pushes to main will auto-deploy!
"@
    }
}

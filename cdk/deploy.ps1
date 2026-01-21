# Frank CDK Deployment Script (PowerShell)
# Prerequisites: AWS CLI configured, Node.js installed

param(
    [Parameter(Position=0)]
    [string]$Action = "help"
)

$ErrorActionPreference = "Stop"
$CdkDir = $PSScriptRoot

Write-Host "=== Frank CDK Deployment ===" -ForegroundColor Cyan

# Check prerequisites
function Test-Command($cmd) {
    return [bool](Get-Command -Name $cmd -ErrorAction SilentlyContinue)
}

if (-not (Test-Command "node")) {
    Write-Host "Error: Node.js not found" -ForegroundColor Red
    Write-Host "Install: https://nodejs.org/"
    exit 1
}

if (-not (Test-Command "aws")) {
    Write-Host "Error: AWS CLI not found" -ForegroundColor Red
    exit 1
}

# Ensure dependencies are installed
Push-Location $CdkDir
if (-not (Test-Path "node_modules")) {
    Write-Host "Installing dependencies..."
    npm install
}

switch ($Action) {
    "bootstrap" {
        Write-Host "Bootstrapping CDK in AWS account..."
        npx cdk bootstrap
    }

    "deploy" {
        Write-Host "Deploying Frank stack..."
        npx cdk deploy --require-approval never
        Write-Host ""
        Write-Host "Deployment complete!" -ForegroundColor Green
        Write-Host "Don't forget to set your secrets:"
        Write-Host '  aws secretsmanager put-secret-value --secret-id /frank/github-token --secret-string "$(gh auth token)"'
        Write-Host '  aws secretsmanager put-secret-value --secret-id /frank/claude-credentials --secret-string (Get-Content ~/.claude/.credentials.json -Raw)'
    }

    "secrets" {
        Write-Host "Setting up secrets..."

        # GitHub token
        $ghToken = $null
        try {
            $ghToken = (gh auth token 2>$null)
        } catch {}

        if ([string]::IsNullOrEmpty($ghToken)) {
            $ghToken = Read-Host "Enter your GitHub token (run 'gh auth token' to get it)"
        } else {
            Write-Host "Using token from 'gh auth token'" -ForegroundColor Green
        }

        if (-not [string]::IsNullOrEmpty($ghToken)) {
            Write-Host "Updating GitHub token..."
            aws secretsmanager put-secret-value --secret-id /frank/github-token --secret-string $ghToken
        }

        # Claude credentials
        $credPath = "$env:USERPROFILE\.claude\.credentials.json"
        if (Test-Path $credPath) {
            $claudeCreds = (Get-Content $credPath -Raw).Trim()
            Write-Host "Updating Claude credentials..."
            aws secretsmanager put-secret-value --secret-id /frank/claude-credentials --secret-string $claudeCreds
            Write-Host "Secrets updated!" -ForegroundColor Green
        } else {
            Write-Host "Warning: Claude credentials not found at $credPath" -ForegroundColor Yellow
            Write-Host "Run 'claude' to authenticate first."
        }
    }

    "diff" {
        Write-Host "Showing changes..."
        npx cdk diff
    }

    "synth" {
        Write-Host "Synthesizing CloudFormation template..."
        npx cdk synth
    }

    "destroy" {
        Write-Host "WARNING: This will destroy all Frank infrastructure!" -ForegroundColor Red
        $confirm = Read-Host "Are you sure? (yes/no)"
        if ($confirm -eq "yes") {
            npx cdk destroy --force
        } else {
            Write-Host "Cancelled."
        }
    }

    "logs" {
        Write-Host "Streaming logs..."
        aws logs tail /ecs/frank --follow
    }

    "exec" {
        Write-Host "Getting task ARN..."
        $taskArn = aws ecs list-tasks --cluster frank --service-name frank --query "taskArns[0]" --output text
        if ($taskArn -and $taskArn -ne "None") {
            Write-Host "Connecting to task: $taskArn"
            aws ecs execute-command --cluster frank --task $taskArn --container frank --interactive --command "/bin/bash"
        } else {
            Write-Host "No running tasks found" -ForegroundColor Yellow
        }
    }

    "status" {
        Write-Host "Service status:"
        aws ecs describe-services --cluster frank --services frank --query "services[0].{Status:status,Running:runningCount,Desired:desiredCount,Pending:pendingCount}" --output table
    }

    default {
        Write-Host @"
Usage: .\deploy.ps1 <action>

Actions:
  bootstrap     - One-time CDK bootstrap (run first)
  deploy        - Deploy/update the Frank stack
  secrets       - Configure GitHub and Claude secrets
  diff          - Show pending changes
  synth         - Output CloudFormation template
  destroy       - Destroy all infrastructure

Operations:
  status        - Check service status
  logs          - Stream container logs
  exec          - Shell into running container

First-time setup:
  1. .\deploy.ps1 bootstrap    # One-time CDK setup
  2. .\deploy.ps1 deploy       # Deploy infrastructure
  3. .\deploy.ps1 secrets      # Set credentials

URL: https://frank.digitaldevops.io
"@
    }
}

Pop-Location

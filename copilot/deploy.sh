#!/bin/bash
set -e

# Frank AWS Copilot Deployment Script
# Prerequisites: AWS CLI configured, Copilot CLI installed

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

echo "=== Frank AWS Copilot Deployment ==="

# Check prerequisites
if ! command -v copilot &> /dev/null; then
    echo "Error: AWS Copilot CLI not found"
    echo "Install: brew install aws/tap/copilot-cli"
    echo "   or:   curl -Lo copilot https://github.com/aws/copilot-cli/releases/latest/download/copilot-linux && chmod +x copilot && sudo mv copilot /usr/local/bin/"
    exit 1
fi

if ! command -v aws &> /dev/null; then
    echo "Error: AWS CLI not found"
    exit 1
fi

# Parse arguments
ACTION="${1:-deploy}"
ENV="${2:-dev}"

case "$ACTION" in
    init)
        echo "Initializing Copilot application..."
        cd "$PROJECT_DIR"
        copilot app init frank

        echo ""
        echo "Creating dev environment..."
        copilot env init --name dev --profile default --default-config

        echo ""
        echo "Initializing frank service..."
        copilot svc init --name frank --svc-type "Load Balanced Web Service" --dockerfile build/Dockerfile.ecs
        ;;

    env)
        echo "Deploying environment: $ENV"
        cd "$PROJECT_DIR"
        copilot env deploy --name "$ENV"
        ;;

    secrets)
        echo "Setting up secrets for environment: $ENV"

        # Prompt for Claude credentials
        echo ""
        echo "Enter your Claude OAuth credentials JSON (from ~/.claude/.credentials.json):"
        echo "Paste the JSON and press Ctrl+D when done:"
        CLAUDE_CREDS=$(cat)

        # Prompt for GitHub token
        echo ""
        echo "Enter your GitHub token (run 'gh auth token' to get it):"
        read -r GH_TOKEN

        # Update secrets in AWS
        echo "Updating secrets in AWS Secrets Manager..."
        aws secretsmanager put-secret-value \
            --secret-id "/copilot/frank/$ENV/secrets/claude-credentials" \
            --secret-string "$CLAUDE_CREDS"

        aws secretsmanager put-secret-value \
            --secret-id "/copilot/frank/$ENV/secrets/github-token" \
            --secret-string "$GH_TOKEN"

        echo "Secrets updated successfully!"
        ;;

    deploy)
        echo "Deploying frank service to environment: $ENV"
        cd "$PROJECT_DIR"
        copilot svc deploy --name frank --env "$ENV"
        ;;

    status)
        echo "Checking status..."
        cd "$PROJECT_DIR"
        copilot svc status --name frank --env "$ENV"
        ;;

    logs)
        echo "Streaming logs..."
        cd "$PROJECT_DIR"
        copilot svc logs --name frank --env "$ENV" --follow
        ;;

    exec)
        echo "Opening shell in container..."
        cd "$PROJECT_DIR"
        copilot svc exec --name frank --env "$ENV"
        ;;

    url)
        echo "Getting service URL..."
        cd "$PROJECT_DIR"
        copilot svc show --name frank --env "$ENV" --json | jq -r '.routes[0].url'
        ;;

    delete)
        echo "Deleting frank service from environment: $ENV"
        cd "$PROJECT_DIR"
        copilot svc delete --name frank --env "$ENV"
        ;;

    delete-all)
        echo "WARNING: This will delete the entire frank application and all environments!"
        read -p "Are you sure? (yes/no): " CONFIRM
        if [ "$CONFIRM" = "yes" ]; then
            cd "$PROJECT_DIR"
            copilot app delete --name frank
        else
            echo "Cancelled."
        fi
        ;;

    pipeline-init)
        echo "Initializing CI/CD pipeline..."
        cd "$PROJECT_DIR"
        copilot pipeline init
        echo ""
        echo "Pipeline initialized. Update copilot/pipeline.yml with your GitHub repo URL,"
        echo "then run: $0 pipeline-deploy"
        ;;

    pipeline-deploy)
        echo "Deploying CI/CD pipeline..."
        cd "$PROJECT_DIR"
        copilot pipeline deploy
        echo ""
        echo "Pipeline deployed! You may need to authorize the GitHub connection in AWS Console:"
        echo "AWS Console -> Developer Tools -> Settings -> Connections"
        ;;

    pipeline-status)
        echo "Checking pipeline status..."
        cd "$PROJECT_DIR"
        copilot pipeline status
        ;;

    *)
        echo "Usage: $0 <action> [environment]"
        echo ""
        echo "Actions:"
        echo "  init            - Initialize Copilot app (first time only)"
        echo "  env             - Deploy environment infrastructure"
        echo "  secrets         - Configure Claude and GitHub secrets"
        echo "  deploy          - Deploy/update the frank service"
        echo "  status          - Check service status"
        echo "  logs            - Stream service logs"
        echo "  exec            - Open shell in container"
        echo "  url             - Get service URL"
        echo "  delete          - Delete service from environment"
        echo "  delete-all      - Delete entire application"
        echo ""
        echo "Pipeline Actions:"
        echo "  pipeline-init   - Initialize CI/CD pipeline"
        echo "  pipeline-deploy - Deploy/update the pipeline"
        echo "  pipeline-status - Check pipeline status"
        echo ""
        echo "Environments: dev, prod"
        echo ""
        echo "Example workflow (manual):"
        echo "  $0 init              # First time setup"
        echo "  $0 env dev           # Deploy dev environment"
        echo "  $0 secrets dev       # Configure secrets"
        echo "  $0 deploy dev        # Deploy service"
        echo "  $0 url dev           # Get URL"
        echo ""
        echo "Example workflow (CI/CD):"
        echo "  $0 init              # First time setup"
        echo "  $0 env dev           # Deploy dev environment"
        echo "  $0 env prod          # Deploy prod environment"
        echo "  $0 pipeline-init     # Initialize pipeline"
        echo "  $0 pipeline-deploy   # Deploy pipeline"
        echo "  # Now pushes to main will auto-deploy!"
        ;;
esac

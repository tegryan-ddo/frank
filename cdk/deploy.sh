#!/bin/bash
set -e

# Frank CDK Deployment Script
# Prerequisites: AWS CLI configured, Node.js installed

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "=== Frank CDK Deployment ==="

# Check prerequisites
if ! command -v node &> /dev/null; then
    echo "Error: Node.js not found"
    echo "Install: https://nodejs.org/"
    exit 1
fi

if ! command -v aws &> /dev/null; then
    echo "Error: AWS CLI not found"
    exit 1
fi

cd "$SCRIPT_DIR"

# Ensure dependencies are installed
if [ ! -d "node_modules" ]; then
    echo "Installing dependencies..."
    npm install
fi

ACTION="${1:-help}"

case "$ACTION" in
    bootstrap)
        echo "Bootstrapping CDK in AWS account..."
        npx cdk bootstrap
        ;;

    deploy)
        echo "Deploying Frank stack..."
        npx cdk deploy --require-approval never
        echo ""
        echo "Deployment complete!"
        echo "Don't forget to set your secrets:"
        echo '  aws secretsmanager put-secret-value --secret-id /frank/github-token --secret-string "$(gh auth token)"'
        echo '  aws secretsmanager put-secret-value --secret-id /frank/claude-credentials --secret-string "$(cat ~/.claude/.credentials.json)"'
        ;;

    secrets)
        echo "Setting up secrets..."

        # GitHub token
        GH_TOKEN=$(gh auth token 2>/dev/null || true)
        if [ -z "$GH_TOKEN" ]; then
            echo "Enter your GitHub token (run 'gh auth token' to get it):"
            read -r GH_TOKEN
        else
            echo "Using token from 'gh auth token'"
        fi

        if [ -n "$GH_TOKEN" ]; then
            echo "Updating GitHub token..."
            aws secretsmanager put-secret-value --secret-id /frank/github-token --secret-string "$GH_TOKEN"
        fi

        # Claude credentials
        CRED_FILE="$HOME/.claude/.credentials.json"
        if [ -f "$CRED_FILE" ]; then
            echo "Updating Claude credentials..."
            aws secretsmanager put-secret-value --secret-id /frank/claude-credentials --secret-string "$(cat "$CRED_FILE")"
            echo "Secrets updated!"
        else
            echo "Warning: Claude credentials not found at $CRED_FILE"
            echo "Run 'claude' to authenticate first."
        fi
        ;;

    diff)
        echo "Showing changes..."
        npx cdk diff
        ;;

    synth)
        echo "Synthesizing CloudFormation template..."
        npx cdk synth
        ;;

    destroy)
        echo "WARNING: This will destroy all Frank infrastructure!"
        read -p "Are you sure? (yes/no): " CONFIRM
        if [ "$CONFIRM" = "yes" ]; then
            npx cdk destroy --force
        else
            echo "Cancelled."
        fi
        ;;

    logs)
        echo "Streaming logs..."
        aws logs tail /ecs/frank --follow
        ;;

    exec)
        echo "Getting task ARN..."
        TASK_ARN=$(aws ecs list-tasks --cluster frank --service-name frank --query "taskArns[0]" --output text)
        if [ -n "$TASK_ARN" ] && [ "$TASK_ARN" != "None" ]; then
            echo "Connecting to task: $TASK_ARN"
            aws ecs execute-command --cluster frank --task "$TASK_ARN" --container frank --interactive --command "/bin/bash"
        else
            echo "No running tasks found"
        fi
        ;;

    status)
        echo "Service status:"
        aws ecs describe-services --cluster frank --services frank --query "services[0].{Status:status,Running:runningCount,Desired:desiredCount,Pending:pendingCount}" --output table
        ;;

    *)
        echo "Usage: $0 <action>"
        echo ""
        echo "Actions:"
        echo "  bootstrap     - One-time CDK bootstrap (run first)"
        echo "  deploy        - Deploy/update the Frank stack"
        echo "  secrets       - Configure GitHub and Claude secrets"
        echo "  diff          - Show pending changes"
        echo "  synth         - Output CloudFormation template"
        echo "  destroy       - Destroy all infrastructure"
        echo ""
        echo "Operations:"
        echo "  status        - Check service status"
        echo "  logs          - Stream container logs"
        echo "  exec          - Shell into running container"
        echo ""
        echo "First-time setup:"
        echo "  1. $0 bootstrap    # One-time CDK setup"
        echo "  2. $0 deploy       # Deploy infrastructure"
        echo "  3. $0 secrets      # Set credentials"
        echo ""
        echo "URL: https://frank.digitaldevops.io"
        ;;
esac

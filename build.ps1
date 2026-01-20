# Frank CLI Build Script for Windows PowerShell
# Usage: .\build.ps1 [command]
# Commands: build, install, test, docker, clean, help

param(
    [Parameter(Position=0)]
    [string]$Command = "build"
)

$ErrorActionPreference = "Stop"

# Variables
$BinaryName = "frank.exe"
$ImageName = "frank-dev:latest"
$EcsImageName = "frank-ecs:latest"

function Show-Help {
    Write-Host "Frank CLI Build Script" -ForegroundColor Cyan
    Write-Host ""
    Write-Host "Usage: .\build.ps1 [command]"
    Write-Host ""
    Write-Host "Commands:"
    Write-Host "  build       Build the CLI binary (default)"
    Write-Host "  install     Install to Go bin path"
    Write-Host "  test        Run tests"
    Write-Host "  docker      Build Docker image (local)"
    Write-Host "  docker-ecs  Build Docker image (ECS optimized)"
    Write-Host "  push-ecr    Build and push ECS image to ECR"
    Write-Host "  clean       Clean build artifacts"
    Write-Host "  help        Show this help"
}

function Build-Binary {
    Write-Host "Building $BinaryName..." -ForegroundColor Yellow
    go build -trimpath -o $BinaryName .
    if ($LASTEXITCODE -eq 0) {
        Write-Host "Build successful: $BinaryName" -ForegroundColor Green
    } else {
        Write-Host "Build failed" -ForegroundColor Red
        exit 1
    }
}

function Install-Binary {
    Write-Host "Installing frank..." -ForegroundColor Yellow
    go install -trimpath .
    if ($LASTEXITCODE -eq 0) {
        Write-Host "Install successful" -ForegroundColor Green
    } else {
        Write-Host "Install failed" -ForegroundColor Red
        exit 1
    }
}

function Run-Tests {
    Write-Host "Running tests..." -ForegroundColor Yellow
    go test -v ./...
}

function Build-Docker {
    Write-Host "Building Docker image: $ImageName..." -ForegroundColor Yellow
    docker build -t $ImageName -f build/Dockerfile build/
    if ($LASTEXITCODE -eq 0) {
        Write-Host "Docker build successful: $ImageName" -ForegroundColor Green
    } else {
        Write-Host "Docker build failed" -ForegroundColor Red
        exit 1
    }
}

function Build-DockerEcs {
    Write-Host "Building ECS Docker image: $EcsImageName..." -ForegroundColor Yellow
    docker build -t $EcsImageName -f build/Dockerfile.ecs build/
    if ($LASTEXITCODE -eq 0) {
        Write-Host "ECS Docker build successful: $EcsImageName" -ForegroundColor Green
    } else {
        Write-Host "ECS Docker build failed" -ForegroundColor Red
        exit 1
    }
}

function Push-Ecr {
    param(
        [string]$EcrRepo = $env:FRANK_ECR_REPO
    )

    if (-not $EcrRepo) {
        Write-Host "Error: ECR repository URL required" -ForegroundColor Red
        Write-Host "Set FRANK_ECR_REPO environment variable or pass as argument" -ForegroundColor Yellow
        exit 1
    }

    # Build ECS image first
    Build-DockerEcs

    # Get AWS account and region from ECR URL
    $EcrRegion = ($EcrRepo -split '\.')[3]

    Write-Host "Logging into ECR..." -ForegroundColor Yellow
    aws ecr get-login-password --region $EcrRegion | docker login --username AWS --password-stdin ($EcrRepo -split '/')[0]

    Write-Host "Tagging and pushing to ECR..." -ForegroundColor Yellow
    docker tag $EcsImageName "${EcrRepo}:latest"
    docker push "${EcrRepo}:latest"

    # Also tag with timestamp
    $Timestamp = Get-Date -Format "yyyyMMdd-HHmmss"
    docker tag $EcsImageName "${EcrRepo}:${Timestamp}"
    docker push "${EcrRepo}:${Timestamp}"

    if ($LASTEXITCODE -eq 0) {
        Write-Host "Push successful: ${EcrRepo}:latest" -ForegroundColor Green
    } else {
        Write-Host "Push failed" -ForegroundColor Red
        exit 1
    }
}

function Clean-Artifacts {
    Write-Host "Cleaning build artifacts..." -ForegroundColor Yellow
    if (Test-Path $BinaryName) {
        Remove-Item $BinaryName -Force
        Write-Host "Removed $BinaryName" -ForegroundColor Green
    }
    # Clean any platform-specific builds
    Get-ChildItem -Filter "frank-*" | Remove-Item -Force
    Write-Host "Clean complete" -ForegroundColor Green
}

# Main switch
switch ($Command.ToLower()) {
    "build" { Build-Binary }
    "install" { Install-Binary }
    "test" { Run-Tests }
    "docker" { Build-Docker }
    "docker-ecs" { Build-DockerEcs }
    "push-ecr" { Push-Ecr }
    "clean" { Clean-Artifacts }
    "help" { Show-Help }
    default {
        Write-Host "Unknown command: $Command" -ForegroundColor Red
        Show-Help
        exit 1
    }
}

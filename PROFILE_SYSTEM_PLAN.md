# Frank Profile System Implementation Plan

## Overview
Replace the worker/router system with a profile-based approach where:
- `frank ecs start <profile>` launches a task at `<profile>.frank.digitaldevops.io`
- `frank.digitaldevops.io` serves a launch page listing all profiles
- Profiles are stored locally in `~/.config/frank/profiles.yaml`

---

## Phase 1: Remove Router System
**Goal**: Clean removal of the secondary worker infrastructure

### Step 1.1: Remove router command
- [ ] Delete `cmd/router.go`
- [ ] Remove any router references from other files

### Step 1.2: Update CDK stack
- [ ] Remove `RouterTask` task definition (lines ~255-333 in frank-stack.ts)
- [ ] Remove `RouterService` ECS service
- [ ] Remove router target group
- [ ] Remove `/w/*` and `/workers` listener rules
- [ ] Remove router security group rules

### Step 1.3: Clean up ecs.go
- [ ] Remove worker URL printing code (lines 276-281)
- [ ] Remove `--name` flag from `frank ecs run` command
- [ ] Remove `ecsRunName` variable and related logic

### Verification
```bash
go build .                    # Should succeed
cd cdk && npx cdk diff        # Should show only removals
```

### Commit
```
Remove router/worker system

- Delete cmd/router.go
- Remove RouterService and RouterTask from CDK
- Remove /w/* ALB listener rules
- Clean up --name flag from ecs run command
```

---

## Phase 2: Wildcard Certificate & DNS
**Goal**: Enable `*.frank.digitaldevops.io` routing

### Step 2.1: Request new ACM certificate
```bash
aws acm request-certificate \
  --domain-name frank.digitaldevops.io \
  --subject-alternative-names "*.frank.digitaldevops.io" \
  --validation-method DNS \
  --region us-east-1
```

### Step 2.2: Validate certificate
- [ ] Add DNS validation records to Route53
- [ ] Wait for certificate to be issued

### Step 2.3: Update CDK with new certificate
- [ ] Update `certificateArn` in `cdk/bin/frank.ts`
- [ ] Add wildcard Route53 record: `*.frank.digitaldevops.io` → ALB

### Verification
```bash
# Check cert is issued
aws acm describe-certificate --certificate-arn <new-arn> --query 'Certificate.Status'

# After CDK deploy, test DNS
nslookup test.frank.digitaldevops.io
```

### Commit
```
Add wildcard DNS and certificate for profile subdomains

- Request ACM cert covering *.frank.digitaldevops.io
- Add wildcard Route53 A record pointing to ALB
- Update CDK to use new certificate
```

---

## Phase 3: Profile Configuration System
**Goal**: YAML-based profile management stored locally

### Step 3.1: Define profile types
Create `internal/profile/profile.go`:
```go
type Profile struct {
    Name        string `yaml:"name"`
    Repo        string `yaml:"repo"`
    Branch      string `yaml:"branch"`
    Description string `yaml:"description,omitempty"`
}

type ProfileConfig struct {
    Profiles map[string]Profile `yaml:"profiles"`
}
```

### Step 3.2: Profile storage functions
Create `internal/profile/storage.go`:
- [ ] `LoadProfiles() (*ProfileConfig, error)` - load from ~/.config/frank/profiles.yaml
- [ ] `SaveProfiles(*ProfileConfig) error` - save to file
- [ ] `GetProfile(name string) (*Profile, error)` - get single profile
- [ ] `AddProfile(Profile) error` - add/update profile
- [ ] `RemoveProfile(name string) error` - delete profile

### Step 3.3: Add profile CLI commands
Create `cmd/profile.go`:
- [ ] `frank profile list` - table of all profiles
- [ ] `frank profile add <name> --repo <url> [--branch <branch>] [--description <desc>]`
- [ ] `frank profile show <name>` - detailed view
- [ ] `frank profile remove <name>` - delete with confirmation

### Example profiles.yaml
```yaml
profiles:
  enkai:
    repo: https://github.com/barff/enkai.git
    branch: main
    description: Enkai development environment
  frank:
    repo: https://github.com/barff/autoclauto.git
    branch: main
    description: Frank CLI development
```

### Verification
```bash
frank profile add test --repo https://github.com/test/repo.git --branch main
frank profile list                    # Shows test profile
frank profile show test               # Shows details
frank profile remove test             # Removes it
cat ~/.config/frank/profiles.yaml     # Verify file format
```

### Commit
```
Add profile configuration system

- Add internal/profile package for profile management
- Add frank profile commands (list, add, show, remove)
- Store profiles in ~/.config/frank/profiles.yaml
```

---

## Phase 4: Dynamic ALB Infrastructure
**Goal**: Create target groups and listener rules on-demand per profile

### Step 4.1: Add ALB management package
Create `internal/alb/alb.go`:
```go
type Manager struct {
    elbClient *elbv2.Client
    vpcID     string
    albArn    string
    listenerArn string
}

func (m *Manager) EnsureTargetGroup(profileName string) (string, error)
func (m *Manager) EnsureListenerRule(profileName, targetGroupArn string) error
func (m *Manager) RegisterTarget(targetGroupArn, ip string, port int) error
func (m *Manager) DeregisterTarget(targetGroupArn, ip string, port int) error
func (m *Manager) DeleteTargetGroup(profileName string) error
func (m *Manager) DeleteListenerRule(profileName string) error
```

### Step 4.2: Target group naming convention
- Name: `frank-profile-<name>` (e.g., `frank-profile-enkai`)
- Port: 7681 (Claude ttyd)
- Health check: `/claude/` path, port 7681
- Tags: `frank-profile=<name>`

### Step 4.3: Listener rule convention
- Priority: Hash of profile name to avoid conflicts (e.g., 100-999 range)
- Condition: Host header = `<profile>.frank.digitaldevops.io`
- Action: Forward to profile's target group

### Step 4.4: Discover ALB/VPC from CloudFormation
```go
func DiscoverInfrastructure() (*InfraConfig, error) {
    // Query CloudFormation stack outputs for:
    // - ALB ARN
    // - HTTPS Listener ARN
    // - VPC ID
    // - Subnet IDs
}
```

### Verification
```bash
# Manual test of ALB functions
go test ./internal/alb/...
```

### Commit
```
Add dynamic ALB management for profiles

- Add internal/alb package for target group and listener rule management
- Discover infrastructure from CloudFormation stack outputs
- Support creating/deleting per-profile ALB resources
```

---

## Phase 5: Profile-Aware ECS Commands
**Goal**: `frank ecs start/stop/list` work with profiles

### Step 5.1: Replace `frank ecs run` with `frank ecs start <profile>`
```go
func runECSStart(cmd *cobra.Command, args []string) error {
    profileName := args[0]

    // 1. Load profile
    profile, err := profile.GetProfile(profileName)

    // 2. Check if already running
    existingTask := findTaskByProfile(profileName)
    if existingTask != nil {
        fmt.Printf("Profile %s is already running\n", profileName)
        fmt.Printf("URL: https://%s.frank.digitaldevops.io/claude/\n", profileName)
        return nil
    }

    // 3. Ensure ALB infrastructure
    tgArn, _ := albManager.EnsureTargetGroup(profileName)
    albManager.EnsureListenerRule(profileName, tgArn)

    // 4. Start ECS task with profile config
    task := startTask(profile, profileName)

    // 5. Wait for task IP and register in target group
    ip := waitForTaskIP(task)
    albManager.RegisterTarget(tgArn, ip, 7681)

    // 6. Print URL
    fmt.Printf("URL: https://%s.frank.digitaldevops.io/claude/\n", profileName)
}
```

### Step 5.2: Task tagging for discovery
When starting a task, add tags:
```go
Tags: []types.Tag{
    {Key: aws.String("frank-profile"), Value: aws.String(profileName)},
}
```

### Step 5.3: Update `frank ecs stop <profile>`
```go
func runECSStop(cmd *cobra.Command, args []string) error {
    profileName := args[0]

    // Find task by profile tag
    task := findTaskByProfile(profileName)
    if task == nil {
        return fmt.Errorf("no running task for profile %s", profileName)
    }

    // Deregister from target group
    albManager.DeregisterTarget(...)

    // Stop task
    stopTask(task)
}
```

### Step 5.4: Update `frank ecs list` to show profiles
```
PROFILE     TASK ID      STATUS    URL
enkai       abc123...    RUNNING   https://enkai.frank.digitaldevops.io/claude/
frank       def456...    RUNNING   https://frank.frank.digitaldevops.io/claude/
```

### Step 5.5: Keep `frank ecs run` for ad-hoc tasks (optional)
- Rename to indicate it's for non-profile tasks
- Or remove entirely

### Verification
```bash
frank profile add test --repo https://github.com/test/test.git
frank ecs start test          # Creates infra, starts task, prints URL
frank ecs list                # Shows test profile running
frank ecs start test          # Says already running, prints URL
frank ecs stop test           # Stops task
frank ecs list                # Shows no tasks
```

### Commit
```
Implement profile-based ECS task management

- Add frank ecs start <profile> command
- Add frank ecs stop <profile> command
- Update frank ecs list to show profile names and URLs
- Tag tasks with profile name for discovery
- Integrate with dynamic ALB management
```

---

## Phase 6: Launch Page
**Goal**: `frank.digitaldevops.io` shows a dashboard of profiles

### Step 6.1: Create launch page HTML
Create `build/launch-page/index.html`:
- Simple responsive HTML/CSS
- JavaScript to fetch profile status from API
- Links to running profiles
- Status indicators

### Step 6.2: Create status API
Options:
A. **Lambda function** - Lightweight, serverless
B. **Repurpose router container** - Already have infra
C. **Add endpoint to frank tasks** - Each task exposes `/api/profiles`

Recommend **Option A (Lambda)** for simplicity:
- API Gateway + Lambda
- Lambda queries ECS for running tasks with `frank-profile` tag
- Returns JSON: `[{name: "enkai", status: "running", url: "..."}]`

### Step 6.3: Update CDK for launch page
- [ ] Add S3 bucket for static HTML (or use existing ECS task)
- [ ] Add Lambda for profile API
- [ ] Add API Gateway
- [ ] Update ALB to route `frank.digitaldevops.io` to launch page

### Step 6.4: Launch page features
- List of configured profiles (from hardcoded list or API)
- Running status with green/red indicators
- Direct links to `/claude/` for each profile
- "Start" button (optional - could call API to start)

### Verification
- Visit `https://frank.digitaldevops.io`
- See list of profiles
- Click running profile link
- Verify redirects work

### Commit
```
Add launch page dashboard at frank.digitaldevops.io

- Add static HTML launch page
- Add Lambda API for profile status
- Update ALB routing for main domain
```

---

## Phase 7: Cleanup & Documentation
**Goal**: Polish and document

### Step 7.1: Remove dead code
- [ ] Any remaining router references
- [ ] Unused config fields
- [ ] Old worker-related code

### Step 7.2: Update CLAUDE.md
- [ ] Document profile system
- [ ] Update ECS commands
- [ ] Add examples

### Step 7.3: Update README if exists

### Verification
```bash
go build .
go vet ./...
go test ./...
```

### Commit
```
Cleanup and documentation for profile system

- Remove dead code
- Update CLAUDE.md with profile documentation
- Add usage examples
```

---

## Rollback Plan
If issues arise:
1. CDK changes can be reverted with `cdk deploy` using previous code
2. Certificate change is additive (old cert still exists)
3. Profile config is local-only, no cloud state to revert

---

## Dependencies & Order
```
Phase 1 (Remove Router)
    ↓
Phase 2 (Wildcard Cert) ──→ Can be done in parallel with Phase 3
    ↓
Phase 3 (Profile Config)
    ↓
Phase 4 (ALB Management) ──→ Depends on Phase 2 (needs ALB info)
    ↓
Phase 5 (ECS Commands) ──→ Depends on Phase 3 + 4
    ↓
Phase 6 (Launch Page) ──→ Can be done after Phase 5, or in parallel
    ↓
Phase 7 (Cleanup)
```

---

## Estimated Scope
- **Phase 1**: ~50 lines removed, CDK changes
- **Phase 2**: AWS CLI + CDK config change
- **Phase 3**: ~200 lines new Go code
- **Phase 4**: ~300 lines new Go code
- **Phase 5**: ~400 lines modified/new Go code
- **Phase 6**: ~100 lines HTML/JS, ~150 lines CDK/Lambda
- **Phase 7**: Cleanup only

**Total new code**: ~1000-1200 lines
**Files touched**: ~15 files

# /work - Implement a Todo Item

Take a work item from the `todo/` folder, create a proper feature specification, and implement it through the appropriate agents and workflows.

## Arguments

- `$ARGUMENTS` - Required: Either a todo filename, slug, or partial match (e.g., "dark-mode", "2024-01-15-add-auth.md", or "authentication")

## Instructions

### Step 1: Find and Load the Todo Item

1. List files in `todo/` folder
2. Match `$ARGUMENTS` against:
   - Exact filename
   - Filename slug (the part after the date)
   - Partial text match in filename or content
3. If multiple matches, use AskUserQuestion to let user select
4. If no matches, report available todos and ask for clarification

Read the matched todo file to understand the work item.

### Step 2: Update Todo Status

Edit the todo file to change:
- `**Status**: idea` to `**Status**: in-progress`

### Step 3: Gather Additional Context

Use the Explore agent to deeply understand:
1. **Codebase architecture** relevant to this feature
2. **Existing patterns** that should be followed
3. **Dependencies** and integration points
4. **Test patterns** used in the project

### Step 4: Create Feature Specification

Use the Plan agent (EnterPlanMode) to create a detailed implementation plan:

1. **Technical Design**
   - Architecture decisions
   - Component breakdown
   - Data flow
   - API changes (if any)

2. **Implementation Steps**
   - Ordered list of changes
   - Files to create/modify
   - Dependencies to add

3. **Testing Strategy**
   - Unit tests needed
   - Integration tests
   - Manual testing steps

4. **Risk Assessment**
   - Breaking changes
   - Migration needs
   - Rollback plan

Save the spec to: `todo/specs/<original-slug>-spec.md`

### Step 5: Get User Approval

Present the implementation plan and ask for approval before proceeding:
- Show the high-level approach
- Highlight any significant decisions
- Note any risks or concerns
- Ask if user wants to proceed, modify, or abort

### Step 6: Implement the Feature

Upon approval, proceed with implementation:

1. **Create a feature branch** (if in a git repo)
   ```bash
   git checkout -b feature/<slug>
   ```

2. **Implement changes** following the spec:
   - Write code following existing patterns
   - Add appropriate tests
   - Update documentation if needed

3. **Run tests** to verify changes work

4. **Self-review** the changes for:
   - Code quality
   - Security concerns
   - Performance implications

### Step 7: Finalize

1. **Update the todo file**:
   - Change status to `completed`
   - Add completion date
   - Link to any PRs or commits

2. **Report completion**:
   - Summary of what was implemented
   - Files changed
   - Tests added
   - Any follow-up items

3. **Offer next steps**:
   - Create a PR (if applicable)
   - Run additional tests
   - Move to another todo item

## Workflow Summary

```
Load Todo -> Update Status -> Explore Codebase -> Create Spec ->
Get Approval -> Implement -> Test -> Finalize
```

## Example Usage

```
/work dark-mode
/work 2024-01-15-add-authentication.md
/work auth
/work                                    # Lists available todos to choose from
```

## Status Transitions

- `idea` - Initial captured state
- `in-progress` - Currently being worked on
- `blocked` - Waiting on external dependency
- `completed` - Implementation finished
- `cancelled` - Decided not to implement

## Notes

- The command will pause for user approval before making significant changes
- If implementation encounters issues, it will pause and ask for guidance
- Complex features may be broken into smaller sub-tasks
- All changes should follow existing code patterns and conventions

# /todo - Capture Ideas for Future Work

Capture an idea, ask clarifying questions if needed, perform light research, and save it as a structured markdown file in the `todo/` folder.

## Arguments

- `$ARGUMENTS` - Required: The idea or feature to capture (can be brief or detailed)

## Instructions

### Step 1: Parse the Input

Extract the core idea from `$ARGUMENTS`. If the input is vague or could be interpreted multiple ways, proceed to clarification.

### Step 2: Ask Clarifying Questions (if needed)

Use the AskUserQuestion tool to gather more context if the idea is:
- Ambiguous or could mean different things
- Missing critical details (scope, target users, constraints)
- Could have multiple implementation approaches

Example questions to consider:
- What problem does this solve?
- Who is the target user/audience?
- Are there any constraints or requirements?
- What's the desired outcome?
- Any related features or dependencies?

Skip this step if the idea is already clear and well-defined.

### Step 3: Light Research

Use available tools to gather context:
1. **Codebase exploration** - Use the Explore agent to understand:
   - Related existing code or features
   - Patterns that might be relevant
   - Potential integration points
2. **Web search** (if applicable) - Look up:
   - Best practices for the feature type
   - Similar implementations or libraries
   - Potential pitfalls to avoid

Keep research focused and brief - this is for context, not deep analysis.

### Step 4: Create the Todo Document

Generate a markdown file in the `todo/` folder with this structure:

**Filename**: `todo/YYYY-MM-DD-<slug>.md` where:
- Date is today's date
- Slug is a kebab-case summary (e.g., `add-user-authentication`, `improve-search-performance`)

**Content structure**:
```markdown
# <Title>

**Created**: <date>
**Status**: idea
**Priority**: <low|medium|high> (infer from context or default to medium)

## Summary

<1-2 sentence description of the idea>

## Problem Statement

<What problem does this solve? Why is it needed?>

## Proposed Solution

<High-level approach or solution>

## Research Notes

<Key findings from codebase exploration and web research>
- Relevant existing code: <files/patterns found>
- External references: <links or best practices>
- Potential approaches: <options discovered>

## Open Questions

<Any remaining questions or decisions to be made>

## Acceptance Criteria

<What does "done" look like? Bullet points>

## Notes

<Any additional context, constraints, or considerations>
```

### Step 5: Report Back

After creating the file:
1. Show the file path created
2. Provide a brief summary of what was captured
3. Mention any open questions that might need resolution before implementation

## Example Usage

```
/todo add dark mode support to the web UI
/todo improve API response times for large datasets
/todo integrate with Slack for notifications
```

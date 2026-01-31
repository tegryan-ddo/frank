# /dd - Deep Dive into a Topic

Perform an exhaustive, multi-layered exploration of a topic, produce a detailed implementation plan, then decompose it into buildable parts and delegate them to sub-agents for parallel execution.

## Arguments

- `$ARGUMENTS` - Required: The topic, question, or area to deep dive into

## Instructions

### Phase 1: Research

#### Step 1: Frame the Investigation

Parse `$ARGUMENTS` and define the scope:

1. **Core question**: What exactly are we trying to understand or build?
2. **Sub-questions**: Break the topic into 3-7 key facets or angles to investigate
3. **Boundaries**: What's in scope vs out of scope?

If the topic is ambiguous, use AskUserQuestion to clarify intent before proceeding.

#### Step 2: Deep Codebase Exploration

Use the Explore agent (thoroughly) to investigate every relevant angle:

1. **Architecture & structure** — How does the relevant code fit together? What are the layers, modules, and boundaries?
2. **Implementation details** — Read the actual code. Trace execution paths. Understand the logic, not just the shape.
3. **Data flow** — How does data move through the system? What transforms it?
4. **Dependencies & integration points** — What does this touch? What touches it?
5. **History** — Use git log/blame to understand how and why things evolved
6. **Edge cases & error handling** — What happens when things go wrong?
7. **Tests** — What's tested? What's not? What do the tests reveal about intended behavior?

Launch multiple Explore agents in parallel for independent sub-questions to maximize coverage.

#### Step 3: External Research

Where relevant, broaden the investigation beyond the codebase:

1. **Documentation** — Check official docs, READMEs, and inline documentation
2. **Web search** — Research best practices, known issues, common patterns, and alternatives
3. **Library/framework docs** — Use context7 or other MCP tools to get up-to-date documentation for relevant libraries
4. **AWS docs** — If AWS services are involved, use the AWS documentation MCP tools

#### Step 4: Deep Thinking

Use the sequential-thinking MCP tool to reason carefully through the findings:

1. **Synthesize** — Connect the dots between what you've found across different angles
2. **Identify patterns** — What recurring themes, strengths, or weaknesses emerge?
3. **Surface tensions** — Where are there trade-offs, contradictions, or technical debt?
4. **Consider alternatives** — What other approaches exist? Why were current choices made?
5. **Assess risks** — What could break? What's fragile? What's robust?

### Phase 2: Plan

#### Step 5: Produce the Deep Dive Report & Plan

Present findings and the implementation plan in a structured report directly in the conversation:

```
## Deep Dive: <Topic>

### Overview
<Executive summary — 2-3 sentences capturing the key insight>

### Key Findings
<Numbered list of the most important discoveries, each with supporting detail>

### Architecture / How It Works
<Detailed explanation with code references (file:line format)>
<Include diagrams if they help clarify relationships>

### Strengths
<What's working well and why>

### Concerns & Risks
<Issues, technical debt, fragility, or gaps discovered>

### Alternatives & Trade-offs
<Other approaches considered or possible, with pros/cons>

### Implementation Plan

#### Work Package 1: <Name>
- **Goal**: What this package accomplishes
- **Files**: List of files to create/modify
- **Details**: Specific changes, logic, and approach
- **Dependencies**: What must exist before this can start (other packages or nothing)

#### Work Package 2: <Name>
...

#### Work Package N: <Name>
...

#### Dependency Graph
<Show which packages can run in parallel vs which must be sequential>
```

#### Step 6: Get User Approval

Present the plan and ask for approval before building:
- Highlight key architectural decisions
- Note any risks or trade-offs
- Ask if the user wants to proceed, modify, or stop at the research phase

### Phase 3: Build

#### Step 7: Decompose and Delegate

Once approved, use TaskCreate to create a task for each work package from the plan. Then execute them:

1. **Identify parallelizable work** — Group work packages by their dependency chains
2. **Launch independent packages in parallel** — Use the Task tool with `subagent_type=general-purpose` to delegate each independent work package as its own sub-agent. Send all independent tasks in a single message for true parallel execution.
3. **Launch dependent packages sequentially** — Once blocking packages complete, launch the next wave of packages that depended on them.

Each sub-agent prompt MUST include:
- The full context of what to build (from the work package)
- The specific files to create or modify
- The coding patterns and conventions to follow (discovered during research)
- Clear acceptance criteria
- Instructions to write the actual code, not just plan it

#### Step 8: Verify and Integrate

After all sub-agents complete:

1. **Review all changes** — Read through what each agent produced
2. **Integration check** — Verify the pieces fit together correctly
3. **Run tests** — If tests exist or were created, run them
4. **Fix any issues** — Resolve conflicts, missing integrations, or inconsistencies between packages

#### Step 9: Report Completion

Present a summary:
- What was built (with file:line references)
- What each work package delivered
- Any issues encountered and how they were resolved
- Remaining open questions or follow-up work
- Offer to capture follow-ups as `/note` entries

## Principles

- **Thoroughness over speed** — Read the code, don't skim it. Trace the paths, don't guess.
- **Evidence-based** — Every claim should reference specific code, docs, or research. Use `file:line` references.
- **Multiple perspectives** — Consider the topic from the viewpoint of developers, users, operators, and the system itself.
- **Honest assessment** — Surface problems and risks clearly. Don't gloss over issues.
- **Structured output** — Make findings easy to navigate and act on.
- **Maximize parallelism** — Independent work packages should always be delegated simultaneously.
- **Self-contained agents** — Each sub-agent gets everything it needs to do its job without asking follow-up questions.

## Example Usage

```
/dd how does authentication work in this project
/dd the ECS task lifecycle and networking setup
/dd why are cold starts slow and fix them
/dd add a caching layer to the API
/dd refactor the profile system to support teams
```

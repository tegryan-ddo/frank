# MCP Launchpad Integration

This container includes MCP Launchpad (`mcpl`) for dynamic tool discovery and execution across multiple MCP servers.

## Available MCP Servers

The following servers are pre-configured:

| Server | Description |
|--------|-------------|
| `context7` | Context management and retrieval |
| `sequential-thinking` | Step-by-step reasoning and planning |
| `aws-documentation` | AWS documentation search |
| `aws-knowledge` | AWS knowledge base (Amazon Q) |
| `aws-core` | Core AWS service operations |

## How to Use MCP Tools

### 1. Discover Tools First

**Never guess tool names.** Always search for relevant tools:

```bash
# Search across all servers
mcpl search "your query here"

# Examples:
mcpl search "s3 bucket"
mcpl search "create file"
mcpl search "sequential thinking"
```

### 2. List Available Tools

```bash
# List all servers
mcpl list

# List tools in a specific server
mcpl list aws-core
mcpl list context7
```

### 3. Inspect Tool Details

```bash
# Get tool schema and example
mcpl inspect <server> <tool> --example

# Example:
mcpl inspect aws-core list_buckets --example
```

### 4. Execute Tools

```bash
# Call a tool with JSON arguments
mcpl call <server> <tool> '{"param": "value"}'

# Examples:
mcpl call sequential-thinking think '{"task": "Plan a web application"}'
mcpl call aws-core list_buckets '{}'
```

## Session Management

MCP Launchpad uses a daemon for persistent connections:

```bash
# Check session status
mcpl session status

# Stop the daemon (connections will restart on next use)
mcpl session stop

# Verify all server connections
mcpl verify
```

## Troubleshooting

### Tool Not Found
```bash
mcpl search "what you're looking for"
```

### Connection Issues
```bash
mcpl verify
mcpl session stop
```

### View Tool Schema
```bash
mcpl inspect <server> <tool>
```

## Best Practices

1. **Search before calling** - Use `mcpl search` to find the right tool
2. **Check parameters** - Use `mcpl inspect --example` to see required arguments
3. **Use JSON format** - Tool arguments must be valid JSON
4. **Verify connections** - Run `mcpl verify` if tools aren't responding

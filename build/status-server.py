#!/usr/bin/env python3
"""
Enhanced status server that exposes Claude Code usage info.
Reads from Claude's project files and serves detailed metrics via HTTP.
"""

import json
import os
import glob
import time
import sys
from http.server import HTTPServer, BaseHTTPRequestHandler
from pathlib import Path
from datetime import datetime

STATUS_PORT = int(os.environ.get('STATUS_PORT', 7683))
LOG_FILE = '/tmp/status-server.log'

def log(msg):
    """Write to log file for debugging."""
    try:
        with open(LOG_FILE, 'a') as f:
            f.write(f"{datetime.now().isoformat()} - {msg}\n")
    except:
        pass

# Model pricing (per million tokens)
MODEL_PRICING = {
    'sonnet': {'input': 3.0, 'output': 15.0},
    'opus': {'input': 15.0, 'output': 75.0},
    'haiku': {'input': 0.25, 'output': 1.25},
}

# Context window sizes
CONTEXT_WINDOWS = {
    'sonnet': 200000,
    'opus': 200000,
    'haiku': 200000,
}

class StatusHandler(BaseHTTPRequestHandler):
    def log_message(self, format, *args):
        pass  # Suppress logging

    def do_GET(self):
        try:
            if self.path == '/status':
                self.send_response(200)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Access-Control-Allow-Origin', '*')
                self.end_headers()

                status = get_claude_status()
                self.wfile.write(json.dumps(status).encode())
            elif self.path == '/status/detailed':
                self.send_response(200)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Access-Control-Allow-Origin', '*')
                self.end_headers()

                status = get_detailed_status()
                self.wfile.write(json.dumps(status).encode())
            elif self.path == '/status/debug':
                self.send_response(200)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Access-Control-Allow-Origin', '*')
                self.end_headers()

                debug = get_debug_info()
                self.wfile.write(json.dumps(debug, indent=2).encode())
            elif self.path == '/health':
                self.send_response(200)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Access-Control-Allow-Origin', '*')
                self.end_headers()
                self.wfile.write(json.dumps({'status': 'ok'}).encode())
            else:
                self.send_response(404)
                self.end_headers()
        except Exception as e:
            log(f"Error handling request {self.path}: {e}")
            self.send_response(500)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Access-Control-Allow-Origin', '*')
            self.end_headers()
            self.wfile.write(json.dumps({'error': str(e)}).encode())

    def do_OPTIONS(self):
        self.send_response(200)
        self.send_header('Access-Control-Allow-Origin', '*')
        self.send_header('Access-Control-Allow-Methods', 'GET, OPTIONS')
        self.end_headers()

def find_jsonl_files(base_dir):
    """Find all JSONL files in various possible locations."""
    jsonl_files = []

    # Possible locations for Claude Code conversation data
    search_paths = [
        base_dir / 'projects',           # Original location
        base_dir / 'sessions',           # Alternative location
        base_dir / 'conversations',      # Another possibility
        base_dir,                         # Root .claude directory
    ]

    for search_path in search_paths:
        if search_path.exists():
            found = list(search_path.glob('**/*.jsonl'))
            jsonl_files.extend(found)

    # Also check workspace-relative .claude directory
    workspace_claude = Path('/workspace/.claude')
    if workspace_claude.exists():
        found = list(workspace_claude.glob('**/*.jsonl'))
        jsonl_files.extend(found)

    # Check worktree directories for their own .claude folders
    # Each container works in /workspace/.worktrees/$CONTAINER_NAME
    worktrees_dir = Path('/workspace/.worktrees')
    if worktrees_dir.exists():
        for worktree in worktrees_dir.iterdir():
            if worktree.is_dir():
                worktree_claude = worktree / '.claude'
                if worktree_claude.exists():
                    found = list(worktree_claude.glob('**/*.jsonl'))
                    jsonl_files.extend(found)

    # Also check current working directory's .claude (for when running in worktree)
    cwd_claude = Path.cwd() / '.claude'
    if cwd_claude.exists() and cwd_claude != workspace_claude:
        found = list(cwd_claude.glob('**/*.jsonl'))
        jsonl_files.extend(found)

    # Deduplicate
    seen = set()
    unique_files = []
    for f in jsonl_files:
        if str(f) not in seen:
            seen.add(str(f))
            unique_files.append(f)

    return unique_files

def is_claude_running():
    """Check if Claude Code process is running."""
    import subprocess
    try:
        result = subprocess.run(['pgrep', '-f', 'claude'], capture_output=True, text=True)
        return result.returncode == 0
    except:
        return False

def get_claude_status():
    """Read Claude Code status from various sources."""
    status = {
        'model': None,
        'context_used': None,
        'context_max': None,
        'tokens_in': None,
        'tokens_out': None,
        'cost': None,
        'message_count': 0,
        'turn_count': 0,
        'cache_read': None,
        'cache_creation': None,
        'last_updated': None,
        'connected': is_claude_running(),
        'files_in_context': [],
        'tool_uses': {},
    }

    home = Path.home()
    claude_dir = home / '.claude'

    # First, try to read from .claude.json (main Claude Code config/session file)
    claude_json = home / '.claude.json'
    if claude_json.exists():
        try:
            with open(claude_json) as f:
                data = json.load(f)
                log(f"Found .claude.json with keys: {list(data.keys())}")

                # Model info
                if 'model' in data:
                    status['model'] = simplify_model_name(data['model'])

                # Check for projects array (stores conversation contexts)
                if 'projects' in data and isinstance(data['projects'], dict):
                    log(f"projects keys: {list(data['projects'].keys())[:5]}")

                # Check for any usage or stats
                for key in ['usage', 'stats', 'tokenUsage', 'session']:
                    if key in data:
                        log(f"{key}: {data[key]}")
                        if isinstance(data[key], dict):
                            # Extract token info if available
                            if 'input_tokens' in data[key]:
                                status['tokens_in'] = data[key]['input_tokens']
                            if 'output_tokens' in data[key]:
                                status['tokens_out'] = data[key]['output_tokens']
                            if 'total_cost' in data[key]:
                                status['cost'] = data[key]['total_cost']

                # Update timestamp
                status['last_updated'] = datetime.fromtimestamp(
                    claude_json.stat().st_mtime
                ).isoformat()
        except Exception as e:
            log(f"Error reading .claude.json: {e}")

    # Check for JSONL conversation files (older Claude Code versions)
    conversation_files = find_jsonl_files(claude_dir)
    if conversation_files:
        log(f"Found {len(conversation_files)} JSONL files")
        latest = max(conversation_files, key=lambda p: p.stat().st_mtime)
        log(f"Using latest: {latest}")
        status = parse_conversation_file(latest, status)
        status['last_updated'] = datetime.fromtimestamp(
            latest.stat().st_mtime
        ).isoformat()

    # Try to read from settings for model info
    settings_file = claude_dir / 'settings.json'
    if settings_file.exists():
        try:
            with open(settings_file) as f:
                settings = json.load(f)
                if 'model' in settings and not status['model']:
                    status['model'] = simplify_model_name(settings['model'])
        except:
            pass

    # Try local project settings
    workspace_claude = Path('/workspace/.claude')
    if workspace_claude.exists():
        local_settings = workspace_claude / 'settings.local.json'
        if local_settings.exists():
            try:
                with open(local_settings) as f:
                    settings = json.load(f)
                    if 'model' in settings:
                        status['model'] = simplify_model_name(settings['model'])
            except:
                pass

    # Check statsig for any cached usage data
    statsig_dir = claude_dir / 'statsig'
    if statsig_dir.exists():
        try:
            for f in statsig_dir.iterdir():
                if f.suffix == '.json':
                    try:
                        with open(f) as sf:
                            sdata = json.load(sf)
                            log(f"Statsig file {f.name} keys: {list(sdata.keys())[:10]}")
                    except:
                        pass
        except:
            pass

    # Check todos directory for active tasks
    todos_dir = claude_dir / 'todos'
    if todos_dir.exists():
        try:
            todo_files = list(todos_dir.glob('*.json'))
            if todo_files:
                status['active_todos'] = len(todo_files)
                log(f"Found {len(todo_files)} todo files")
        except:
            pass

    # Set context window based on model
    model_key = (status['model'] or 'sonnet').lower()
    status['context_max'] = CONTEXT_WINDOWS.get(model_key, 200000)

    # Calculate context used from tokens
    if status['tokens_in'] is not None:
        status['context_used'] = status['tokens_in']
        if status['cache_read']:
            status['context_used'] += status['cache_read']

    return status

def get_detailed_status():
    """Get detailed breakdown of conversation metrics."""
    status = get_claude_status()

    # Add detailed breakdown
    detailed = {
        **status,
        'turns': [],
        'files_in_context': [],
        'tool_uses': {},
    }

    home = Path.home()
    claude_dir = home / '.claude'

    # Find JSONL files in various locations
    conversation_files = find_jsonl_files(claude_dir)
    if conversation_files:
        latest = max(conversation_files, key=lambda p: p.stat().st_mtime)
        detailed = parse_detailed_conversation(latest, detailed)

    return detailed

def get_debug_info():
    """Get debug info about what files the status server can see."""
    home = Path.home()
    claude_dir = home / '.claude'

    debug = {
        'home': str(home),
        'user': os.environ.get('USER', 'unknown'),
        'claude_dir': str(claude_dir),
        'claude_dir_exists': claude_dir.exists(),
        'claude_dir_contents': [],
        'subdirs': {},
        'jsonl_files': [],
        'sample_entries': [],
        'log_file': LOG_FILE,
        'log_contents': [],
    }

    # Get .claude directory contents and subdirectory contents
    if claude_dir.exists():
        try:
            debug['claude_dir_contents'] = [str(p.name) for p in claude_dir.iterdir()]
            # Also list contents of subdirectories
            for subdir in claude_dir.iterdir():
                if subdir.is_dir():
                    try:
                        debug['subdirs'][subdir.name] = [str(p.name) for p in list(subdir.iterdir())[:10]]
                    except:
                        pass
        except:
            pass

    # Check workspace .claude too
    workspace_claude = Path('/workspace/.claude')
    if workspace_claude.exists():
        try:
            debug['workspace_claude_contents'] = [str(p.name) for p in workspace_claude.iterdir()]
        except:
            pass

    # Get last 20 log lines
    try:
        if os.path.exists(LOG_FILE):
            with open(LOG_FILE) as f:
                lines = f.readlines()
                debug['log_contents'] = [l.strip() for l in lines[-20:]]
    except:
        pass

    # Find all JSONL files
    conversation_files = find_jsonl_files(claude_dir)
    if conversation_files:
        debug['jsonl_files'] = [
            {
                'path': str(f),
                'size': f.stat().st_size,
                'mtime': f.stat().st_mtime,
            }
            for f in sorted(conversation_files, key=lambda p: p.stat().st_mtime, reverse=True)[:10]
        ]

        # Get sample entries from the most recent file
        if conversation_files:
            latest = max(conversation_files, key=lambda p: p.stat().st_mtime)
            try:
                with open(latest) as f:
                    for i, line in enumerate(f):
                        if i >= 5:  # First 5 entries
                            break
                        line = line.strip()
                        if line:
                            try:
                                entry = json.loads(line)
                                # Only include keys, not full content
                                msg_keys = []
                                msg_usage = None
                                if 'message' in entry and isinstance(entry['message'], dict):
                                    msg_keys = list(entry['message'].keys())
                                    if 'usage' in entry['message']:
                                        msg_usage = list(entry['message']['usage'].keys())
                                debug['sample_entries'].append({
                                    'keys': list(entry.keys()),
                                    'type': entry.get('type'),
                                    'role': entry.get('role'),
                                    'has_usage': 'usage' in entry,
                                    'has_message': 'message' in entry,
                                    'message_keys': msg_keys,
                                    'message_usage_keys': msg_usage,
                                    'costUSD': entry.get('costUSD'),
                                })
                            except:
                                debug['sample_entries'].append({'error': 'parse failed'})
            except Exception as e:
                debug['sample_error'] = str(e)

    return debug

def parse_conversation_file(filepath, status):
    """Parse a Claude conversation JSONL file for token usage."""
    # Track cumulative totals for cost calculation
    total_out = 0
    total_cost = 0

    # Track the LAST usage entry for context window (current state)
    last_input_tokens = 0
    last_cache_read = 0
    last_cache_creation = 0

    model = None
    message_count = 0
    turn_count = 0

    try:
        with open(filepath) as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue
                try:
                    entry = json.loads(line)
                    entry_type = entry.get('type', '')
                    role = entry.get('role', '')

                    # Count messages - handle both 'type' and 'role' formats
                    if entry_type in ('user', 'assistant', 'human', 'ai') or role in ('user', 'assistant', 'human'):
                        message_count += 1
                        if entry_type in ('user', 'human') or role in ('user', 'human'):
                            turn_count += 1

                    # Look for usage data in message entries
                    # For context window: use the LAST entry's input tokens (current context)
                    # For output tokens: accumulate (total generated)
                    if 'usage' in entry:
                        usage = entry['usage']
                        if 'input_tokens' in usage:
                            last_input_tokens = usage['input_tokens']
                        if 'output_tokens' in usage:
                            total_out += usage['output_tokens']
                        # Cache tokens (if using prompt caching)
                        if 'cache_read_input_tokens' in usage:
                            last_cache_read = usage['cache_read_input_tokens']
                        if 'cache_creation_input_tokens' in usage:
                            last_cache_creation = usage['cache_creation_input_tokens']

                    # Look for message.usage (nested format)
                    if 'message' in entry and isinstance(entry['message'], dict):
                        msg = entry['message']
                        if 'usage' in msg:
                            usage = msg['usage']
                            if 'input_tokens' in usage:
                                last_input_tokens = usage['input_tokens']
                            if 'output_tokens' in usage:
                                total_out += usage['output_tokens']
                            if 'cache_read_input_tokens' in usage:
                                last_cache_read = usage['cache_read_input_tokens']
                            if 'cache_creation_input_tokens' in usage:
                                last_cache_creation = usage['cache_creation_input_tokens']
                        if 'model' in msg:
                            model = msg['model']

                    # Look for model info
                    if 'model' in entry and entry['model']:
                        model = entry['model']

                    # Also check for costUSD in some formats
                    if 'costUSD' in entry:
                        total_cost += entry['costUSD']

                except json.JSONDecodeError:
                    continue
    except:
        pass

    status['message_count'] = message_count
    status['turn_count'] = turn_count

    # tokens_in = last input tokens (current context window usage)
    # tokens_out = total output tokens generated in session
    if last_input_tokens > 0 or total_out > 0:
        status['tokens_in'] = last_input_tokens
        status['tokens_out'] = total_out

        if last_cache_read > 0:
            status['cache_read'] = last_cache_read
        if last_cache_creation > 0:
            status['cache_creation'] = last_cache_creation

        # Use accumulated cost if available, otherwise calculate
        if total_cost > 0:
            status['cost'] = round(total_cost, 4)
        elif status['cost'] is None:
            model_key = simplify_model_name(model).lower() if model else 'sonnet'
            pricing = MODEL_PRICING.get(model_key, MODEL_PRICING['sonnet'])

            # For cost, use last input tokens (current context) + all output
            cost = (last_input_tokens * pricing['input'] / 1_000_000)
            cost += (total_out * pricing['output'] / 1_000_000)

            # Cache read tokens are 10% of input price
            if last_cache_read > 0:
                cost += (last_cache_read * pricing['input'] * 0.1 / 1_000_000)

            # Cache creation tokens are 25% more than input price
            if last_cache_creation > 0:
                cost += (last_cache_creation * pricing['input'] * 1.25 / 1_000_000)

            status['cost'] = round(cost, 4)

    if model:
        status['model'] = simplify_model_name(model)

    return status

def parse_detailed_conversation(filepath, detailed):
    """Parse conversation for detailed turn-by-turn breakdown."""
    turns = []
    current_turn = None
    tool_uses = {}
    files_seen = set()

    try:
        with open(filepath) as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue
                try:
                    entry = json.loads(line)
                    entry_type = entry.get('type', '')
                    role = entry.get('role', '')

                    # Track turns
                    if entry_type in ('user', 'human') or role in ('user', 'human'):
                        if current_turn:
                            turns.append(current_turn)
                        current_turn = {
                            'user_tokens': 0,
                            'assistant_tokens': 0,
                            'tools_used': [],
                            'timestamp': entry.get('timestamp'),
                        }

                    # Track tool usage - check multiple formats
                    tool_name = None
                    tool_input = None

                    # Format 1: top-level tool_use
                    if 'tool_use' in entry:
                        tool_data = entry['tool_use']
                        if isinstance(tool_data, dict):
                            tool_name = tool_data.get('name')
                            tool_input = tool_data.get('input', {})

                    # Format 2: type = tool_use with name field
                    if entry_type == 'tool_use' or entry.get('name'):
                        tool_name = tool_name or entry.get('name')
                        tool_input = tool_input or entry.get('input', {})

                    # Format 3: content array with tool_use blocks
                    if 'content' in entry and isinstance(entry['content'], list):
                        for block in entry['content']:
                            if isinstance(block, dict) and block.get('type') == 'tool_use':
                                block_name = block.get('name')
                                block_input = block.get('input', {})
                                if block_name:
                                    tool_uses[block_name] = tool_uses.get(block_name, 0) + 1
                                    if current_turn:
                                        current_turn['tools_used'].append(block_name)
                                    # Extract file paths from tool input
                                    extract_files_from_tool(block_name, block_input, files_seen)

                    # Format 4: message.content array
                    if 'message' in entry and isinstance(entry['message'], dict):
                        msg = entry['message']
                        if 'content' in msg and isinstance(msg['content'], list):
                            for block in msg['content']:
                                if isinstance(block, dict) and block.get('type') == 'tool_use':
                                    block_name = block.get('name')
                                    block_input = block.get('input', {})
                                    if block_name:
                                        tool_uses[block_name] = tool_uses.get(block_name, 0) + 1
                                        if current_turn:
                                            current_turn['tools_used'].append(block_name)
                                        extract_files_from_tool(block_name, block_input, files_seen)

                    if tool_name:
                        tool_uses[tool_name] = tool_uses.get(tool_name, 0) + 1
                        if current_turn:
                            current_turn['tools_used'].append(tool_name)
                        extract_files_from_tool(tool_name, tool_input or {}, files_seen)

                    # Add usage to current turn
                    if 'usage' in entry and current_turn:
                        usage = entry['usage']
                        current_turn['user_tokens'] += usage.get('input_tokens', 0)
                        current_turn['assistant_tokens'] += usage.get('output_tokens', 0)

                    # Check message.usage too
                    if 'message' in entry and isinstance(entry['message'], dict):
                        msg = entry['message']
                        if 'usage' in msg and current_turn:
                            usage = msg['usage']
                            current_turn['user_tokens'] += usage.get('input_tokens', 0)
                            current_turn['assistant_tokens'] += usage.get('output_tokens', 0)

                except json.JSONDecodeError:
                    continue

        if current_turn:
            turns.append(current_turn)

    except:
        pass

    detailed['turns'] = turns[-10:]  # Last 10 turns
    detailed['tool_uses'] = tool_uses
    detailed['files_in_context'] = sorted(list(files_seen))[:30]

    return detailed


def extract_files_from_tool(tool_name, tool_input, files_seen):
    """Extract file paths from tool input."""
    if not isinstance(tool_input, dict):
        return

    # Common file path fields
    file_fields = ['file_path', 'path', 'filename', 'notebook_path']
    for field in file_fields:
        if field in tool_input:
            path = tool_input[field]
            if isinstance(path, str) and path:
                # Normalize path
                path = path.replace('\\', '/')
                files_seen.add(path)

    # Glob patterns
    if tool_name == 'Glob' and 'pattern' in tool_input:
        pass  # Don't add glob patterns as files

    # Grep paths
    if tool_name == 'Grep' and 'path' in tool_input:
        path = tool_input['path']
        if isinstance(path, str) and path:
            files_seen.add(path.replace('\\', '/'))

def simplify_model_name(model):
    """Convert full model name to simple display name."""
    if not model:
        return None
    model_lower = model.lower()
    if 'sonnet' in model_lower:
        return 'Sonnet'
    elif 'opus' in model_lower:
        return 'Opus'
    elif 'haiku' in model_lower:
        return 'Haiku'
    else:
        # Return last part of model name, truncated
        return model.split('/')[-1][:20]

def main():
    log(f"Starting status server on port {STATUS_PORT}")
    log(f"Home directory: {Path.home()}")
    log(f"User: {os.environ.get('USER', 'unknown')}")
    log(f"Claude dir check: {Path.home() / '.claude'} exists: {(Path.home() / '.claude').exists()}")

    # List claude dir contents at startup
    claude_dir = Path.home() / '.claude'
    if claude_dir.exists():
        log(f"Initial .claude contents: {list(claude_dir.iterdir())}")
        projects_dir = claude_dir / 'projects'
        if projects_dir.exists():
            log(f"Projects dir contents: {list(projects_dir.iterdir())[:10]}")

    server = HTTPServer(('0.0.0.0', STATUS_PORT), StatusHandler)
    print(f"Status server running on port {STATUS_PORT}")
    log(f"Server started successfully on port {STATUS_PORT}")
    server.serve_forever()

if __name__ == '__main__':
    main()

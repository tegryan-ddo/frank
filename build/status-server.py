#!/usr/bin/env python3
"""
Enhanced status server that exposes Claude Code usage info.
Reads from Claude's project files and serves detailed metrics via HTTP.
Also captures prompts and uploads to S3 for analytics.
"""

import json
import os
import glob
import time
import sys
import re
import threading
import uuid
import hashlib
import base64
from http.server import HTTPServer, BaseHTTPRequestHandler
from pathlib import Path
from datetime import datetime
from collections import deque

# Try to import boto3 for S3 uploads (optional)
try:
    import boto3
    HAS_BOTO3 = True
except ImportError:
    HAS_BOTO3 = False

# Server configuration
# Now serves on WEB_PORT (7680) and handles both status API and static files
WEB_PORT = int(os.environ.get('WEB_PORT', 7680))
STATUS_PORT = int(os.environ.get('STATUS_PORT', 7683))  # Keep for backwards compat
LOG_FILE = '/tmp/status-server.log'

# Static file serving configuration
WEB_DIR = os.environ.get('WEB_DIR', '/tmp/frank-web')
URL_PREFIX = os.environ.get('URL_PREFIX', '')  # e.g., '/enkai' for profile routing

# MIME types for static file serving
MIME_TYPES = {
    '.html': 'text/html',
    '.css': 'text/css',
    '.js': 'application/javascript',
    '.json': 'application/json',
    '.png': 'image/png',
    '.jpg': 'image/jpeg',
    '.jpeg': 'image/jpeg',
    '.gif': 'image/gif',
    '.svg': 'image/svg+xml',
    '.ico': 'image/x-icon',
    '.woff': 'font/woff',
    '.woff2': 'font/woff2',
    '.ttf': 'font/ttf',
}

# Analytics configuration
ANALYTICS_BUCKET = os.environ.get('ANALYTICS_BUCKET', '')
ANALYTICS_ENABLED = os.environ.get('ANALYTICS_ENABLED', 'false').lower() == 'true'
CONTAINER_NAME = os.environ.get('CONTAINER_NAME', 'unknown')
AWS_REGION = os.environ.get('AWS_REGION', 'us-east-1')

# Buffer for prompts before S3 upload
prompt_buffer = deque(maxlen=100)
prompt_buffer_lock = threading.Lock()
last_upload_time = time.time()
UPLOAD_INTERVAL = 60  # Upload every 60 seconds
MAX_BUFFER_SIZE = 10  # Or when buffer reaches this size

# Track processed prompts to avoid duplicates
processed_prompt_ids = set()

# Current session info
current_session_id = str(uuid.uuid4())[:8]
last_prompt_id = None

# Active user tracking
active_users = {}  # {user_id: {display_name, email, short_id, last_seen, ...}}
active_users_lock = threading.Lock()
ACTIVE_USERS_FILE = '/workspace/.active-users.json'
USER_TIMEOUT_SECONDS = 120  # Remove users after 2 minutes of inactivity

# Version tracking for update detection
VERSION_CACHE = {
    'current_revision': None,
    'latest_revision': None,
    'task_family': None,
    'last_check': 0,
}
VERSION_CHECK_INTERVAL = 300  # Check for updates every 5 minutes

# Pnyx configuration
PNYX_API_URL = os.environ.get('PNYX_API_URL', 'https://pnyx.digitaldevops.io')
PNYX_CREDENTIALS_FILE = os.path.expanduser('~/.config/pnyx/credentials.json')

# Pnyx tick state
pnyx_tick_state = {
    'enabled': False,
    'interval_seconds': 1800,  # 30 minutes default
    'last_tick': None,
    'next_tick': None,
    'last_result': None,
    'timer_thread': None,
    'stop_event': None,
}
pnyx_tick_lock = threading.Lock()

# GitHub issue monitor state
GH_MONITOR_STATE_FILE = '/tmp/gh-monitor-state.json'

gh_monitor_state = {
    'enabled': False,
    'interval_seconds': 120,  # 2 minutes default
    'last_check': None,
    'next_check': None,
    'last_result': None,
    'timer_thread': None,
    'stop_event': None,
    'processed_issues': {},  # "owner/repo#number" -> {"processed_at": ..., "updated_at": ...}
    'backoff_until': None,   # Timestamp until which we should back off (rate limit/error recovery)
    'backoff_count': 0,      # Number of consecutive errors for exponential backoff
    'rate_limit': None,      # Last known rate limit info
}
gh_monitor_lock = threading.Lock()


def _save_gh_monitor_state():
    """Persist processed issues to disk so they survive container restarts."""
    try:
        with gh_monitor_lock:
            data = {
                'processed_issues': gh_monitor_state['processed_issues'],
                'saved_at': datetime.now().isoformat(),
            }
        with open(GH_MONITOR_STATE_FILE, 'w') as f:
            json.dump(data, f)
    except Exception as e:
        log(f"GH monitor: failed to save state: {e}")


def _load_gh_monitor_state():
    """Load processed issues from disk on startup."""
    try:
        if os.path.exists(GH_MONITOR_STATE_FILE):
            with open(GH_MONITOR_STATE_FILE, 'r') as f:
                data = json.load(f)
            processed = data.get('processed_issues', {})
            # Migrate from old set format (list of strings) to dict format
            if isinstance(processed, list):
                processed = {k: {'processed_at': data.get('saved_at', datetime.now().isoformat())} for k in processed}
            with gh_monitor_lock:
                gh_monitor_state['processed_issues'] = processed
            log(f"GH monitor: loaded {len(processed)} processed issues from disk")
    except Exception as e:
        log(f"GH monitor: failed to load state: {e}")


# Load persisted state on module init
_load_gh_monitor_state()

# Prompt textbox state (reported by web UI)
prompt_textbox_state = {
    'has_text': False,
    'last_updated': None,
}
prompt_textbox_lock = threading.Lock()

def log(msg):
    """Write to log file for debugging."""
    try:
        with open(LOG_FILE, 'a') as f:
            f.write(f"{datetime.now().isoformat()} - {msg}\n")
    except:
        pass


# =========================================================================
# Claude Idle Detection
# =========================================================================

def is_claude_idle(session='frank-claude'):
    """
    Check if Claude Code is idle (waiting for user input) by inspecting
    the tmux pane content. When idle, Claude shows the ❯ prompt character.
    When busy, it shows a spinner like ✽ Ionizing... or streaming output.
    """
    import subprocess

    try:
        result = subprocess.run(
            ['tmux', 'capture-pane', '-t', session, '-p'],
            capture_output=True, text=True, timeout=5
        )
        if result.returncode != 0:
            log(f"tmux capture-pane failed: {result.stderr}")
            return False

        pane_content = result.stdout
        if not pane_content.strip():
            return False

        # Get the last non-empty lines
        lines = [l for l in pane_content.split('\n') if l.strip()]
        if not lines:
            return False

        # Look for the idle prompt pattern in the last several lines
        # When idle, Claude shows: ❯ (with optional trailing space)
        # The prompt is typically near the bottom, surrounded by dash lines
        last_lines = lines[-8:]  # Check last 8 lines
        for line in last_lines:
            stripped = line.strip()
            # The idle prompt is just ❯ possibly with trailing whitespace
            if stripped == '❯' or stripped == '❯ ':
                return True

        return False

    except Exception as e:
        log(f"Error checking Claude idle state: {e}")
        return False


def is_prompt_textbox_empty():
    """Check if the web UI prompt textbox is empty (based on last reported state)."""
    with prompt_textbox_lock:
        # If we haven't heard from the UI in 30 seconds, assume empty
        # (user may not have the page open)
        if prompt_textbox_state['last_updated'] is None:
            return True
        age = time.time() - prompt_textbox_state['last_updated']
        if age > 30:
            return True
        return not prompt_textbox_state['has_text']


def get_claude_state(session='frank-claude'):
    """Get Claude's current state as a dict."""
    idle = is_claude_idle(session)
    prompt_empty = is_prompt_textbox_empty()
    return {
        'idle': idle,
        'prompt_empty': prompt_empty,
        'ready_for_tick': idle and prompt_empty,
    }


# =========================================================================
# Pnyx Tick Functions (sends /pnyx to Claude when idle)
# =========================================================================

PNYX_TICK_PROMPT = '/pnyx'  # The skill command to send to Claude

def get_pnyx_credentials():
    """Get Pnyx API credentials from file or environment."""
    # Try environment first
    api_key = os.environ.get('PNYX_API_KEY')
    if api_key:
        return {'api_key': api_key, 'api_url': PNYX_API_URL}

    # Try credentials file
    if os.path.exists(PNYX_CREDENTIALS_FILE):
        try:
            with open(PNYX_CREDENTIALS_FILE) as f:
                creds = json.load(f)
                return {
                    'api_key': creds.get('api_key'),
                    'api_url': creds.get('api_url', PNYX_API_URL)
                }
        except Exception as e:
            log(f"Error reading Pnyx credentials: {e}")

    return None


def run_pnyx_tick():
    """
    Run a single Pnyx tick cycle:
    1. Check if Claude is idle and prompt textbox is empty
    2. If ready, send /pnyx to Claude's tmux session
    3. Return summary of what happened
    """
    log("Running Pnyx tick...")

    # Check credentials first
    creds = get_pnyx_credentials()
    if not creds:
        return {'error': 'No Pnyx credentials configured', 'sent': False}

    # Check if Claude is ready
    state = get_claude_state()

    if not state['idle']:
        log("Pnyx tick skipped: Claude is busy")
        return {
            'timestamp': datetime.now().isoformat(),
            'sent': False,
            'skipped': True,
            'reason': 'Claude is busy',
            'claude_state': state,
        }

    if not state['prompt_empty']:
        log("Pnyx tick skipped: prompt textbox has text")
        return {
            'timestamp': datetime.now().isoformat(),
            'sent': False,
            'skipped': True,
            'reason': 'Prompt textbox has text',
            'claude_state': state,
        }

    # Claude is idle and prompt is empty - send the pnyx command
    success = send_to_tmux(PNYX_TICK_PROMPT, session='frank-claude', auto_submit=True)

    result = {
        'timestamp': datetime.now().isoformat(),
        'sent': success,
        'skipped': False,
        'prompt': PNYX_TICK_PROMPT,
        'claude_state': state,
    }

    if success:
        log(f"Pnyx tick sent '{PNYX_TICK_PROMPT}' to Claude")
    else:
        log("Pnyx tick failed to send prompt to Claude")
        result['error'] = 'Failed to send prompt to tmux'

    return result


def pnyx_tick_timer_loop(stop_event, interval_seconds):
    """Background timer loop for Pnyx tick."""
    while not stop_event.is_set():
        # Wait for interval or stop signal
        if stop_event.wait(timeout=interval_seconds):
            break  # Stop event was set

        # Run tick
        try:
            result = run_pnyx_tick()
            with pnyx_tick_lock:
                pnyx_tick_state['last_tick'] = datetime.now().isoformat()
                pnyx_tick_state['last_result'] = result
                pnyx_tick_state['next_tick'] = (
                    datetime.now().timestamp() + pnyx_tick_state['interval_seconds']
                )
        except Exception as e:
            log(f"Pnyx tick error: {e}")
            with pnyx_tick_lock:
                pnyx_tick_state['last_result'] = {'error': str(e), 'actions': []}

    log("Pnyx tick timer stopped")


def start_pnyx_tick_timer(interval_seconds=1800):
    """Start the Pnyx tick background timer."""
    global pnyx_tick_state

    with pnyx_tick_lock:
        # Stop existing timer if running
        if pnyx_tick_state['timer_thread'] and pnyx_tick_state['timer_thread'].is_alive():
            pnyx_tick_state['stop_event'].set()
            pnyx_tick_state['timer_thread'].join(timeout=5)

        # Create new timer
        stop_event = threading.Event()
        timer_thread = threading.Thread(
            target=pnyx_tick_timer_loop,
            args=(stop_event, interval_seconds),
            daemon=True
        )

        pnyx_tick_state['enabled'] = True
        pnyx_tick_state['interval_seconds'] = interval_seconds
        pnyx_tick_state['stop_event'] = stop_event
        pnyx_tick_state['timer_thread'] = timer_thread
        pnyx_tick_state['next_tick'] = datetime.now().timestamp() + interval_seconds

        timer_thread.start()
        log(f"Pnyx tick timer started (interval: {interval_seconds}s)")

    return True


def stop_pnyx_tick_timer():
    """Stop the Pnyx tick background timer."""
    global pnyx_tick_state

    with pnyx_tick_lock:
        if pnyx_tick_state['stop_event']:
            pnyx_tick_state['stop_event'].set()

        if pnyx_tick_state['timer_thread']:
            pnyx_tick_state['timer_thread'].join(timeout=5)

        pnyx_tick_state['enabled'] = False
        pnyx_tick_state['timer_thread'] = None
        pnyx_tick_state['stop_event'] = None
        pnyx_tick_state['next_tick'] = None

        log("Pnyx tick timer stopped")

    return True


def get_pnyx_tick_status():
    """Get the current Pnyx tick timer status."""
    with pnyx_tick_lock:
        status = {
            'enabled': pnyx_tick_state['enabled'],
            'interval_seconds': pnyx_tick_state['interval_seconds'],
            'last_tick': pnyx_tick_state['last_tick'],
            'last_result': pnyx_tick_state['last_result'],
            'has_credentials': get_pnyx_credentials() is not None,
        }

        if pnyx_tick_state['next_tick']:
            remaining = pnyx_tick_state['next_tick'] - datetime.now().timestamp()
            status['next_tick_in_seconds'] = max(0, int(remaining))
        else:
            status['next_tick_in_seconds'] = None

        return status


# =========================================================================
# GitHub Issue Monitor (checks for enkai: labeled issues)
# =========================================================================

def get_gh_repo():
    """Parse GIT_REPO env var to extract owner/repo (e.g. 'tegryan-ddo/enkai')."""
    git_repo = os.environ.get('GIT_REPO', '')
    if not git_repo:
        return None

    # Handle https://github.com/owner/repo.git or git@github.com:owner/repo.git
    import re
    match = re.match(r'(?:https?://github\.com/|git@github\.com:)([^/]+/[^/.]+?)(?:\.git)?$', git_repo)
    if match:
        return match.group(1)

    # Handle plain owner/repo format
    match = re.match(r'^([^/]+/[^/]+)$', git_repo)
    if match:
        return match.group(1)

    return None


# Configurable labels: read from GH_MONITOR_LABELS env var, or derive from container name.
# Format: comma-separated label names, e.g. "enkai:research,enkai:design,enkai:plan,enkai:build"
# If not set, defaults to {CONTAINER_NAME}:research, {CONTAINER_NAME}:design, etc.
def _get_gh_monitor_labels():
    env_labels = os.environ.get('GH_MONITOR_LABELS', '').strip()
    if env_labels:
        return [l.strip() for l in env_labels.split(',') if l.strip()]
    # Default: all containers use enkai: prefix
    return ['enkai:research', 'enkai:design', 'enkai:plan', 'enkai:build']


GH_MONITOR_LABELS = _get_gh_monitor_labels()

# Label suffix -> skill command routing
# "build" is special: triggers /scrum which processes ALL build issues at once
LABEL_SKILL_MAP = {
    'build': '/scrum',
    'design': '/design',
    'plan': '/plan',
    'research': '/dd',
}


def _get_issue_task_type(issue):
    """Extract the task type from an issue's label suffix (e.g., 'enkai:build' -> 'build')."""
    labels = [l.get('name', '') for l in issue.get('labels', [])]
    for label in labels:
        if ':' in label:
            task_type = label.split(':', 1)[1]
            if task_type in LABEL_SKILL_MAP:
                return task_type
    return None


def _sanitize_issue_body(body):
    """Strip HTML and truncate issue body for safe prompt inclusion."""
    body = body or '(no description)'
    body = re.sub(r'<[^>]+>', '', body)  # Strip HTML
    if len(body) > 4000:
        body = body[:4000] + '\n\n... (truncated)'
    return body


def get_task_prompt(issue, repo):
    """Build a skill-routed prompt for a monitored issue."""
    task_type = _get_issue_task_type(issue)
    skill = LABEL_SKILL_MAP.get(task_type, '')
    labels = [l.get('name', '') for l in issue.get('labels', [])]
    body = _sanitize_issue_body(issue.get('body', ''))
    all_labels = ', '.join(labels)

    if task_type == 'build':
        # /scrum handles all build issues itself — just invoke it
        return '/scrum'

    if skill:
        # Single-issue skills get the issue context as the argument
        return (
            f"{skill} Issue #{issue['number']}: {issue['title']} (repo: {repo})\n\n"
            f"Labels: {all_labels}\n\n"
            f"{body}\n\n"
            f"When done, close the issue with: gh issue close {issue['number']} --repo {repo}"
        )

    # Fallback for unknown label types
    return (
        f"Please work on this GitHub issue.\n\n"
        f"Issue #{issue['number']}: {issue['title']}\n"
        f"Labels: {all_labels}\n"
        f"Repository: {repo}\n\n"
        f"{body}\n\n"
        f"When done, close the issue with: gh issue close {issue['number']} --repo {repo}"
    )


def _check_rate_limit():
    """Check GitHub API rate limit and return info dict."""
    import subprocess
    try:
        result = subprocess.run(
            ['gh', 'api', 'rate_limit', '--jq', '.rate'],
            capture_output=True, text=True, timeout=10
        )
        if result.returncode == 0 and result.stdout.strip():
            info = json.loads(result.stdout)
            return {
                'limit': info.get('limit', 0),
                'remaining': info.get('remaining', 0),
                'used': info.get('used', 0),
                'reset': info.get('reset', 0),
            }
    except Exception as e:
        log(f"GH monitor: rate limit check failed: {e}")
    return None


def _gh_monitor_log(msg, result=None):
    """Write structured log entry for GH monitor activity."""
    try:
        entry = {
            'timestamp': datetime.now().isoformat(),
            'message': msg,
        }
        if result:
            entry['result'] = result
        with gh_monitor_lock:
            if gh_monitor_state.get('rate_limit'):
                entry['rate_limit_remaining'] = gh_monitor_state['rate_limit'].get('remaining')
        with open('/tmp/gh-monitor.log', 'a') as f:
            f.write(json.dumps(entry) + '\n')
    except Exception:
        pass


def check_github_issues():
    """
    Check GitHub for open issues with configured labels across all accessible repos.
    If Claude is idle, send one unprocessed issue as a prompt.
    Includes rate limit checking and exponential backoff on errors.
    """
    import subprocess

    # Check if we're in a backoff period
    with gh_monitor_lock:
        if gh_monitor_state['backoff_until']:
            if time.time() < gh_monitor_state['backoff_until']:
                remaining = int(gh_monitor_state['backoff_until'] - time.time())
                _gh_monitor_log(f"Backing off for {remaining}s more")
                return {
                    'timestamp': datetime.now().isoformat(),
                    'sent': False,
                    'skipped': True,
                    'reason': f'Backing off ({remaining}s remaining)',
                    'backoff': True,
                }
            else:
                # Backoff expired
                gh_monitor_state['backoff_until'] = None

    log(f"GH monitor: searching issues across all repos (labels: {GH_MONITOR_LABELS})...")

    # Search for each label across all accessible repos
    all_issues = {}  # keyed by "owner/repo#number" to deduplicate
    errors = []

    for label in GH_MONITOR_LABELS:
        try:
            result = subprocess.run(
                ['gh', 'search', 'issues', '--label', label,
                 '--state', 'open', '--json', 'repository,number,title,body,labels,updatedAt',
                 '--limit', '100'],
                capture_output=True, text=True, timeout=30
            )

            if result.returncode != 0:
                errors.append(f'{label}: {result.stderr.strip()[:80]}')
                continue

            issues = json.loads(result.stdout) if result.stdout.strip() else []
            for issue in issues:
                repo_name = issue.get('repository', {}).get('nameWithOwner', '')
                if not repo_name:
                    continue
                key = f"{repo_name}#{issue['number']}"
                if key not in all_issues:
                    all_issues[key] = {**issue, '_repo': repo_name, '_key': key}

        except subprocess.TimeoutExpired:
            errors.append(f'{label}: timed out')
        except Exception as e:
            errors.append(f'{label}: {e}')

    # Check rate limit after API calls
    rate_info = _check_rate_limit()
    if rate_info:
        with gh_monitor_lock:
            gh_monitor_state['rate_limit'] = rate_info
        if rate_info['remaining'] < 100:
            log(f"GH monitor: rate limit low ({rate_info['remaining']}/{rate_info['limit']})")
            _gh_monitor_log(f"Rate limit low: {rate_info['remaining']}/{rate_info['limit']}")

    if errors and not all_issues:
        # All labels failed — apply exponential backoff
        with gh_monitor_lock:
            gh_monitor_state['backoff_count'] += 1
            backoff_secs = min(1800, 30 * (2 ** gh_monitor_state['backoff_count']))  # Max 30 min
            gh_monitor_state['backoff_until'] = time.time() + backoff_secs
        _gh_monitor_log(f"All labels failed, backing off {backoff_secs}s", {'errors': errors})
        return {
            'timestamp': datetime.now().isoformat(),
            'error': '; '.join(errors),
            'issues_found': 0,
            'sent': False,
            'backoff_seconds': backoff_secs,
        }

    # Success — reset backoff counter
    with gh_monitor_lock:
        gh_monitor_state['backoff_count'] = 0

    issues = list(all_issues.values())

    # Filter out already-processed issues (check for updates too)
    with gh_monitor_lock:
        processed = gh_monitor_state['processed_issues']
        new_issues = []
        for i in issues:
            key = i['_key']
            if key not in processed:
                new_issues.append(i)
            else:
                # Check if issue was updated since we last processed it
                issue_updated = i.get('updatedAt', '')
                last_updated = processed[key].get('updated_at', '')
                if issue_updated and last_updated and issue_updated > last_updated:
                    new_issues.append(i)  # Re-process updated issue

    if not new_issues:
        return {
            'timestamp': datetime.now().isoformat(),
            'issues_found': len(issues),
            'new_issues': 0,
            'sent': False,
            'skipped': True,
            'reason': 'No new issues' if not issues else 'All issues already processed',
        }

    # Check if Claude is ready
    state = get_claude_state()
    if not state['idle']:
        return {
            'timestamp': datetime.now().isoformat(),
            'issues_found': len(issues),
            'new_issues': len(new_issues),
            'sent': False,
            'skipped': True,
            'reason': 'Claude is busy',
            'claude_state': state,
        }

    if not state['prompt_empty']:
        return {
            'timestamp': datetime.now().isoformat(),
            'issues_found': len(issues),
            'new_issues': len(new_issues),
            'sent': False,
            'skipped': True,
            'reason': 'Prompt textbox has text',
            'claude_state': state,
        }

    # Separate build issues from single-issue tasks
    build_issues = [i for i in new_issues if _get_issue_task_type(i) == 'build']
    single_issues = [i for i in new_issues if _get_issue_task_type(i) != 'build']

    # Build issues: send /scrum once, which processes all build issues in parallel
    if build_issues:
        prompt = '/scrum'
        success = send_to_tmux(prompt, session='frank-claude', auto_submit=True)

        build_keys = [i['_key'] for i in build_issues]
        result = {
            'timestamp': datetime.now().isoformat(),
            'issues_found': len(issues),
            'new_issues': len(new_issues),
            'sent': success,
            'mode': 'scrum',
            'build_issues': len(build_issues),
            'issue_keys': build_keys,
            'claude_state': state,
        }

        if success:
            with gh_monitor_lock:
                for i in build_issues:
                    gh_monitor_state['processed_issues'][i['_key']] = {
                        'processed_at': datetime.now().isoformat(),
                        'updated_at': i.get('updatedAt', ''),
                    }
            _save_gh_monitor_state()
            log(f"GH monitor: sent /scrum for {len(build_issues)} build issues")
            _gh_monitor_log(f"Sent /scrum for {len(build_issues)} build issues", result)
        else:
            log(f"GH monitor: failed to send /scrum")
            _gh_monitor_log(f"Failed to send /scrum", result)

        return result

    # Single-issue tasks: process one per tick
    issue = single_issues[0]
    repo = issue['_repo']
    prompt = get_task_prompt(issue, repo)

    success = send_to_tmux(prompt, session='frank-claude', auto_submit=True)

    issue_key = issue['_key']
    result = {
        'timestamp': datetime.now().isoformat(),
        'issues_found': len(issues),
        'new_issues': len(new_issues),
        'sent': success,
        'mode': 'skill',
        'skill': LABEL_SKILL_MAP.get(_get_issue_task_type(issue), 'unknown'),
        'issue_key': issue_key,
        'issue_number': issue['number'],
        'issue_title': issue['title'],
        'issue_repo': repo,
        'claude_state': state,
    }

    if success:
        with gh_monitor_lock:
            gh_monitor_state['processed_issues'][issue_key] = {
                'processed_at': datetime.now().isoformat(),
                'updated_at': issue.get('updatedAt', ''),
            }
        _save_gh_monitor_state()
        log(f"GH monitor: sent {LABEL_SKILL_MAP.get(_get_issue_task_type(issue), '')} for {issue_key}")
        _gh_monitor_log(f"Sent {issue_key}", result)
    else:
        log(f"GH monitor: failed to send {issue_key}")
        _gh_monitor_log(f"Failed to send {issue_key}", result)

    return result


def gh_monitor_timer_loop(stop_event, interval_seconds):
    """Background timer loop for GitHub issue monitoring."""
    while not stop_event.is_set():
        if stop_event.wait(timeout=interval_seconds):
            break

        try:
            result = check_github_issues()
            with gh_monitor_lock:
                gh_monitor_state['last_check'] = datetime.now().isoformat()
                gh_monitor_state['last_result'] = result
                gh_monitor_state['next_check'] = (
                    datetime.now().timestamp() + gh_monitor_state['interval_seconds']
                )
        except Exception as e:
            log(f"GH monitor error: {e}")
            with gh_monitor_lock:
                gh_monitor_state['last_result'] = {'error': str(e)}

    log("GH monitor timer stopped")


def start_gh_monitor(interval_seconds=120):
    """Start the GitHub issue monitor background timer."""
    global gh_monitor_state

    with gh_monitor_lock:
        # Stop existing timer if running
        if gh_monitor_state['timer_thread'] and gh_monitor_state['timer_thread'].is_alive():
            gh_monitor_state['stop_event'].set()
            gh_monitor_state['timer_thread'].join(timeout=5)

        stop_event = threading.Event()
        timer_thread = threading.Thread(
            target=gh_monitor_timer_loop,
            args=(stop_event, interval_seconds),
            daemon=True
        )

        gh_monitor_state['enabled'] = True
        gh_monitor_state['interval_seconds'] = interval_seconds
        gh_monitor_state['stop_event'] = stop_event
        gh_monitor_state['timer_thread'] = timer_thread
        gh_monitor_state['next_check'] = datetime.now().timestamp() + interval_seconds

        timer_thread.start()
        log(f"GH monitor started (interval: {interval_seconds}s)")

    return True


def stop_gh_monitor():
    """Stop the GitHub issue monitor background timer."""
    global gh_monitor_state

    with gh_monitor_lock:
        if gh_monitor_state['stop_event']:
            gh_monitor_state['stop_event'].set()

        if gh_monitor_state['timer_thread']:
            gh_monitor_state['timer_thread'].join(timeout=5)

        gh_monitor_state['enabled'] = False
        gh_monitor_state['timer_thread'] = None
        gh_monitor_state['stop_event'] = None
        gh_monitor_state['next_check'] = None

        log("GH monitor stopped")

    return True


def get_gh_monitor_status():
    """Get the current GitHub issue monitor status."""
    with gh_monitor_lock:
        status = {
            'enabled': gh_monitor_state['enabled'],
            'interval_seconds': gh_monitor_state['interval_seconds'],
            'last_check': gh_monitor_state['last_check'],
            'last_result': gh_monitor_state['last_result'],
            'processed_count': len(gh_monitor_state['processed_issues']),
            'labels': GH_MONITOR_LABELS,
            'rate_limit': gh_monitor_state.get('rate_limit'),
        }

        if gh_monitor_state.get('backoff_until') and time.time() < gh_monitor_state['backoff_until']:
            status['backoff_seconds'] = int(gh_monitor_state['backoff_until'] - time.time())
        else:
            status['backoff_seconds'] = None

        if gh_monitor_state['next_check']:
            remaining = gh_monitor_state['next_check'] - datetime.now().timestamp()
            status['next_check_in_seconds'] = max(0, int(remaining))
        else:
            status['next_check_in_seconds'] = None

        return status


# =========================================================================
# User Extraction and Tracking (Multi-User Sessions)
# =========================================================================

def extract_user_from_headers(headers):
    """
    Extract user information from Cognito ALB headers.

    ALB injects these headers after Cognito authentication:
    - x-amzn-oidc-identity: The user's sub (unique ID)
    - x-amzn-oidc-data: JWT containing claims (email, name, etc.)
    - x-amzn-oidc-accesstoken: Access token (not used here)

    Returns: {user_id, email, display_name, short_id} or None if not authenticated
    """
    user_id = headers.get('x-amzn-oidc-identity', '')
    oidc_data = headers.get('x-amzn-oidc-data', '')

    if not user_id:
        return None

    # Generate short_id from user_id hash (8 characters)
    short_id = hashlib.sha256(user_id.encode()).hexdigest()[:8]

    # Default values
    email = ''
    display_name = ''

    # Try to decode the JWT payload for email and name
    if oidc_data:
        try:
            # JWT format: header.payload.signature
            parts = oidc_data.split('.')
            if len(parts) >= 2:
                # Decode payload (base64url encoded)
                payload = parts[1]
                # Add padding if needed
                padding = 4 - len(payload) % 4
                if padding != 4:
                    payload += '=' * padding
                decoded = base64.urlsafe_b64decode(payload)
                claims = json.loads(decoded)

                email = claims.get('email', '')
                # Try various name fields
                display_name = (
                    claims.get('name') or
                    claims.get('preferred_username') or
                    claims.get('cognito:username') or
                    email.split('@')[0] if email else ''
                )
        except Exception as e:
            log(f"Error decoding OIDC JWT: {e}")

    # If no display name, use email prefix or short_id
    if not display_name:
        display_name = email.split('@')[0] if email else f"user-{short_id}"

    return {
        'user_id': user_id,
        'email': email,
        'display_name': display_name,
        'short_id': short_id,
    }


def update_active_user(user_info):
    """Update or add a user to the active users list."""
    if not user_info:
        return

    user_id = user_info['user_id']
    now = time.time()

    with active_users_lock:
        active_users[user_id] = {
            'user_id': user_id,
            'email': user_info.get('email', ''),
            'display_name': user_info.get('display_name', ''),
            'short_id': user_info.get('short_id', ''),
            'last_seen': now,
            'first_seen': active_users.get(user_id, {}).get('first_seen', now),
        }

        # Clean up stale users
        cleanup_stale_users()

        # Persist to file for Lambda to read
        persist_active_users()


def cleanup_stale_users():
    """Remove users who haven't been seen recently."""
    now = time.time()
    stale_ids = [
        uid for uid, info in active_users.items()
        if now - info.get('last_seen', 0) > USER_TIMEOUT_SECONDS
    ]
    for uid in stale_ids:
        del active_users[uid]
        log(f"Removed stale user: {uid[:8]}...")


def persist_active_users():
    """Save active users to a JSON file for Lambda to read."""
    try:
        # Prepare data (don't expose full user_id)
        users_data = {
            'profile': CONTAINER_NAME,
            'updated_at': datetime.now().isoformat(),
            'user_count': len(active_users),
            'users': [
                {
                    'short_id': info['short_id'],
                    'display_name': info['display_name'],
                    'last_seen': datetime.fromtimestamp(info['last_seen']).isoformat(),
                }
                for info in active_users.values()
            ]
        }

        with open(ACTIVE_USERS_FILE, 'w') as f:
            json.dump(users_data, f, indent=2)
    except Exception as e:
        log(f"Error persisting active users: {e}")


def get_active_users_list():
    """Get list of active users (for API response)."""
    with active_users_lock:
        cleanup_stale_users()
        return [
            {
                'short_id': info['short_id'],
                'display_name': info['display_name'],
                'last_seen': datetime.fromtimestamp(info['last_seen']).isoformat(),
            }
            for info in active_users.values()
        ]


def check_port(port, timeout=2):
    """Check if a service is listening on a port."""
    import socket
    try:
        s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        s.settimeout(timeout)
        result = s.connect_ex(('localhost', port))
        s.close()
        return result == 0
    except:
        return False


def get_current_version():
    """Read current task definition revision from file."""
    try:
        revision_file = '/tmp/frank/current-revision'
        family_file = '/tmp/frank/task-family'

        if os.path.exists(revision_file):
            with open(revision_file) as f:
                VERSION_CACHE['current_revision'] = f.read().strip()
        if os.path.exists(family_file):
            with open(family_file) as f:
                VERSION_CACHE['task_family'] = f.read().strip()

        return VERSION_CACHE['current_revision']
    except Exception as e:
        log(f"Error reading current version: {e}")
        return None


def get_latest_version():
    """Query ECS for the latest task definition revision."""
    global VERSION_CACHE

    # Check cache freshness
    now = time.time()
    if now - VERSION_CACHE['last_check'] < VERSION_CHECK_INTERVAL and VERSION_CACHE['latest_revision']:
        return VERSION_CACHE['latest_revision']

    if not HAS_BOTO3:
        return None

    try:
        family = VERSION_CACHE.get('task_family')
        if not family:
            get_current_version()
            family = VERSION_CACHE.get('task_family')

        if not family:
            return None

        ecs = boto3.client('ecs', region_name=AWS_REGION)

        # Get the latest ACTIVE task definition for this family
        response = ecs.list_task_definitions(
            familyPrefix=family,
            status='ACTIVE',
            sort='DESC',
            maxResults=1
        )

        if response.get('taskDefinitionArns'):
            latest_arn = response['taskDefinitionArns'][0]
            # Extract revision from ARN (e.g., "...:42" -> "42")
            latest_rev = latest_arn.split(':')[-1]
            VERSION_CACHE['latest_revision'] = latest_rev
            VERSION_CACHE['last_check'] = now
            log(f"Latest task definition: {family}:{latest_rev}")
            return latest_rev

    except Exception as e:
        log(f"Error checking latest version: {e}")

    return None


def get_version_status():
    """Get version status for update detection."""
    current = get_current_version()
    latest = get_latest_version()

    update_available = False
    if current and latest:
        try:
            update_available = int(latest) > int(current)
        except ValueError:
            update_available = latest != current

    return {
        'current_revision': current,
        'latest_revision': latest,
        'task_family': VERSION_CACHE.get('task_family'),
        'update_available': update_available,
        'running_in_ecs': current is not None,
    }

def get_health_status():
    """Get comprehensive health status checking all services."""
    ttyd_port = int(os.environ.get('TTYD_PORT', 7681))
    bash_port = int(os.environ.get('BASH_PORT', 7682))

    checks = {
        'web_server': True,  # We're responding, so yes
        'claude_terminal': check_port(ttyd_port),
        'bash_terminal': check_port(bash_port),
    }

    all_healthy = all(checks.values())
    return {
        'status': 'ok' if all_healthy else 'degraded',
        'checks': checks,
    }

def serve_static_file(handler, file_path):
    """Serve a static file with proper MIME type."""
    try:
        # Ensure path is within WEB_DIR (security)
        abs_path = os.path.abspath(file_path)
        abs_web_dir = os.path.abspath(WEB_DIR)
        if not abs_path.startswith(abs_web_dir):
            handler.send_response(403)
            handler.end_headers()
            return

        if not os.path.exists(file_path):
            handler.send_response(404)
            handler.end_headers()
            return

        # Get MIME type
        ext = os.path.splitext(file_path)[1].lower()
        content_type = MIME_TYPES.get(ext, 'application/octet-stream')

        # Read and serve file
        with open(file_path, 'rb') as f:
            content = f.read()

        handler.send_response(200)
        handler.send_header('Content-Type', content_type)
        handler.send_header('Content-Length', len(content))
        handler.send_header('Access-Control-Allow-Origin', '*')
        handler.end_headers()
        handler.wfile.write(content)
    except Exception as e:
        log(f"Error serving static file {file_path}: {e}")
        handler.send_response(500)
        handler.end_headers()

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

def send_to_tmux(text, session='frank-claude', auto_submit=False):
    """Send text to a tmux session using load-buffer/paste-buffer for safety."""
    import subprocess
    import tempfile

    try:
        # Clear any current input before pasting
        # Ctrl+C: cancel any current operation (more aggressive than Escape)
        # Small delay to let terminal settle
        # Ctrl+U: clear line (Unix line kill - simpler than Ctrl+A + Ctrl+K)
        subprocess.run(
            ['tmux', 'send-keys', '-t', session, 'C-c'],
            capture_output=True, text=True, timeout=5
        )
        time.sleep(0.3)  # Let terminal process the cancel and return to prompt
        subprocess.run(
            ['tmux', 'send-keys', '-t', session, 'C-u'],
            capture_output=True, text=True, timeout=5
        )

        # Write text to a temp file (handles special chars and multi-line safely)
        with tempfile.NamedTemporaryFile(mode='w', suffix='.txt', delete=False) as f:
            f.write(text)
            tmp_path = f.name

        # Load into tmux paste buffer (separate from terminal kill ring)
        result = subprocess.run(
            ['tmux', 'load-buffer', tmp_path],
            capture_output=True, text=True, timeout=5
        )
        os.unlink(tmp_path)

        if result.returncode != 0:
            log(f"tmux load-buffer failed: {result.stderr}")
            return False

        # Paste buffer into the target session
        result = subprocess.run(
            ['tmux', 'paste-buffer', '-t', session],
            capture_output=True, text=True, timeout=5
        )

        if result.returncode != 0:
            log(f"tmux paste-buffer failed: {result.stderr}")
            return False

        # Optionally press Enter to submit
        if auto_submit:
            time.sleep(0.15)  # Let terminal process pasted text before submitting
            result = subprocess.run(
                ['tmux', 'send-keys', '-t', session, 'Enter'],
                capture_output=True, text=True, timeout=5
            )
            if result.returncode != 0:
                log(f"tmux send-keys Enter failed: {result.stderr}")
            # Note: Don't try to restore user text with Ctrl+Y after submitting,
            # as Claude will be processing and the restore would interfere

        log(f"Sent {len(text)} chars to tmux session '{session}' (auto_submit={auto_submit})")
        return True

    except Exception as e:
        log(f"Error sending to tmux: {e}")
        # Clean up temp file on error
        try:
            os.unlink(tmp_path)
        except:
            pass
        return False


class StatusHandler(BaseHTTPRequestHandler):
    def log_message(self, format, *args):
        pass  # Suppress logging

    def do_GET(self):
        try:
            # Normalize path and strip URL prefix if configured
            path = self.path.split('?')[0]  # Remove query string

            # Strip URL_PREFIX from path for static file serving
            # e.g., /enkai/ -> /, /enkai/something -> /something
            if URL_PREFIX and path.startswith(URL_PREFIX):
                path = path[len(URL_PREFIX):] or '/'

            # API endpoints (check these first)
            if path == '/status':
                self.send_response(200)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Access-Control-Allow-Origin', '*')
                self.end_headers()
                status = get_claude_status()
                self.wfile.write(json.dumps(status).encode())
                return

            if path == '/status/detailed':
                self.send_response(200)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Access-Control-Allow-Origin', '*')
                self.end_headers()
                status = get_detailed_status()
                self.wfile.write(json.dumps(status).encode())
                return

            if path == '/status/debug':
                self.send_response(200)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Access-Control-Allow-Origin', '*')
                self.end_headers()
                debug = get_debug_info()
                self.wfile.write(json.dumps(debug, indent=2).encode())
                return

            if path == '/health':
                health = get_health_status()
                # Return 200 even if degraded (for ALB health checks)
                # but include detailed status in response
                self.send_response(200)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Access-Control-Allow-Origin', '*')
                self.end_headers()
                self.wfile.write(json.dumps(health).encode())
                return

            if path == '/status/version':
                self.send_response(200)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Access-Control-Allow-Origin', '*')
                self.end_headers()
                version = get_version_status()
                self.wfile.write(json.dumps(version).encode())
                return

            if path == '/status/processes':
                self.send_response(200)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Access-Control-Allow-Origin', '*')
                self.end_headers()
                proc_stats = get_claude_processes()
                self.wfile.write(json.dumps(proc_stats, indent=2).encode())
                return

            # User endpoints for multi-user sessions
            if path == '/status/user':
                # Extract current user from headers and return info
                user_info = extract_user_from_headers(dict(self.headers))
                if user_info:
                    # Update active users tracking
                    update_active_user(user_info)
                self.send_response(200)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Access-Control-Allow-Origin', '*')
                self.end_headers()
                self.wfile.write(json.dumps({
                    'authenticated': user_info is not None,
                    'user': user_info,
                }).encode())
                return

            if path == '/status/users':
                # Return list of active users
                self.send_response(200)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Access-Control-Allow-Origin', '*')
                self.end_headers()
                users = get_active_users_list()
                self.wfile.write(json.dumps({
                    'count': len(users),
                    'users': users,
                }).encode())
                return

            # Claude state endpoint
            if path == '/status/claude-state':
                self.send_response(200)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Access-Control-Allow-Origin', '*')
                self.end_headers()
                state = get_claude_state()
                self.wfile.write(json.dumps(state).encode())
                return

            # Pnyx tick endpoints
            if path == '/status/tick':
                self.send_response(200)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Access-Control-Allow-Origin', '*')
                self.end_headers()
                status = get_pnyx_tick_status()
                status['claude_state'] = get_claude_state()
                self.wfile.write(json.dumps(status).encode())
                return

            # GitHub issue monitor status endpoint
            if path == '/status/gh-monitor':
                self.send_response(200)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Access-Control-Allow-Origin', '*')
                self.end_headers()
                status = get_gh_monitor_status()
                status['claude_state'] = get_claude_state()
                self.wfile.write(json.dumps(status).encode())
                return

            # Static file serving
            # Handle directory requests (serve index.html)
            if path == '/' or path == '':
                path = '/index.html'

            # Security: prevent directory traversal
            if '..' in path:
                self.send_response(403)
                self.end_headers()
                return

            # Build file path
            file_path = os.path.join(WEB_DIR, path.lstrip('/'))

            # If path is a directory, serve index.html
            if os.path.isdir(file_path):
                file_path = os.path.join(file_path, 'index.html')

            # Serve the static file
            serve_static_file(self, file_path)

        except Exception as e:
            log(f"Error handling request {self.path}: {e}")
            self.send_response(500)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Access-Control-Allow-Origin', '*')
            self.end_headers()
            self.wfile.write(json.dumps({'error': str(e)}).encode())

    def do_POST(self):
        """Handle POST requests for feedback and prompt sending."""
        try:
            path = self.path.split('?')[0]
            if URL_PREFIX and path.startswith(URL_PREFIX):
                path = path[len(URL_PREFIX):] or '/'

            if path == '/status/send-prompt':
                content_length = int(self.headers.get('Content-Length', 0))
                body = self.rfile.read(content_length).decode('utf-8')

                try:
                    data = json.loads(body)
                    text = data.get('text', '')
                    auto_submit = data.get('autoSubmit', False)
                    session = data.get('session', 'frank-claude')

                    if not text:
                        self.send_response(400)
                        self.send_header('Content-Type', 'application/json')
                        self.send_header('Access-Control-Allow-Origin', '*')
                        self.end_headers()
                        self.wfile.write(json.dumps({'error': 'No text provided'}).encode())
                        return

                    # Validate session name (only allow known sessions)
                    if session not in ('frank-claude', 'frank-bash'):
                        self.send_response(400)
                        self.send_header('Content-Type', 'application/json')
                        self.send_header('Access-Control-Allow-Origin', '*')
                        self.end_headers()
                        self.wfile.write(json.dumps({'error': 'Invalid session'}).encode())
                        return

                    success = send_to_tmux(text, session, auto_submit)

                    self.send_response(200 if success else 500)
                    self.send_header('Content-Type', 'application/json')
                    self.send_header('Access-Control-Allow-Origin', '*')
                    self.end_headers()
                    self.wfile.write(json.dumps({
                        'success': success,
                        'message': 'Prompt sent to terminal' if success else 'Failed to send prompt'
                    }).encode())
                except json.JSONDecodeError:
                    self.send_response(400)
                    self.send_header('Content-Type', 'application/json')
                    self.send_header('Access-Control-Allow-Origin', '*')
                    self.end_headers()
                    self.wfile.write(json.dumps({'error': 'Invalid JSON'}).encode())
                return

            elif path == '/status/tick/start':
                # Start Pnyx tick timer
                content_length = int(self.headers.get('Content-Length', 0))
                body = self.rfile.read(content_length).decode('utf-8') if content_length > 0 else '{}'

                try:
                    data = json.loads(body) if body else {}
                    interval = data.get('interval', 1800)  # Default 30 minutes

                    # Validate interval (minimum 60 seconds, maximum 4 hours)
                    interval = max(60, min(14400, int(interval)))

                    start_pnyx_tick_timer(interval)

                    self.send_response(200)
                    self.send_header('Content-Type', 'application/json')
                    self.send_header('Access-Control-Allow-Origin', '*')
                    self.end_headers()
                    self.wfile.write(json.dumps({
                        'success': True,
                        'message': f'Pnyx tick timer started ({interval}s interval)',
                        'status': get_pnyx_tick_status()
                    }).encode())
                except Exception as e:
                    self.send_response(500)
                    self.send_header('Content-Type', 'application/json')
                    self.send_header('Access-Control-Allow-Origin', '*')
                    self.end_headers()
                    self.wfile.write(json.dumps({'error': str(e)}).encode())
                return

            elif path == '/status/tick/stop':
                # Stop Pnyx tick timer
                stop_pnyx_tick_timer()

                self.send_response(200)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Access-Control-Allow-Origin', '*')
                self.end_headers()
                self.wfile.write(json.dumps({
                    'success': True,
                    'message': 'Pnyx tick timer stopped',
                    'status': get_pnyx_tick_status()
                }).encode())
                return

            elif path == '/status/tick/now':
                # Run Pnyx tick immediately
                def run_tick_background():
                    result = run_pnyx_tick()
                    with pnyx_tick_lock:
                        pnyx_tick_state['last_tick'] = datetime.now().isoformat()
                        pnyx_tick_state['last_result'] = result

                # Run in background thread to not block the response
                thread = threading.Thread(target=run_tick_background, daemon=True)
                thread.start()

                self.send_response(200)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Access-Control-Allow-Origin', '*')
                self.end_headers()
                self.wfile.write(json.dumps({
                    'success': True,
                    'message': 'Pnyx tick started (running in background)',
                }).encode())
                return

            elif path == '/status/gh-monitor/start':
                # Start GitHub issue monitor
                content_length = int(self.headers.get('Content-Length', 0))
                body = self.rfile.read(content_length).decode('utf-8') if content_length > 0 else '{}'

                try:
                    data = json.loads(body) if body else {}
                    interval = data.get('interval', 120)  # Default 2 minutes

                    # Validate interval (minimum 30 seconds, maximum 1 hour)
                    interval = max(30, min(3600, int(interval)))

                    start_gh_monitor(interval)

                    self.send_response(200)
                    self.send_header('Content-Type', 'application/json')
                    self.send_header('Access-Control-Allow-Origin', '*')
                    self.end_headers()
                    self.wfile.write(json.dumps({
                        'success': True,
                        'message': f'GH monitor started ({interval}s interval)',
                        'status': get_gh_monitor_status()
                    }).encode())
                except Exception as e:
                    self.send_response(500)
                    self.send_header('Content-Type', 'application/json')
                    self.send_header('Access-Control-Allow-Origin', '*')
                    self.end_headers()
                    self.wfile.write(json.dumps({'error': str(e)}).encode())
                return

            elif path == '/status/gh-monitor/stop':
                # Stop GitHub issue monitor
                stop_gh_monitor()

                self.send_response(200)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Access-Control-Allow-Origin', '*')
                self.end_headers()
                self.wfile.write(json.dumps({
                    'success': True,
                    'message': 'GH monitor stopped',
                    'status': get_gh_monitor_status()
                }).encode())
                return

            elif path == '/status/gh-monitor/now':
                # Check GitHub issues immediately
                def run_gh_check_background():
                    result = check_github_issues()
                    with gh_monitor_lock:
                        gh_monitor_state['last_check'] = datetime.now().isoformat()
                        gh_monitor_state['last_result'] = result

                thread = threading.Thread(target=run_gh_check_background, daemon=True)
                thread.start()

                self.send_response(200)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Access-Control-Allow-Origin', '*')
                self.end_headers()
                self.wfile.write(json.dumps({
                    'success': True,
                    'message': 'GH check started (running in background)',
                }).encode())
                return

            elif path == '/status/heartbeat':
                # Update user's last_seen timestamp
                content_length = int(self.headers.get('Content-Length', 0))
                body = self.rfile.read(content_length).decode('utf-8') if content_length > 0 else '{}'
                try:
                    data = json.loads(body) if body else {}
                except json.JSONDecodeError:
                    data = {}

                # Update prompt textbox state if provided
                if 'promptHasText' in data:
                    with prompt_textbox_lock:
                        prompt_textbox_state['has_text'] = bool(data['promptHasText'])
                        prompt_textbox_state['last_updated'] = time.time()

                user_info = extract_user_from_headers(dict(self.headers))
                if user_info:
                    update_active_user(user_info)
                    self.send_response(200)
                    self.send_header('Content-Type', 'application/json')
                    self.send_header('Access-Control-Allow-Origin', '*')
                    self.end_headers()
                    self.wfile.write(json.dumps({
                        'success': True,
                        'user': user_info,
                        'active_users': len(active_users),
                    }).encode())
                else:
                    self.send_response(200)
                    self.send_header('Content-Type', 'application/json')
                    self.send_header('Access-Control-Allow-Origin', '*')
                    self.end_headers()
                    self.wfile.write(json.dumps({
                        'success': False,
                        'message': 'No user authentication found',
                    }).encode())
                return

            elif path == '/status/upload':
                # Handle file upload (multipart/form-data)
                content_type = self.headers.get('Content-Type', '')
                content_length = int(self.headers.get('Content-Length', 0))

                # Size limit: 10MB
                if content_length > 10 * 1024 * 1024:
                    self.send_response(413)
                    self.send_header('Content-Type', 'application/json')
                    self.send_header('Access-Control-Allow-Origin', '*')
                    self.end_headers()
                    self.wfile.write(json.dumps({'error': 'File too large (max 10MB)'}).encode())
                    return

                if 'multipart/form-data' not in content_type:
                    self.send_response(400)
                    self.send_header('Content-Type', 'application/json')
                    self.send_header('Access-Control-Allow-Origin', '*')
                    self.end_headers()
                    self.wfile.write(json.dumps({'error': 'Expected multipart/form-data'}).encode())
                    return

                try:
                    # Parse multipart boundary
                    boundary = None
                    for part in content_type.split(';'):
                        part = part.strip()
                        if part.startswith('boundary='):
                            boundary = part[len('boundary='):].strip('"')
                            break

                    if not boundary:
                        self.send_response(400)
                        self.send_header('Content-Type', 'application/json')
                        self.send_header('Access-Control-Allow-Origin', '*')
                        self.end_headers()
                        self.wfile.write(json.dumps({'error': 'No boundary in Content-Type'}).encode())
                        return

                    # Read the entire body
                    body = self.rfile.read(content_length)
                    boundary_bytes = ('--' + boundary).encode()

                    # Parse multipart parts
                    file_data = None
                    file_name = None
                    auto_submit = False

                    parts = body.split(boundary_bytes)
                    for part in parts:
                        if not part or part == b'--\r\n' or part == b'--':
                            continue
                        # Split headers from body at first double CRLF
                        header_end = part.find(b'\r\n\r\n')
                        if header_end == -1:
                            continue
                        headers_raw = part[:header_end].decode('utf-8', errors='replace')
                        part_body = part[header_end + 4:]
                        # Remove trailing \r\n
                        if part_body.endswith(b'\r\n'):
                            part_body = part_body[:-2]

                        # Parse Content-Disposition
                        field_name = None
                        filename = None
                        for hdr_line in headers_raw.split('\r\n'):
                            if hdr_line.lower().startswith('content-disposition:'):
                                for param in hdr_line.split(';'):
                                    param = param.strip()
                                    if param.startswith('name='):
                                        field_name = param[5:].strip('"')
                                    elif param.startswith('filename='):
                                        filename = param[9:].strip('"')

                        if field_name == 'file' and filename:
                            file_data = part_body
                            file_name = filename
                        elif field_name == 'autoSubmit':
                            auto_submit = part_body.decode('utf-8', errors='replace').strip().lower() == 'true'

                    if not file_data or not file_name:
                        self.send_response(400)
                        self.send_header('Content-Type', 'application/json')
                        self.send_header('Access-Control-Allow-Origin', '*')
                        self.end_headers()
                        self.wfile.write(json.dumps({'error': 'No file uploaded'}).encode())
                        return

                    # Validate file extension
                    allowed_exts = {
                        # Images
                        '.png', '.jpg', '.jpeg', '.gif', '.webp', '.svg', '.bmp', '.ico',
                        # Text & docs
                        '.txt', '.md', '.csv', '.tsv', '.log', '.pdf', '.rst', '.org',
                        # Code
                        '.js', '.ts', '.jsx', '.tsx', '.py', '.rb', '.go', '.rs', '.java',
                        '.c', '.cpp', '.h', '.hpp', '.cs', '.php', '.swift', '.kt', '.scala',
                        '.sh', '.bash', '.zsh', '.fish', '.ps1', '.bat', '.cmd',
                        '.r', '.m', '.lua', '.pl', '.ex', '.exs', '.erl', '.hs', '.clj',
                        # Config & data
                        '.json', '.yaml', '.yml', '.toml', '.ini', '.cfg', '.conf',
                        '.xml', '.env', '.properties', '.tf', '.hcl',
                        # Web
                        '.html', '.htm', '.css', '.scss', '.sass', '.less',
                        # Data & query
                        '.sql', '.graphql', '.gql', '.proto',
                        # Other dev files
                        '.diff', '.patch', '.lock', '.dockerfile',
                        '.gitignore', '.editorconfig', '.prettierrc', '.eslintrc',
                    }
                    ext = os.path.splitext(file_name)[1].lower()
                    if ext not in allowed_exts:
                        self.send_response(400)
                        self.send_header('Content-Type', 'application/json')
                        self.send_header('Access-Control-Allow-Origin', '*')
                        self.end_headers()
                        self.wfile.write(json.dumps({
                            'error': f'File type not allowed: {ext}. Allowed: {", ".join(sorted(allowed_exts))}'
                        }).encode())
                        return

                    # Sanitize filename: keep only alphanumeric, dots, dashes, underscores
                    base_name = os.path.splitext(file_name)[0]
                    safe_base = re.sub(r'[^a-zA-Z0-9._-]', '_', base_name)
                    if not safe_base:
                        safe_base = 'upload'
                    safe_filename = safe_base + ext

                    # Create upload directory
                    upload_dir = '/workspace/uploads'
                    os.makedirs(upload_dir, exist_ok=True)

                    # Handle duplicate filenames
                    save_path = os.path.join(upload_dir, safe_filename)
                    if os.path.exists(save_path):
                        counter = 1
                        while os.path.exists(save_path):
                            save_path = os.path.join(upload_dir, f"{safe_base}_{counter}{ext}")
                            counter += 1
                        safe_filename = os.path.basename(save_path)

                    # Save file
                    with open(save_path, 'wb') as f:
                        f.write(file_data)

                    log(f"Uploaded file saved: {save_path} ({len(file_data)} bytes)")

                    # Send file path to Claude terminal
                    prompt_text = save_path
                    send_to_tmux(prompt_text, 'frank-claude', auto_submit)

                    self.send_response(200)
                    self.send_header('Content-Type', 'application/json')
                    self.send_header('Access-Control-Allow-Origin', '*')
                    self.end_headers()
                    self.wfile.write(json.dumps({
                        'success': True,
                        'path': save_path,
                        'filename': safe_filename,
                    }).encode())

                except Exception as e:
                    log(f"Upload error: {e}")
                    self.send_response(500)
                    self.send_header('Content-Type', 'application/json')
                    self.send_header('Access-Control-Allow-Origin', '*')
                    self.end_headers()
                    self.wfile.write(json.dumps({'error': str(e)}).encode())
                return

            elif path == '/status/feedback':
                # Read request body
                content_length = int(self.headers.get('Content-Length', 0))
                body = self.rfile.read(content_length).decode('utf-8')

                try:
                    data = json.loads(body)
                    rating = data.get('rating', 'neutral')
                    prompt_id = data.get('prompt_id')

                    # Validate rating
                    if rating not in ('positive', 'negative', 'neutral'):
                        rating = 'neutral'

                    # Save feedback
                    success = save_feedback(prompt_id, rating)

                    self.send_response(200)
                    self.send_header('Content-Type', 'application/json')
                    self.send_header('Access-Control-Allow-Origin', '*')
                    self.end_headers()
                    self.wfile.write(json.dumps({
                        'success': success,
                        'message': 'Feedback recorded' if success else 'Analytics not enabled'
                    }).encode())
                except json.JSONDecodeError:
                    self.send_response(400)
                    self.send_header('Content-Type', 'application/json')
                    self.send_header('Access-Control-Allow-Origin', '*')
                    self.end_headers()
                    self.wfile.write(json.dumps({'error': 'Invalid JSON'}).encode())
            else:
                self.send_response(404)
                self.send_header('Access-Control-Allow-Origin', '*')
                self.end_headers()
        except Exception as e:
            log(f"Error handling POST request {self.path}: {e}")
            self.send_response(500)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Access-Control-Allow-Origin', '*')
            self.end_headers()
            self.wfile.write(json.dumps({'error': str(e)}).encode())

    def do_OPTIONS(self):
        self.send_response(200)
        self.send_header('Access-Control-Allow-Origin', '*')
        self.send_header('Access-Control-Allow-Methods', 'GET, POST, OPTIONS')
        self.send_header('Access-Control-Allow-Headers', 'Content-Type')
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


def get_process_stats(pid):
    """Get detailed stats for a process from /proc filesystem."""
    stats = {
        'pid': pid,
        'name': None,
        'state': None,
        'cpu_percent': 0,
        'memory_mb': 0,
        'memory_percent': 0,
        'threads': 0,
        'disk_read_mb': 0,
        'disk_write_mb': 0,
        'cmdline': None,
    }

    try:
        # Get process name and state from /proc/<pid>/stat
        with open(f'/proc/{pid}/stat') as f:
            stat_line = f.read()
            # Format: pid (comm) state ppid ...
            # comm can contain spaces and parentheses, so find the last )
            comm_start = stat_line.index('(')
            comm_end = stat_line.rindex(')')
            stats['name'] = stat_line[comm_start + 1:comm_end]
            fields = stat_line[comm_end + 2:].split()
            stats['state'] = fields[0]  # State is first field after comm
            stats['threads'] = int(fields[17]) if len(fields) > 17 else 1

            # CPU time: utime (field 13) + stime (field 14) in clock ticks
            utime = int(fields[11]) if len(fields) > 11 else 0
            stime = int(fields[12]) if len(fields) > 12 else 0
            # Store raw ticks for later CPU calculation
            stats['_cpu_ticks'] = utime + stime
    except Exception as e:
        log(f"Error reading /proc/{pid}/stat: {e}")

    try:
        # Get memory info from /proc/<pid>/status
        with open(f'/proc/{pid}/status') as f:
            for line in f:
                if line.startswith('VmRSS:'):
                    # VmRSS is in kB
                    kb = int(line.split()[1])
                    stats['memory_mb'] = round(kb / 1024, 1)
                    break
    except Exception as e:
        log(f"Error reading /proc/{pid}/status: {e}")

    try:
        # Get disk I/O from /proc/<pid>/io
        with open(f'/proc/{pid}/io') as f:
            for line in f:
                if line.startswith('read_bytes:'):
                    stats['disk_read_mb'] = round(int(line.split()[1]) / (1024 * 1024), 2)
                elif line.startswith('write_bytes:'):
                    stats['disk_write_mb'] = round(int(line.split()[1]) / (1024 * 1024), 2)
    except Exception as e:
        # io file may not be accessible
        pass

    try:
        # Get command line
        with open(f'/proc/{pid}/cmdline') as f:
            cmdline = f.read().replace('\x00', ' ').strip()
            # Truncate long command lines
            stats['cmdline'] = cmdline[:200] if len(cmdline) > 200 else cmdline
    except:
        pass

    # Calculate memory percent
    try:
        with open('/proc/meminfo') as f:
            for line in f:
                if line.startswith('MemTotal:'):
                    total_kb = int(line.split()[1])
                    stats['memory_percent'] = round((stats['memory_mb'] * 1024 / total_kb) * 100, 1)
                    break
    except:
        pass

    return stats


def get_container_network_stats():
    """Get network stats for the container from /proc/net/dev."""
    network = {
        'rx_mb': 0,
        'tx_mb': 0,
        'rx_packets': 0,
        'tx_packets': 0,
    }

    try:
        with open('/proc/net/dev') as f:
            for line in f:
                line = line.strip()
                # Skip header lines
                if ':' not in line:
                    continue
                # Parse interface: rx_bytes rx_packets ... tx_bytes tx_packets ...
                iface, data = line.split(':')
                iface = iface.strip()
                # Skip loopback
                if iface == 'lo':
                    continue
                fields = data.split()
                if len(fields) >= 10:
                    network['rx_mb'] += int(fields[0]) / (1024 * 1024)
                    network['rx_packets'] += int(fields[1])
                    network['tx_mb'] += int(fields[8]) / (1024 * 1024)
                    network['tx_packets'] += int(fields[9])
    except Exception as e:
        log(f"Error reading /proc/net/dev: {e}")

    network['rx_mb'] = round(network['rx_mb'], 2)
    network['tx_mb'] = round(network['tx_mb'], 2)

    return network


def get_claude_processes():
    """Find all Claude-related processes and their stats."""
    import subprocess

    processes = []
    process_map = {}  # pid -> stats

    try:
        # Find all Claude-related processes
        # This includes: claude, node (for claude), python (for MCP servers), etc.
        result = subprocess.run(
            ['ps', 'aux'],
            capture_output=True,
            text=True
        )

        if result.returncode == 0:
            lines = result.stdout.strip().split('\n')
            for line in lines[1:]:  # Skip header
                fields = line.split(None, 10)
                if len(fields) < 11:
                    continue

                pid = fields[1]
                cpu = fields[2]
                mem = fields[3]
                command = fields[10]

                # Filter for Claude-related processes
                is_claude = any(keyword in command.lower() for keyword in [
                    'claude',
                    '/usr/local/bin/claude',
                    'node.*claude',
                    'mcp',
                    'sequential-thinking',
                    'context7',
                    'serena',
                    'playwright',
                ])

                if is_claude:
                    try:
                        stats = get_process_stats(int(pid))
                        stats['cpu_percent'] = float(cpu)
                        stats['memory_percent'] = float(mem)
                        processes.append(stats)
                    except:
                        pass

    except Exception as e:
        log(f"Error getting claude processes: {e}")

    # Also get total container stats
    container_stats = {
        'total_memory_mb': 0,
        'total_cpu_percent': 0,
        'process_count': len(processes),
    }

    for p in processes:
        container_stats['total_memory_mb'] += p.get('memory_mb', 0)
        container_stats['total_cpu_percent'] += p.get('cpu_percent', 0)

    container_stats['total_memory_mb'] = round(container_stats['total_memory_mb'], 1)
    container_stats['total_cpu_percent'] = round(container_stats['total_cpu_percent'], 1)

    # Get network stats (container-level)
    network = get_container_network_stats()

    return {
        'processes': processes,
        'container': container_stats,
        'network': network,
        'timestamp': datetime.now().isoformat(),
    }

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


# =========================================================================
# Analytics Functions
# =========================================================================

def filter_pii(text):
    """Remove potential PII from text."""
    if not text:
        return text

    # Email addresses
    text = re.sub(r'[a-zA-Z0-9_.+-]+@[a-zA-Z0-9-]+\.[a-zA-Z0-9-.]+', '[EMAIL]', text)

    # AWS account IDs (12 digits)
    text = re.sub(r'\b\d{12}\b', '[AWS_ACCOUNT]', text)

    # API keys and tokens (long alphanumeric strings)
    text = re.sub(r'(?:api[_-]?key|token|secret|password|credential)["\s:=]+["\']?([a-zA-Z0-9_-]{20,})["\']?',
                  r'[REDACTED_KEY]', text, flags=re.IGNORECASE)

    # Generic long secrets
    text = re.sub(r'[a-zA-Z0-9]{40,}', '[REDACTED]', text)

    # File paths with usernames (Unix)
    text = re.sub(r'/Users/[^/\s]+/', '/Users/[USER]/', text)
    text = re.sub(r'/home/[^/\s]+/', '/home/[USER]/', text)

    # Windows paths with usernames
    text = re.sub(r'C:\\Users\\[^\\]+\\', 'C:\\Users\\[USER]\\', text)

    return text


def extract_user_prompts(filepath):
    """Extract user prompts from a JSONL conversation file."""
    global processed_prompt_ids
    prompts = []
    turn_number = 0

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

                    # Check if this is a user message
                    if entry_type in ('user', 'human') or role in ('user', 'human'):
                        turn_number += 1

                        # Generate a unique ID for this prompt
                        prompt_text = extract_text_content(entry)
                        prompt_id = f"{filepath.stem}_{turn_number}"

                        # Skip if already processed
                        if prompt_id in processed_prompt_ids:
                            continue

                        processed_prompt_ids.add(prompt_id)

                        # Filter PII
                        filtered_text = filter_pii(prompt_text)

                        prompt_record = {
                            'id': str(uuid.uuid4()),
                            'profile': CONTAINER_NAME,
                            'session_id': current_session_id,
                            'timestamp': entry.get('timestamp', datetime.now().isoformat()),
                            'prompt': {
                                'text': filtered_text,
                                'tokens': len(filtered_text.split()),  # Rough estimate
                            },
                            'context': {
                                'turn_number': turn_number,
                                'model': None,  # Will be filled later
                                'files_referenced': [],
                            },
                            'outcome': {
                                'next_turn_count': 0,
                                'tools_used': [],
                                'total_output_tokens': 0,
                            },
                        }
                        prompts.append(prompt_record)

                except json.JSONDecodeError:
                    continue
    except Exception as e:
        log(f"Error extracting prompts: {e}")

    return prompts


def extract_text_content(entry):
    """Extract text content from various message formats."""
    # Direct text field
    if 'text' in entry:
        return entry['text']

    # Content field (string)
    if 'content' in entry:
        content = entry['content']
        if isinstance(content, str):
            return content
        # Content array
        if isinstance(content, list):
            texts = []
            for block in content:
                if isinstance(block, str):
                    texts.append(block)
                elif isinstance(block, dict):
                    if block.get('type') == 'text':
                        texts.append(block.get('text', ''))
            return ' '.join(texts)

    # Message.content
    if 'message' in entry and isinstance(entry['message'], dict):
        return extract_text_content(entry['message'])

    return ''


def buffer_prompt(prompt_record):
    """Add a prompt to the buffer and trigger upload if needed."""
    global last_upload_time

    with prompt_buffer_lock:
        prompt_buffer.append(prompt_record)

        # Check if we should upload
        should_upload = (
            len(prompt_buffer) >= MAX_BUFFER_SIZE or
            time.time() - last_upload_time > UPLOAD_INTERVAL
        )

        if should_upload:
            upload_buffered_prompts()


def upload_buffered_prompts():
    """Upload buffered prompts to S3."""
    global last_upload_time

    if not ANALYTICS_ENABLED or not ANALYTICS_BUCKET or not HAS_BOTO3:
        return

    with prompt_buffer_lock:
        if not prompt_buffer:
            return

        prompts_to_upload = list(prompt_buffer)
        prompt_buffer.clear()
        last_upload_time = time.time()

    # Upload in background
    def do_upload():
        try:
            s3 = boto3.client('s3', region_name=AWS_REGION)

            now = datetime.now()
            profile = CONTAINER_NAME or 'unknown'

            # S3 key: prompts/{profile}/{year}/{month}/{day}/{session}_{timestamp}.json
            key = f"prompts/{profile}/{now.year}/{now.month:02d}/{now.day:02d}/{current_session_id}_{int(now.timestamp())}.json"

            body = json.dumps(prompts_to_upload, indent=2)

            s3.put_object(
                Bucket=ANALYTICS_BUCKET,
                Key=key,
                Body=body,
                ContentType='application/json'
            )

            log(f"Uploaded {len(prompts_to_upload)} prompts to s3://{ANALYTICS_BUCKET}/{key}")
        except Exception as e:
            log(f"Error uploading prompts to S3: {e}")

    threading.Thread(target=do_upload, daemon=True).start()


def save_feedback(prompt_id, rating):
    """Save feedback to S3."""
    global last_prompt_id

    if not ANALYTICS_ENABLED or not ANALYTICS_BUCKET or not HAS_BOTO3:
        return False

    try:
        s3 = boto3.client('s3', region_name=AWS_REGION)

        now = datetime.now()
        profile = CONTAINER_NAME or 'unknown'

        feedback_record = {
            'prompt_id': prompt_id or last_prompt_id or 'unknown',
            'profile': profile,
            'timestamp': now.isoformat(),
            'rating': rating,
            'context': {
                'session_id': current_session_id,
            }
        }

        # S3 key: feedback/{profile}/{year}/{month}/{day}/feedback_{timestamp}.json
        key = f"feedback/{profile}/{now.year}/{now.month:02d}/{now.day:02d}/feedback_{int(now.timestamp())}.json"

        s3.put_object(
            Bucket=ANALYTICS_BUCKET,
            Key=key,
            Body=json.dumps(feedback_record, indent=2),
            ContentType='application/json'
        )

        log(f"Saved feedback to s3://{ANALYTICS_BUCKET}/{key}")
        return True
    except Exception as e:
        log(f"Error saving feedback to S3: {e}")
        return False


def scan_and_capture_prompts():
    """Background task to periodically scan for new prompts."""
    while True:
        try:
            if ANALYTICS_ENABLED:
                home = Path.home()
                claude_dir = home / '.claude'

                conversation_files = find_jsonl_files(claude_dir)
                for filepath in conversation_files:
                    prompts = extract_user_prompts(filepath)
                    for prompt in prompts:
                        buffer_prompt(prompt)

                # Force upload if there's anything in the buffer
                if prompt_buffer:
                    upload_buffered_prompts()
        except Exception as e:
            log(f"Error in prompt capture: {e}")

        # Scan every 30 seconds
        time.sleep(30)


class HealthOnlyHandler(BaseHTTPRequestHandler):
    """Simple health check handler for the dedicated health port."""
    def log_message(self, format, *args):
        pass  # Suppress logging

    def do_GET(self):
        if self.path == '/health' or self.path == '/':
            health = get_health_status()
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Access-Control-Allow-Origin', '*')
            self.end_headers()
            self.wfile.write(json.dumps(health).encode())
        else:
            self.send_response(404)
            self.end_headers()


def run_health_server():
    """Run a dedicated health check server on STATUS_PORT (7683)."""
    try:
        health_server = HTTPServer(('0.0.0.0', STATUS_PORT), HealthOnlyHandler)
        log(f"Health server started on port {STATUS_PORT}")
        health_server.serve_forever()
    except Exception as e:
        log(f"Error starting health server on port {STATUS_PORT}: {e}")


def main():
    log(f"Starting web+status server on port {WEB_PORT}")
    log(f"Starting health server on port {STATUS_PORT}")
    log(f"Home directory: {Path.home()}")
    log(f"User: {os.environ.get('USER', 'unknown')}")
    log(f"Claude dir check: {Path.home() / '.claude'} exists: {(Path.home() / '.claude').exists()}")

    # Log static file serving configuration
    log(f"Web directory: {WEB_DIR}")
    log(f"URL prefix: '{URL_PREFIX}'")

    # Log analytics configuration
    log(f"Analytics enabled: {ANALYTICS_ENABLED}")
    log(f"Analytics bucket: {ANALYTICS_BUCKET}")
    log(f"Container name: {CONTAINER_NAME}")
    log(f"Session ID: {current_session_id}")
    log(f"Boto3 available: {HAS_BOTO3}")

    # List claude dir contents at startup
    claude_dir = Path.home() / '.claude'
    if claude_dir.exists():
        log(f"Initial .claude contents: {list(claude_dir.iterdir())}")
        projects_dir = claude_dir / 'projects'
        if projects_dir.exists():
            log(f"Projects dir contents: {list(projects_dir.iterdir())[:10]}")

    # Start prompt capture background thread if analytics is enabled
    if ANALYTICS_ENABLED and HAS_BOTO3:
        log("Starting prompt capture background thread...")
        capture_thread = threading.Thread(target=scan_and_capture_prompts, daemon=True)
        capture_thread.start()
        log("Prompt capture thread started")
    else:
        if not ANALYTICS_ENABLED:
            log("Analytics is disabled (ANALYTICS_ENABLED != 'true')")
        if not HAS_BOTO3:
            log("boto3 not available - S3 uploads disabled")

    # Start dedicated health server on STATUS_PORT (7683) for ECS health checks
    health_thread = threading.Thread(target=run_health_server, daemon=True)
    health_thread.start()

    # Main web+status server on WEB_PORT (7680)
    server = HTTPServer(('0.0.0.0', WEB_PORT), StatusHandler)
    print(f"Web+status server running on port {WEB_PORT}")
    print(f"Health server running on port {STATUS_PORT}")
    log(f"Server started successfully on ports {WEB_PORT} and {STATUS_PORT}")
    server.serve_forever()

if __name__ == '__main__':
    main()

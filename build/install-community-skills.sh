#!/bin/bash
# Install Community Skills
# Downloads skills and scripts from community GitHub repos at container startup.
# Reads repos from /usr/local/share/frank/community-skills.conf

set -euo pipefail

CONF_FILE="/usr/local/share/frank/community-skills.conf"
CACHE_DIR="/opt/community-skills"
SKILLS_DIR="$HOME/.claude/skills"
SCRIPTS_DIR="$HOME/.claude/scripts"

sync_community_skills() {
    if [ ! -f "$CONF_FILE" ]; then
        echo "No community skills config found at $CONF_FILE"
        return 0
    fi

    echo "=== Syncing Community Skills ==="
    mkdir -p "$CACHE_DIR" "$SKILLS_DIR" "$SCRIPTS_DIR"

    local count=0
    local skill_count=0
    local script_count=0

    while IFS= read -r line || [ -n "$line" ]; do
        # Skip comments and blank lines
        line=$(echo "$line" | sed 's/#.*//' | xargs)
        [ -z "$line" ] && continue

        # Parse: REPO [BRANCH]
        local repo branch repo_name cache_path
        repo=$(echo "$line" | awk '{print $1}')
        branch=$(echo "$line" | awk '{print $2}')
        branch="${branch:-main}"
        repo_name=$(echo "$repo" | tr '/' '-')
        cache_path="$CACHE_DIR/$repo_name"

        echo "  Syncing $repo ($branch)..."

        # Clone or update
        if [ -d "$cache_path/.git" ]; then
            git -C "$cache_path" fetch --depth 1 origin "$branch" 2>/dev/null || true
            git -C "$cache_path" checkout FETCH_HEAD 2>/dev/null || true
        else
            rm -rf "$cache_path"
            git clone --depth 1 --branch "$branch" "https://github.com/$repo.git" "$cache_path" 2>/dev/null || {
                echo "    WARNING: Failed to clone $repo"
                continue
            }
        fi

        local sha
        sha=$(git -C "$cache_path" rev-parse --short=8 HEAD 2>/dev/null || echo "unknown")
        echo "    Version: $sha"

        # Copy skills
        if [ -d "$cache_path/.claude/skills" ]; then
            local skills_found
            skills_found=$(ls -d "$cache_path/.claude/skills"/*/ 2>/dev/null | wc -l)
            if [ "$skills_found" -gt 0 ]; then
                cp -r "$cache_path/.claude/skills"/* "$SKILLS_DIR/" 2>/dev/null || true
                skill_count=$((skill_count + skills_found))
                echo "    Installed $skills_found skill(s)"
            fi
        fi

        # Copy scripts
        if [ -d "$cache_path/.claude/scripts" ]; then
            local scripts_found
            scripts_found=$(ls "$cache_path/.claude/scripts"/* 2>/dev/null | wc -l)
            if [ "$scripts_found" -gt 0 ]; then
                cp -r "$cache_path/.claude/scripts"/* "$SCRIPTS_DIR/" 2>/dev/null || true
                chmod +x "$SCRIPTS_DIR"/* 2>/dev/null || true
                script_count=$((script_count + scripts_found))
                echo "    Installed $scripts_found script(s)"
            fi
        fi

        count=$((count + 1))
    done < "$CONF_FILE"

    echo "  Community skills sync complete: $count repo(s), $skill_count skill(s), $script_count script(s)"
}

# Copy community skills to a working directory (called after worktree setup)
copy_community_skills_to_workdir() {
    local work_dir="$1"
    [ -z "$work_dir" ] && return 0

    # Copy skills
    if [ -d "$SKILLS_DIR" ] && [ "$(ls -A "$SKILLS_DIR" 2>/dev/null)" ]; then
        mkdir -p "$work_dir/.claude/skills"
        cp -rn "$SKILLS_DIR"/* "$work_dir/.claude/skills/" 2>/dev/null || true
    fi

    # Copy scripts
    if [ -d "$SCRIPTS_DIR" ] && [ "$(ls -A "$SCRIPTS_DIR" 2>/dev/null)" ]; then
        mkdir -p "$work_dir/.claude/scripts"
        cp -r "$SCRIPTS_DIR"/* "$work_dir/.claude/scripts/" 2>/dev/null || true
        chmod +x "$work_dir/.claude/scripts"/* 2>/dev/null || true
    fi
}

# Run sync if called directly
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
    sync_community_skills
fi

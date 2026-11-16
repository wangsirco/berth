#!/bin/sh
# Berth "Claude" terminal: the last conversation in this folder (or a new one), then a shell.
d="$HOME/.claude/projects/$(pwd | sed 's#[^A-Za-z0-9]#-#g')"
if ! command -v claude >/dev/null 2>&1; then
	echo "Claude Code is not installed in this workspace yet (turn it on in the workspace settings)."
elif ls "$d"/*.jsonl >/dev/null 2>&1; then
	claude --continue
else
	claude
fi
command -v bash >/dev/null 2>&1 && exec bash -l || exec sh -l

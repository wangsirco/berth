#!/bin/sh
# A stand-in for Claude Code in e2e (no account is used): speaks the same stream-json
# as `claude -p`, edits files like a task would, asks a question, or works slowly.
resume="" prev=""
for a in "$@"; do [ "$prev" = "--resume" ] && resume="$a"; prev="$a"; done
case " $* " in *" -p "*) ;; *) echo "fake claude (interactive) in $(pwd)"; exit 0 ;; esac
p=$(cat)
sid=${resume:-s-$$}
tok=no
[ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}" ] && tok=yes
echo "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"$sid\"}"
echo "{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"working token=$tok\"},{\"type\":\"tool_use\",\"name\":\"Write\",\"input\":{\"file_path\":\"hello.txt\"}}]}}"
case "$p" in
*SLOW*) sleep 120; res="slow done" ;;
*ASK*) res="Which color should I use?" ;;
*)
	if [ -n "$resume" ]; then
		echo "$p" >color.txt
		res="Used $p."
	else
		echo "hello from claude" >hello.txt
		echo "- $p" >>notes.md
		res="Wrote hello.txt."
	fi
	;;
esac
echo "{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"$res\",\"total_cost_usd\":0.01,\"num_turns\":2,\"duration_ms\":1200,\"session_id\":\"$sid\"}"

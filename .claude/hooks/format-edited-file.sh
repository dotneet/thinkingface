#!/usr/bin/env bash
#
# PostToolUse hook: format the file Claude Code just wrote.
#
# Reads the hook payload on stdin and formats the edited file with gofmt
# (backend Go) or Biome (frontend TypeScript). Always exits 0 -- a formatter
# that is missing, or a file that does not parse yet, must never fail the
# edit that triggered it.
#
# Safety notes:
#   - the path from the payload is only ever passed as a *quoted argument*,
#     never re-evaluated, so a filename containing spaces, quotes, newlines,
#     `;` or `$(...)` is data and cannot execute anything;
#   - `--` terminates option parsing, so a path starting with `-` is not read
#     as a flag;
#   - the path must resolve inside $CLAUDE_PROJECT_DIR, so editing
#     /somewhere/else/backend/x.go does not get rewritten by this repo's hook.
#
# Manual test:
#   echo '{"tool_input":{"file_path":"/abs/path/backend/x.go"}}' \
#     | CLAUDE_PROJECT_DIR=/path/to/thinkingface .claude/hooks/format-edited-file.sh

set -u

command -v jq >/dev/null 2>&1 || exit 0

file="$(jq -r '.tool_input.file_path // empty' 2>/dev/null)" || exit 0
[ -n "$file" ] || exit 0

project="${CLAUDE_PROJECT_DIR:-$PWD}"
# Normalise a relative path against the project root before the containment
# check below, since the hook's working directory is not guaranteed.
case "$file" in
	/*) ;;
	*) file="$project/$file" ;;
esac

# Reject `..` outright rather than trying to resolve it: the case patterns
# below use `*`, which happily spans `/`, so `<project>/backend/../../x.go`
# would otherwise pass the containment check.
case "$file" in
	*/../* | */..) exit 0 ;;
esac

# Only touch files inside this repository. The quoted "$project" is a literal
# in the case pattern, so metacharacters in the path do not turn into globs.
case "$file" in
	"$project"/*) ;;
	*) exit 0 ;;
esac

[ -f "$file" ] || exit 0

case "$file" in
	"$project"/backend/*.go)
		if command -v gofmt >/dev/null 2>&1; then
			gofmt -w -- "$file" >/dev/null 2>&1 || true
		fi
		;;
	"$project"/frontend/*.ts | "$project"/frontend/*.tsx)
		if [ -x "$project/frontend/node_modules/.bin/biome" ]; then
			(cd "$project/frontend" && ./node_modules/.bin/biome format --write "$file" >/dev/null 2>&1) || true
		elif command -v bunx >/dev/null 2>&1; then
			(cd "$project/frontend" && bunx --bun @biomejs/biome format --write "$file" >/dev/null 2>&1) || true
		fi
		;;
esac

exit 0

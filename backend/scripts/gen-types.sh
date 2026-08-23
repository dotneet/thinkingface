#!/usr/bin/env bash
#
# Regenerate frontend/types/api.gen.ts from the Go wire structs in
# backend/internal/apitypes.
#
# The output is raw tygo output on purpose: frontend/biome.json excludes
# `types/*.gen.ts` from formatting and linting, so there is no formatter to
# agree with. tygo's output is deterministic, which keeps
# `make gen-types` + `git status --porcelain` an accurate sync check.
#
# The destination lives in backend/tygo.yaml (`output_path`) and is read from
# there, so this script and the generator config cannot drift apart.
#
# Usage: backend/scripts/gen-types.sh
set -euo pipefail

backend_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
config="$backend_dir/tygo.yaml"

# The destination is read out of tygo.yaml rather than hard-coded here, so the
# two can never drift: a changed output_path that this script did not know
# about would otherwise leave api.gen.ts frozen at its committed contents while
# the sync check stayed green.
output_rel="$(sed -n 's/^[[:space:]]*output_path:[[:space:]]*"\{0,1\}\([^"]*\)"\{0,1\}[[:space:]]*$/\1/p' "$config")"
if [ -z "$output_rel" ]; then
	echo "gen-types: could not read output_path from $config" >&2
	exit 1
fi
if [ "$(printf '%s\n' "$output_rel" | wc -l)" -ne 1 ]; then
	echo "gen-types: $config has multiple output_path entries; this script assumes a single-package setup" >&2
	exit 1
fi
# output_path in tygo.yaml is relative to the directory tygo runs in (backend/).
case "$output_rel" in
	/*) output="$output_rel" ;;
	*) output="$backend_dir/$output_rel" ;;
esac

if ! command -v go >/dev/null 2>&1; then
	echo "gen-types: go not found (https://go.dev/dl/)" >&2
	exit 1
fi

cd "$backend_dir"

# `go tool tygo` writes the whole file in one go and leaves the previous
# contents alone when it fails, so a failed run cannot leave a truncated
# api.gen.ts behind. `set -e` then stops here rather than letting a caller
# treat a stale file as freshly generated.
go tool tygo generate

if [ ! -s "$output" ]; then
	echo "gen-types: tygo succeeded but $output was not generated" >&2
	exit 1
fi

echo "gen-types: wrote $(cd "$(dirname "$output")" && pwd)/$(basename "$output")"

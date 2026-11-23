#!/bin/sh
# Checks commit subjects against Conventional Commits (https://www.conventionalcommits.org):
#
#   <type>[(<scope>)][!]: <description>
#
# The description starts lowercase and has no trailing period. Merge commits are not checked.
#
# Usage:
#   check-commits.sh --file <path>   check one message file (used by the commit-msg hook)
#   check-commits.sh <rev-range>     check every non-merge commit in a range (used by CI)
set -eu

TYPES='feat|fix|docs|style|refactor|perf|test|build|ci|chore|revert'
PATTERN="^($TYPES)(\([a-z0-9._/-]+\))?!?: [a-z0-9]"
MAX_LEN=72

# check <subject> <label>: prints an error and returns 1 if the subject is invalid.
check() {
	case $1 in
	'fixup! '* | 'squash! '* | 'amend! '*) return 0 ;; # autosquash markers, squashed before merge
	'Merge '*) return 0 ;;                               # git's merge message; CI skips merges too
	esac
	if ! printf '%s\n' "$1" | grep -Eq "$PATTERN"; then
		echo "error: $2: \"$1\"" >&2
		echo "  expected '<type>(<scope>): <description>' with type one of: $TYPES" >&2
		echo "  the description starts lowercase; for a revert write 'revert: <description>'" >&2
		return 1
	fi
	case $1 in
	*.)
		echo "error: $2: subject ends with a period: \"$1\"" >&2
		return 1
		;;
	esac
	if [ "${#1}" -gt "$MAX_LEN" ]; then
		echo "error: $2: subject is ${#1} characters, max is $MAX_LEN: \"$1\"" >&2
		return 1
	fi
}

if [ "${1:-}" = --file ]; then
	# First line that is neither empty nor a git comment.
	subject=$(grep -v -e '^#' -e '^[[:space:]]*$' "$2" | head -n 1 || true)
	check "$subject" "commit message"
	exit
fi

range=${1:?usage: check-commits.sh --file <path> | <rev-range>}
commits=$(git log --no-merges --format='%h %s' "$range")
[ -n "$commits" ] || exit 0

status=0
while read -r sha subject; do
	check "$subject" "$sha" || status=1
done <<EOF
$commits
EOF
exit "$status"

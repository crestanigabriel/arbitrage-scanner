#!/bin/sh
# Usage: with-env.sh FILE COMMAND [ARG...]
#
# Runs COMMAND with the KEY=VALUE lines of FILE added to its environment. A variable that is
# already set keeps its value (the usual dotenv rule), so FILE holds the defaults and a one-off
# `KEY=value make ...` overrides them. Blank lines and lines starting with "#" are skipped.
# Values are taken literally: no quotes, no variable expansion.
set -eu

if [ $# -lt 2 ]; then
	echo "usage: $0 FILE COMMAND [ARG...]" >&2
	exit 2
fi
file=$1
shift
if [ ! -r "$file" ]; then
	echo "$0: cannot read $file" >&2
	exit 1
fi

while IFS= read -r line || [ -n "$line" ]; do
	case $line in
	'' | '#'*) continue ;;
	esac
	key=${line%%=*}
	case $key in
	"$line" | '' | [0-9]* | *[!A-Za-z0-9_]*)
		echo "$0: $file: not a KEY=VALUE line: $line" >&2
		exit 1
		;;
	esac
	# $key is a validated identifier, so this eval only reads the variable.
	eval "isset=\${$key+x}"
	if [ -z "$isset" ]; then
		export "$line"
	fi
done <"$file"

exec "$@"

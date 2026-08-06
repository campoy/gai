#!/bin/sh
# Table test for the no-secrets-staged hook: "<want-exit>|<command>"
#
#   sh .claude/hooks/no-secrets-staged_test.sh .claude/hooks/no-secrets-staged.sh
#
# The allow cases are not filler. Each of the last three is a false positive the
# hook actually produced and blocked real work with: an explicit path beginning
# with a dot, prose in a quoted argument, and a grep pattern. This hook must
# under-match rather than over-match — .gitignore already covers the common
# case, and a hook that blocks legitimate commits teaches people to route
# around it. Add a case here before changing the matching logic.
H="$1"
[ -n "$H" ] || { echo "usage: $0 <path-to-hook>" >&2; exit 64; }
fail=0
while IFS='|' read -r want cmd; do
	[ -n "$want" ] || continue
	printf '%s' "{\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"$cmd\"}}" | "$H" >/dev/null 2>&1
	got=$?
	if [ "$got" != "$want" ]; then
		printf 'FAIL want=%s got=%s  %s\n' "$want" "$got" "$cmd"
		fail=1
	else
		printf 'ok   exit=%s  %s\n' "$got" "$cmd"
	fi
done <<'CASES'
0|ls -la
0|go test ./...
0|git commit -m wip
0|git add agent/compact.go
0|git add .claude/settings.json AGENTS.md
0|git add ./tools/file.go
0|git add --dry-run agent/compact.go
0|echo 'never run git add -A in this repo'
0|npx tool --reply 'the hook blocked git add secrets/key twice'
0|grep -r 'git add .' docs/
2|git add .
2|git add -A
2|git add --all
2|git add -u
2|git add -Av
2|git add -vA
2|git add -f secrets/key
2|git add --force secrets/key
2|git add secrets/openai-api-key
2|git add ../secrets/openai-api-key
2|go build ./... && git add -u
2|git add secrets
2|cd /tmp && git add .
2|git status; git add -A
CASES
exit $fail

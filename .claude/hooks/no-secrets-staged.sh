#!/bin/sh
# Blocks any git command that could stage a file under secrets/.
#
# AGENTS.md requires explicit paths when staging, because a live API key was
# once committed to this repo and had to be purged with a root-commit rewrite.
# The reviewer used to check for this, but review happens after the push — by
# then a leaked key is already public and must be rotated regardless of the
# verdict. This is the same rule enforced at the only moment it still helps.
#
# Wired as a PreToolUse hook on Bash in .claude/settings.json. Exit 2 blocks the
# call and returns stderr to the model; exit 0 allows it.
#
# The arguments are tokenized rather than substring-matched. An earlier version
# globbed for "git add ." and blocked `git add .claude/settings.json`, which is
# a perfectly legitimate explicit path — the check has to see whole arguments.

command=$(jq -r '.tool_input.command // empty')

# Drop everything from the first heredoc operator onward. A heredoc body is
# data, not commands — and a commit message that *discusses* `git add` or
# secrets/ was otherwise enough to trip every check below. A hook cannot parse
# shell reliably, so it declines to read the part that is not shell. The cost is
# that a staging command placed after a heredoc goes unseen; this is a guard
# against accidents, and an accident does not hide behind one.
command=${command%%<<*}

# Everything after the first `git add`, which is where the pathspecs live. The
# trailing whitespace requirement keeps this from matching `git add-something`.
args=$(printf '%s\n' "$command" | sed -n 's/.*git[[:space:]]\{1,\}add[[:space:]]\{1,\}//p')

# Not a staging command: nothing to do. Note that `git commit -a` stages only
# tracked modifications, which cannot reach an ignored path, so it is not caught
# here — secrets/ is gitignored and only an explicit add can defeat that.
[ -n "$args" ] || exit 0

blanket() {
	echo "Blocked: blanket staging (\`$1\`)." >&2
	echo "AGENTS.md requires explicit paths so nothing under secrets/ can slip in." >&2
	echo "Stage the files you changed by name, e.g. \`git add agent/compact.go\`." >&2
	exit 2
}

force() {
	echo "Blocked: \`git add $1\` overrides .gitignore, which is the only thing keeping secrets/ untracked." >&2
	exit 2
}

for token in $args; do
	# Stop at a shell separator: what follows belongs to another command.
	case "$token" in
	';' | '&&' | '||' | '|' | '&') break ;;
	esac

	case "$token" in
	# A single-dash flag, possibly bundled as -Av or -uf. Test what it
	# contains rather than trying to spell every combination.
	-[!-]*)
		case "$token" in
		*[Au]*) blanket "$token" ;;
		esac
		case "$token" in
		*f*) force "$token" ;;
		esac
		;;
	# A blanket add stages whatever is present, including a secrets/ file that
	# a --force or a stale .gitignore let through.
	'.' | '--all' | '--update') blanket "$token" ;;
	'--force') force "$token" ;;
	# An explicit path under secrets/ is never legitimate.
	secrets | secrets/* | */secrets/*)
		echo "Blocked: this would stage a path under secrets/." >&2
		echo "That directory holds the raw OpenAI API key and is gitignored for that reason." >&2
		exit 2
		;;
	esac
done

exit 0

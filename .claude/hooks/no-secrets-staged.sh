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
# Three false positives shaped what follows, all the same mistake in different
# clothes — treating a command line as a string to search rather than a command
# to parse:
#
#   1. Globbing for "git add ." blocked `git add .claude/settings.json`, a
#      legitimate explicit path. Fix: tokenize and compare whole arguments.
#   2. A commit message that *discussed* staging tripped every check, because
#      the heredoc body was being read as shell. Fix: stop at the heredoc.
#   3. An unrelated command carrying the words "git add" inside a quoted
#      argument was treated as staging. Fix: only look at command positions.
#
# The general lesson, written here so the next edit keeps it: this hook must
# under-match rather than over-match. A false negative loses a backstop that
# .gitignore already covers; a false positive blocks legitimate work and
# teaches whoever hit it to route around the hook.

command=$(jq -r '.tool_input.command // empty')

# A heredoc body is data, not commands. Drop it — a commit message is allowed to
# talk about staging. The cost is that a staging command placed after a heredoc
# goes unseen; this guards against accidents, and an accident does not hide.
command=${command%%<<*}

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

secret() {
	echo "Blocked: this would stage \`$1\`, under secrets/." >&2
	echo "That directory holds the raw OpenAI API key and is gitignored for that reason." >&2
	exit 2
}

# Split on shell separators so each command starts a line. `git add` matters only
# at the head of one of these — anywhere else it is an argument to something
# else, and not ours to judge.
# Globbing stays off throughout, so a literal `*` argument is judged as written
# rather than expanded against the working directory.
set -f
saved_ifs=$IFS
IFS='
'
set -- $(printf '%s\n' "$command" | tr ';&|()' '\n\n\n\n\n')
# Restore default splitting before the argument loop below, which splits on
# spaces. Leaving IFS at newline here meant every pathspec arrived as a single
# token and nothing matched.
IFS=$saved_ifs

for line in "$@"; do
	# Trim leading whitespace so ` git add x` from `cd y && git add x` matches.
	line=${line#"${line%%[![:space:]]*}"}

	case "$line" in
	'git add' | 'git add '*) ;;
	*) continue ;;
	esac

	args=${line#git add}

	for token in $args; do
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
		# A blanket add stages whatever is present, including a secrets/ file
		# that a --force or a stale .gitignore let through.
		'.' | '--all' | '--update') blanket "$token" ;;
		'--force') force "$token" ;;
		# An explicit path under secrets/ is never legitimate.
		secrets | secrets/* | */secrets/*) secret "$token" ;;
		esac
	done
done
set +f

exit 0

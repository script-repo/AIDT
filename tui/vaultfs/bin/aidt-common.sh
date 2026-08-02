#!/bin/sh
# aidt-common.sh — shared helpers for the aidt-* vault tools.
# Sourced, not executed. POSIX sh only (dash/ash/bash all run these).

AIDT_AGENT_VAULT="${AIDT_AGENT_VAULT:-$HOME/Obsidian/AIDT-Agent-Vault}"
export AIDT_AGENT_VAULT
VAULT="$AIDT_AGENT_VAULT"

[ -d "$VAULT" ] || {
	echo "${TOOL:-aidt}: vault not found at $VAULT" >&2
	echo "${TOOL:-aidt}: set AIDT_AGENT_VAULT or redeploy the agent from AIDT." >&2
	exit 1
}

die() {
	echo "${TOOL:-aidt}: $*" >&2
	exit 1
}

now_utc() { date -u +%Y-%m-%dT%H:%M:%SZ; }

# slug <text> -> kebab-case identifier
slug() {
	printf %s "$1" | tr '[:upper:]' '[:lower:]' |
		sed -e 's/[^a-z0-9]\{1,\}/-/g' -e 's/^-//' -e 's/-$//'
}

# fm <file> <key> -> scalar value from YAML frontmatter, empty if absent
fm() {
	[ -f "$1" ] || return 0
	awk -v key="$2" '
		NR == 1 && $0 == "---" { inside = 1; next }
		inside && $0 == "---"  { exit }
		inside {
			i = index($0, ":")
			if (i > 0 && substr($0, 1, i - 1) == key) {
				v = substr($0, i + 1)
				sub(/^[ \t]+/, "", v); sub(/[ \t]+$/, "", v)
				gsub(/^["'\'']|["'\'']$/, "", v)
				print v
				exit
			}
		}
	' "$1"
}

# fmlist <file> <key> -> one list item per line. Handles both YAML forms:
#   key: [a, b]
#   key:
#     - a
#     - b
fmlist() {
	[ -f "$1" ] || return 0
	awk -v key="$2" '
		NR == 1 && $0 == "---" { inside = 1; next }
		inside && $0 == "---"  { exit }
		!inside { next }
		collecting {
			# A list item is indented and starts with "- ".
			if ($0 ~ /^[ \t]+-[ \t]*/) {
				v = $0
				sub(/^[ \t]+-[ \t]*/, "", v)
				sub(/[ \t]+$/, "", v)
				gsub(/^["'\'']|["'\'']$/, "", v)
				if (v != "") print v
				next
			}
			# Any non-item line ends the block.
			collecting = 0
		}
		{
			i = index($0, ":")
			if (i <= 0 || substr($0, 1, i - 1) != key) next
			v = substr($0, i + 1)
			sub(/^[ \t]+/, "", v); sub(/[ \t]+$/, "", v)
			if (v == "" || v == "[]") { collecting = 1; next }
			if (v ~ /^\[.*\]$/) {
				gsub(/^\[|\]$/, "", v)
				n = split(v, parts, ",")
				for (j = 1; j <= n; j++) {
					p = parts[j]
					sub(/^[ \t]+/, "", p); sub(/[ \t]+$/, "", p)
					gsub(/^["'\'']|["'\'']$/, "", p)
					if (p != "") print p
				}
				exit
			}
			print v
			exit
		}
	' "$1"
}

# has_item <needle> <<EOF ... list on stdin ... EOF
has_item() {
	needle="$1"
	while IFS= read -r item; do
		[ "$item" = "$needle" ] && return 0
	done
	return 1
}

# replace_block <file> <begin-marker> <end-marker> <content-file>
# Rewrites the region between the markers, preserving everything else.
# Atomic: writes a temp file and renames it into place.
replace_block() {
	_rb_file="$1" _rb_begin="$2" _rb_end="$3" _rb_body="$4"
	[ -f "$_rb_file" ] || die "missing $_rb_file"
	_rb_tmp="$_rb_file.tmp.$$"
	awk -v begin="$_rb_begin" -v end="$_rb_end" -v body="$_rb_body" '
		index($0, begin) { print; while ((getline line < body) > 0) print line; skip = 1; next }
		index($0, end)   { skip = 0 }
		!skip            { print }
	' "$_rb_file" >"$_rb_tmp" || { rm -f "$_rb_tmp"; die "failed to rewrite $_rb_file"; }
	mv "$_rb_tmp" "$_rb_file"
}

# md_cell <text> -> escape a value for a markdown table cell
md_cell() {
	printf %s "$1" | sed -e 's/|/\\|/g' -e 's/^[ \t]*//' -e 's/[ \t]*$//'
}

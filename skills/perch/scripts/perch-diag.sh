#!/bin/sh
# Read-only perch diagnostic collector.
#
# Usage: perch-diag.sh [--config PATH]
#
# Prints a redacted snapshot for an agent or operator. It never prints the
# config body, secrets, message bodies, or password values. It is safe to run
# while `perch serve` is running.

set -u

config=""
while [ "$#" -gt 0 ]; do
	case "$1" in
	--config)
		if [ "$#" -lt 2 ]; then
			echo "usage: $0 [--config PATH]" >&2
			exit 2
		fi
		config=$2
		shift 2
		;;
	-h | --help)
		sed -n '2,9p' "$0" | sed 's/^# \{0,1\}//'
		exit 0
		;;
	*)
		echo "usage: $0 [--config PATH]" >&2
		exit 2
		;;
	esac
done

if [ -z "$config" ]; then
	config=${PERCH_CONFIG:-${XDG_CONFIG_HOME:-$HOME/.config}/perch/perch.yaml}
fi

section() {
	printf '\n== %s ==\n' "$*"
}

show_cmd() {
	printf '\n$ %s\n' "$*"
}

run() {
	show_cmd "$@"
	"$@" 2>&1
	printf '[exit %s]\n' "$?"
}

have() {
	command -v "$1" >/dev/null 2>&1
}

if ! have perch; then
	echo "perch: not found in PATH" >&2
	echo "install it or add the binary to PATH before collecting diagnostics." >&2
	exit 1
fi

section "perch version"
perch version 2>&1
printf '[exit %s]\n' "$?"

section "config"
printf 'path: %s\n' "$config"
if [ -f "$config" ]; then
	printf 'present: yes\n'
	ls -l "$config" 2>/dev/null || true
	if [ -r "$config" ]; then
		# Report permissions without printing the file body.
		if have stat; then
			stat -c 'mode: %a owner: %U group: %G' "$config" 2>/dev/null ||
				stat -f 'mode: %Lp owner: %Su group: %Sg' "$config" 2>/dev/null || true
		fi
	else
		printf 'readable: no (run as the service user)\n'
	fi
else
	printf 'present: no\n'
fi

data_dir=$(sed -n 's/^[[:space:]]*data_dir:[[:space:]]*//p' "$config" 2>/dev/null | head -1 |
	sed -e 's/[[:space:]]*#.*$//' -e 's/^"//' -e 's/"$//' -e "s/^'//" -e "s/'$//")
if [ -n "$data_dir" ]; then
	printf 'data_dir: %s\n' "$data_dir"
	case "$data_dir" in
	/*) ;;
	*)
		printf 'warning: data_dir is relative; perch resolves it against the current directory (%s)\n' "$PWD"
		printf 'warning: run from the serve directory or use an absolute data_dir\n'
		;;
	esac
fi

section "doctor (may take ~20s)"
if have timeout; then
	show_cmd "perch doctor --config $config"
	timeout 60 perch doctor --config "$config" 2>&1
	printf '[exit %s]\n' "$?"
else
	run perch doctor --config "$config"
fi

section "accounts"
run perch account list --config "$config"

section "outbound"
run perch outbound list --config "$config" --limit 20

section "process"
if have pgrep; then
	show_cmd "pgrep -af 'perch serve'"
	pgrep -af 'perch serve' 2>&1
	printf '[exit %s]\n' "$?"
else
	show_cmd "ps -ef | grep '[p]erch serve'"
	ps -ef 2>/dev/null | grep '[p]erch serve' || true
fi

section "listeners"
if have ss; then
	show_cmd "ss -ltnp (filtered to configured ports)"
	ss -ltnp 2>/dev/null | head -1
	# Print lines for configured listener ports, plus the quickstart defaults.
	ports=$(sed -n -e 's/^[[:space:]]*addr:[[:space:]]*//p' \
		-e 's/^[[:space:]]*imaps_addr:[[:space:]]*//p' \
		-e 's/^[[:space:]]*tls_addr:[[:space:]]*//p' "$config" 2>/dev/null |
		sed -e 's/[[:space:]]*#.*$//' -e 's/^"//' -e 's/"$//' -e "s/^'//" -e "s/'$//" |
		grep -E ':[0-9]+$' | sed 's/.*://' | sort -u | tr '\n' ' ')
	[ -n "$ports" ] || ports="1143 1587 1993 1465"
	for port in $ports; do
		ss -ltnp 2>/dev/null | grep -E "[:.]${port}[[:space:]]" || true
	done
else
	show_cmd "netstat -an (listening)"
	netstat -an 2>/dev/null | grep -i listen || true
fi

section "systemd"
if have systemctl; then
	if systemctl list-unit-files perch.service 2>/dev/null | grep -q '^perch\.service'; then
		run systemctl is-active perch
		if have journalctl; then
			show_cmd "journalctl -u perch -n 50 --no-pager"
			journalctl -u perch -n 50 --no-pager 2>&1 || true
		fi
	else
		printf 'perch.service not installed\n'
	fi
else
	printf 'systemctl not available\n'
fi

section "database (read-only)"
db=""
if [ -n "$data_dir" ] && [ -f "$data_dir/perch.db" ]; then
	db="$data_dir/perch.db"
	printf 'database: %s\n' "$db"
	if have sqlite3; then
		show_cmd "sqlite3 -readonly (dead letters)"
		sqlite3 -readonly "$db" \
			"SELECT datetime(created_at,'unixepoch') AS at, address, task_id, reason FROM dead_letter ORDER BY id DESC LIMIT 10;" 2>&1 || true
		show_cmd "sqlite3 -readonly (outbound details)"
		sqlite3 -readonly "$db" \
			"SELECT datetime(created_at,'unixepoch') AS at, status, from_address, subject, attempt, smtp_code, enhanced_status, remote_response, reason FROM outbound ORDER BY id DESC LIMIT 10;" 2>&1 || true
	else
		printf 'sqlite3 not available; skip row-level queries\n'
	fi
else
	printf 'database not found at %s/perch.db\n' "${data_dir:-<unknown data_dir>}"
fi

section "next steps"
cat <<'EOF'
- Fix hard failures from top to bottom; doctor stops at the first one.
- For login failures, check DISABLED in the account table and the serve log
  for "IMAP login failed" / "IMAP login throttled".
- For missing inbound mail, check dead_letter reasons and whether the account
  is disabled or the alias/catch-all is missing.
- For outbound failures, inspect status, SMTP code, remote_response, reason.
- Do not paste this output publicly until you have checked it for addresses.
EOF

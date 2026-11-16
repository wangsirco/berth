#!/bin/sh
# Berth dev workspace bootstrap: PID 1's child in every workspace container.
# Mounted read-only at /opt/berth; per-workspace state (keys, password, scripts) is at
# /opt/berth-state, readable by root only. Everything here is idempotent: it runs on every start.
set -u
T=/opt/berth
S=/opt/berth-state
W="${BERTH_WORKSPACE:-/workspaces}"
log() { echo "[berth] $*"; }

pkg_install() {
	if command -v apt-get >/dev/null 2>&1; then
		DEBIAN_FRONTEND=noninteractive apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends "$@"
	elif command -v dnf >/dev/null 2>&1; then
		dnf install -y -q "$@"
	elif command -v apk >/dev/null 2>&1; then
		apk add --no-cache "$@"
	else
		log "no package manager for: $*"
		return 1
	fi
}

home_of() { awk -F: -v u="$1" '$1==u {print $6}' /etc/passwd; }

# --- the user people work as -------------------------------------------------
U="${BERTH_USER:-}"
if [ -z "$U" ] || ! id "$U" >/dev/null 2>&1; then
	U=$(awk -F: '$3>=1000 && $3<60000 && $7 !~ /nologin|false/ {print $1; exit}' /etc/passwd)
fi
if [ -z "$U" ]; then
	U=dev
	useradd -m -s /bin/bash "$U" 2>/dev/null || adduser -D -s /bin/sh "$U"
fi
G=$(id -gn "$U")
H=$(home_of "$U")
[ -n "$H" ] || H=/home/$U
mkdir -p "$H" && chown "$U:$G" "$H"
echo "$U" >"$S/user"
# a locked account ("!") refuses SSH keys on some builds: unlock it without a password
if [ -f /etc/shadow ] && awk -F: -v u="$U" '$1==u && $2 ~ /^!/ {f=1} END {exit !f}' /etc/shadow; then
	sed -i "s/^$U:![^:]*:/$U:*:/" /etc/shadow
fi
# sudo without a password, as in a dev container (the image may already allow it)
if command -v sudo >/dev/null 2>&1 && [ -d /etc/sudoers.d ] && [ ! -f /etc/sudoers.d/berth-dev ]; then
	echo "$U ALL=(ALL) NOPASSWD:ALL" >/etc/sudoers.d/berth-dev && chmod 440 /etc/sudoers.d/berth-dev
fi

as_user() { # run a shell command as the user, keeping the container's environment
	if command -v runuser >/dev/null 2>&1; then
		HOME="$H" USER="$U" runuser -u "$U" -- sh -c "$1"
	else
		HOME="$H" USER="$U" su "$U" -s /bin/sh -c "$1"
	fi
}

# --- files made by Berth belong to the user ---------------------------------
if [ -f "$S/chown-pending" ]; then
	chown -R "$U:$G" "$W" && rm -f "$S/chown-pending"
fi

# --- Claude Code --------------------------------------------------------------
# One shared copy (glibc) at /opt/berth/claude; a musl image (Alpine) gets the official
# installer in the user's home instead. A token the panel keeps (API key or a long-lived
# login token) reaches terminals and SSH through the user's own 600 file.
# `boot.sh sync-claude` (from the panel, by docker exec) redoes just this part.
claude_setup() {
	if ldd /bin/sh 2>&1 | grep -q musl || [ -f /lib/ld-musl-aarch64.so.1 ]; then
		if [ ! -x "$H/.local/bin/claude" ]; then
			log "installing Claude Code (musl)"
			command -v curl >/dev/null 2>&1 || pkg_install curl bash libgcc libstdc++ ripgrep
			as_user "curl -fsSL https://claude.ai/install.sh | bash" >"$S/claude-install.log" 2>&1 || log "Claude Code install failed"
		fi
		[ -x "$H/.local/bin/claude" ] && ln -sf "$H/.local/bin/claude" /usr/local/bin/claude
	elif [ -x "$T/claude/claude" ]; then
		mkdir -p /usr/local/bin && ln -sf "$T/claude/claude" /usr/local/bin/claude
	fi
	CE="$H/.config/berth/claude.env"
	if [ -s "$S/claude.env" ]; then
		as_user "mkdir -p '$H/.config/berth'"
		cp "$S/claude.env" "$CE" && chown "$U:$G" "$CE" && chmod 600 "$CE"
		[ -f "$H/.claude.json" ] || as_user "printf '%s\n' '{\"hasCompletedOnboarding\": true, \"theme\": \"dark\"}' >'$H/.claude.json'"
	else
		rm -f "$CE"
	fi
	printf '%s\n' '[ -r "$HOME/.config/berth/claude.env" ] && . "$HOME/.config/berth/claude.env"' >/etc/profile.d/01-berth-claude.sh
}
if [ "${1:-}" = sync-claude ]; then
	claude_setup
	exit 0
fi
[ "${BERTH_CLAUDE:-0}" = 1 ] && claude_setup

# --- SSH (keys only) ----------------------------------------------------------
if [ "${BERTH_SSH:-1}" = 1 ]; then
	if [ ! -x /usr/sbin/sshd ]; then
		log "installing the SSH server"
		pkg_install openssh-server || pkg_install openssh
	fi
	mkdir -p /run/sshd "$S/ssh"
	[ -f "$S/ssh/ssh_host_ed25519_key" ] || ssh-keygen -q -t ed25519 -N "" -f "$S/ssh/ssh_host_ed25519_key"
	mkdir -p "$H/.ssh"
	cp "$S/authorized_keys" "$H/.ssh/authorized_keys" 2>/dev/null || : >"$H/.ssh/authorized_keys"
	chown -R "$U:$G" "$H/.ssh" && chmod 700 "$H/.ssh" && chmod 600 "$H/.ssh/authorized_keys"
	# the environment SSH sessions see (docker's env is not inherited by sshd children)
	export -p | grep -v -E '^(export|declare -x) (HOME|USER|PWD|OLDPWD|SHLVL|HOSTNAME|TERM|BERTH_[A-Z_]*|_)=' >/etc/profile.d/00-berth-env.sh 2>/dev/null || true
	/usr/sbin/sshd -p 2222 -h "$S/ssh/ssh_host_ed25519_key" -o PasswordAuthentication=no -o KbdInteractiveAuthentication=no \
		-o PermitRootLogin=no -o AuthorizedKeysFile=.ssh/authorized_keys -o PidFile=/run/berth-sshd.pid -o AllowTcpForwarding=yes \
		&& log "SSH on 2222 as $U" || log "SSH failed to start"
fi

# --- devcontainer lifecycle --------------------------------------------------
run_hook() { # name: runs $S/<name>.sh as the user in the workspace, logs to $S/<name>.log
	[ -f "$S/$1.sh" ] || return 0
	cp "$S/$1.sh" "/tmp/berth-$1.sh" && chmod 755 "/tmp/berth-$1.sh"
	echo running >"$S/$1.status"
	if as_user "cd '$W' && sh /tmp/berth-$1.sh" >"$S/$1.log" 2>&1; then
		echo done >"$S/$1.status"
	else
		echo failed >"$S/$1.status"
	fi
}
(
	if [ ! -f "$S/created" ]; then
		run_hook on-create
		run_hook post-create
		touch "$S/created"
	fi
	run_hook post-start
) &

# --- web VS Code --------------------------------------------------------------
if [ "${BERTH_CODE:-0}" = 1 ] && [ -x "$T/code-server/bin/code-server" ]; then
	DATA="$H/.local/share/code-server"
	as_user "mkdir -p '$DATA/User'"
	if [ ! -f "$DATA/User/settings.json" ]; then
		as_user "printf '%s\n' '{\"workbench.colorTheme\": \"Default Dark Modern\", \"security.workspace.trust.enabled\": false, \"window.restoreWindows\": \"none\"}' >'$DATA/User/settings.json'"
	fi
	TLS=""
	[ -f "$S/tls.crt" ] && cp "$S/tls.crt" "$S/tls.key" /tmp/ 2>/dev/null && chown "$U" /tmp/tls.crt /tmp/tls.key && TLS="--cert /tmp/tls.crt --cert-key /tmp/tls.key"
	PW=$(cat "$S/password" 2>/dev/null)
	LOC=""
	[ -n "${BERTH_LOCALE:-}" ] && LOC="--locale $BERTH_LOCALE"
	install_ext() { as_user "'$T/code-server/bin/code-server' --user-data-dir '$DATA' --install-extension '$1'" >>"$S/extensions.log" 2>&1; }
	if [ -f "$S/extensions" ] && [ ! -f "$S/extensions.done" ]; then
		# the language pack first, before the editor starts (it is read only at start)
		grep -i 'language-pack' "$S/extensions" | while read -r ext; do install_ext "$ext"; done
		as_user "'$T/code-server/lib/node' '$T/langpacks.js' '$DATA'" >>"$S/extensions.log" 2>&1
		(
			grep -iv 'language-pack' "$S/extensions" | while read -r ext; do [ -n "$ext" ] && install_ext "$ext"; done
			touch "$S/extensions.done"
		) &
	fi
	(
		export PASSWORD="$PW"
		while :; do
			as_user "exec '$T/code-server/bin/code-server' --bind-addr 0.0.0.0:8443 --auth password $TLS $LOC \
				--disable-telemetry --disable-update-check --user-data-dir '$DATA' --app-name 'Berth · ${BERTH_NAME:-dev}' '$W'" >"$S/code-server.log" 2>&1
			sleep 3
		done
	) &
	log "web VS Code on 8443"
fi

log "ready"
exec sleep infinity

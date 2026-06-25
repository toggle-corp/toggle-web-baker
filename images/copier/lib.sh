#!/usr/bin/env bash
# lib.sh -- pure-ish gate/assembly functions for the copier.
#
# Sourced by entrypoint.sh AND by the unit tests. Functions take their inputs as
# arguments (not globals) wherever practical so they can be exercised without a
# container runtime. Side-effecting functions (retention sweep, assemble, flip)
# operate on real paths the test harness sets up in a tmpdir.
#
# Conventions:
#   * Every function returns 0 on success, non-zero on a gate failure.
#   * Reasons are printed to stderr; the entrypoint maps a failure into the
#     termination message.

# du_bytes <path> -- apparent byte size of a tree (du -sb). Echoes an integer.
du_bytes() {
	du -sb -- "$1" | awk '{print $1}'
}

# free_bytes <path> -- bytes free on the filesystem holding <path>.
# df -B1 reports in 1-byte blocks; the "Available" column is field 4.
free_bytes() {
	df -B1 -- "$1" | awk 'NR==2 {print $4}'
}

# is_uint <value> -- true iff value is a non-negative integer.
is_uint() {
	case "$1" in
	'' | *[!0-9]*) return 1 ;;
	*) return 0 ;;
	esac
}

# gate_size_cap <source_bytes> <cap_bytes>
# Reject when source exceeds the cap. A cap of 0 means "unset / no cap".
gate_size_cap() {
	local src="$1" cap="$2"
	is_uint "$src" || {
		echo "size-gate: non-numeric source bytes '$src'" >&2
		return 2
	}
	is_uint "$cap" || {
		echo "size-gate: non-numeric cap '$cap'" >&2
		return 2
	}
	if [ "$cap" -gt 0 ] && [ "$src" -gt "$cap" ]; then
		echo "size-gate: output $src bytes exceeds RELEASE_SIZE_CAP $cap" >&2
		return 1
	fi
	return 0
}

# gate_free_space <source_bytes> <headroom_bytes> <free_bytes>
# Require source + headroom <= free.
gate_free_space() {
	local src="$1" headroom="$2" free="$3"
	if ! { is_uint "$src" && is_uint "$headroom" && is_uint "$free"; }; then
		echo "free-gate: non-numeric input (src=$src headroom=$headroom free=$free)" >&2
		return 2
	fi
	local need=$((src + headroom))
	if [ "$need" -gt "$free" ]; then
		echo "free-gate: need $need bytes (source+headroom) but only $free free on /output" >&2
		return 1
	fi
	return 0
}

# safe_name <basename> -- true iff a path component is safe to assemble.
# Rejects path-traversal and odd/control characters. Allows POSIX-portable
# filename chars plus space, '#', and a few common web-asset punctuation marks.
safe_name() {
	local n="$1"
	case "$n" in
	'' | '.' | '..') return 1 ;;
	*/* | *$'\n'* | *$'\t'*) return 1 ;; # no separators / control whitespace
	esac
	# Disallow a leading dash (option-injection) defensively.
	case "$n" in
	-*) return 1 ;;
	esac
	return 0
}

# scan_unsafe <root> -- echo any entry name under <root> (one level deep is not
# enough; we walk the whole tree) that fails safe_name. Returns 1 if any found.
# Symlinks pointing outside <root> are handled separately by rsync --safe-links,
# but we additionally reject traversal-y names here as defense in depth.
scan_unsafe() {
	local root="$1" rc=0
	# -print0 + read -d '' to survive spaces/newlines in names.
	while IFS= read -r -d '' path; do
		local base="${path##*/}"
		if ! safe_name "$base"; then
			echo "scan: unsafe path component in '$path'" >&2
			rc=1
		fi
	done < <(find "$root" -mindepth 1 -print0)
	return "$rc"
}

# assemble <source_dir> <dest_dir> <owner_uid_gid>
# rsync the source tree into dest, stripping symlinks that point outside the
# tree (--safe-links) and copying symlink targets that are inside. Then chown
# the assembled tree to the platform user so nginx's
# `disable_symlinks if_not_owner` treats only platform-owned files as
# followable.
assemble() {
	local src="$1" dest="$2" owner="$3"
	mkdir -p -- "$dest"
	# -a: archive; --safe-links: drop symlinks pointing outside the tree;
	# trailing slash on src copies its CONTENTS into dest.
	rsync -a --safe-links --no-specials --no-devices -- "$src/" "$dest/" || {
		echo "assemble: rsync failed" >&2
		return 1
	}
	chown -R -- "$owner" "$dest" || {
		echo "assemble: chown to $owner failed" >&2
		return 1
	}
	return 0
}

# atomic_flip <output_root> <release_rel_path>
# Point <output_root>/current at <release_rel_path> with no half-written window:
# create a temp symlink then rename it over current (rename is atomic on a POSIX
# filesystem). <release_rel_path> is RELATIVE to output_root so the symlink
# survives the volume being mounted at a different absolute path.
atomic_flip() {
	local root="$1" rel="$2"
	ln -sfn -- "$rel" "$root/current.tmp" || {
		echo "flip: cannot create current.tmp" >&2
		return 1
	}
	mv -T -- "$root/current.tmp" "$root/current" || {
		echo "flip: atomic rename failed" >&2
		return 1
	}
	return 0
}

# retention_sweep <releases_dir> <keep> <current_target_basename>
# Delete release dirs keeping the one `current` points at PLUS the newest $keep
# others. Run BEFORE measuring/copying so the free-space gate sees reclaimed
# space (race-free: the new release does not exist yet).
#
# "Newest" is by directory name, which is a sortable timestamp (see TS_FORMAT in
# the entrypoint), so a lexical sort is a chronological sort.
retention_sweep() {
	local rel_dir="$1" keep="$2" keep_current="$3"
	is_uint "$keep" || {
		echo "retention: non-numeric KEEP_RELEASES '$keep'" >&2
		return 2
	}
	[ -d "$rel_dir" ] || return 0 # nothing to sweep yet

	# Collect release dir basenames, newest first.
	local -a all=()
	while IFS= read -r name; do
		[ -n "$name" ] && all+=("$name")
	done < <(find "$rel_dir" -mindepth 1 -maxdepth 1 -type d -printf '%f\n' | sort -r)

	local kept=0 name
	for name in "${all[@]}"; do
		# Always keep whatever `current` resolves to.
		if [ -n "$keep_current" ] && [ "$name" = "$keep_current" ]; then
			continue
		fi
		if [ "$kept" -lt "$keep" ]; then
			kept=$((kept + 1))
			continue
		fi
		rm -rf -- "${rel_dir:?}/${name:?}" || {
			echo "retention: failed to remove $name" >&2
			return 1
		}
		echo "retention: pruned release $name" >&2
	done
	return 0
}

# read_phase_env_value <phase_env_file> <key>
# Echo the value of KEY=VALUE from the phase-env file, or empty if absent.
# Pure convention file; missing file/key is not an error.
read_phase_env_value() {
	local file="$1" key="$2"
	[ -r "$file" ] || return 0
	# Take the LAST assignment wins; strip surrounding quotes.
	local line val
	line="$(grep -E "^${key}=" "$file" | tail -n1 || true)"
	[ -n "$line" ] || return 0
	val="${line#*=}"
	val="${val%\"}"
	val="${val#\"}"
	val="${val%\'}"
	val="${val#\'}"
	printf '%s' "$val"
}

# json_escape <string> -- minimal JSON string escaper for values we emit.
json_escape() {
	local s="$1"
	s="${s//\\/\\\\}"
	s="${s//\"/\\\"}"
	s="${s//$'\n'/\\n}"
	s="${s//$'\t'/\\t}"
	s="${s//$'\r'/}"
	printf '%s' "$s"
}

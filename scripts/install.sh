#!/bin/sh
# Installs arena into a versioned store and points $ARENA_PREFIX/bin/arena at it.
#
#   install.sh                 install the latest release
#   install.sh --from PATH     install a local binary (used by make install)
#
# Environment:
#   ARENA_PREFIX            install prefix (default: $HOME/.local)
#   ARENA_VERSION           release to install (default: the latest release)
#   ARENA_RELEASE_BASE_URL  release host (default: the GitHub releases URL)
#
# The binary lands in $ARENA_PREFIX/lib/arena/<version>/arena and bin/arena is a
# symlink to it. A running arena pins its own executable, so each process keeps
# the version it started with while the link moves on.
#
# Everything runs inside main, which is called on the last line. A download that
# is cut short therefore either fails to parse or never reaches that call, and
# never runs half of the install.
set -eu

cleanup() {
	if [ -n "$tmp" ]; then rm -rf "$tmp"; fi
	if [ -n "$tmp_bin" ]; then rm -f "$tmp_bin"; fi
	if [ -n "$tmp_link" ]; then rm -f "$tmp_link"; fi
	if [ -n "$tmp_json" ]; then rm -f "$tmp_json"; fi
}

say() { printf '%s\n' "$*"; }
die() {
	printf 'arena install: %s\n' "$*" >&2
	exit 1
}

usage() {
	say "usage: install.sh [--from PATH]"
	say "  Installs the latest arena release, or the local binary at PATH."
	say "  Environment: ARENA_PREFIX, ARENA_VERSION, ARENA_RELEASE_BASE_URL"
}

# Makes untrusted text safe to show in a message.
printable() {
	printf '%s' "$1" | LC_ALL=C tr -c '[:print:]' '?'
}

# A release version is MAJOR.MINOR.PATCH with an optional -prerelease suffix of
# [0-9A-Za-z.-], with no leading v. The same rule guards the release tag in
# release.yml. The version becomes a directory name and part of a URL, so nothing
# else gets through. A newline is refused first because grep matches by line.
valid_version() {
	nl=$(printf '\n.')
	nl=${nl%.}
	case $1 in
	*"$nl"*) return 1 ;;
	esac
	printf '%s\n' "$1" | LC_ALL=C grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'
}

# require_version DESCRIPTION VALUE
require_version() {
	valid_version "$2" ||
		die "$1 '$(printable "$2")' is not a release version; expected MAJOR.MINOR.PATCH with an optional -prerelease suffix and no leading v"
}

sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{ print $1 }'
	else
		shasum -a 256 "$1" | awk '{ print $1 }'
	fi
}

# Prints the last field of the last non-empty line of "$1 version". The update
# notice is switched off so that asking for the version never reaches the network.
binary_version() {
	ARENA_NO_UPDATE_CHECK=1 "$1" version </dev/null 2>/dev/null | awk 'NF { v = $NF } END { print v }'
}

# Copies $1 to $2/arena by renaming a sibling temp file into place, so an
# arena that is already running from that path is never rewritten.
place() {
	mkdir -p "$2"
	tmp_bin=$2/.arena.$$
	cp "$1" "$tmp_bin"
	chmod 755 "$tmp_bin"
	mv -f "$tmp_bin" "$2/arena"
	tmp_bin=
}

json_escape() {
	printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'
}

main() {
	tmp=
	tmp_bin=
	tmp_link=
	tmp_json=
	trap cleanup EXIT
	trap 'exit 1' HUP INT TERM

	from=
	while [ $# -gt 0 ]; do
		case $1 in
		--from)
			[ $# -ge 2 ] || die "--from needs a path"
			from=$2
			shift 2
			;;
		--from=*)
			from=${1#--from=}
			shift
			;;
		-h | --help)
			usage
			exit 0
			;;
		*)
			usage >&2
			die "unknown argument: $1"
			;;
		esac
	done

	case $(uname -s) in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	MINGW* | MSYS* | CYGWIN*) die "native Windows is not supported; install inside WSL instead" ;;
	*) die "unsupported operating system: $(uname -s)" ;;
	esac

	prefix=${ARENA_PREFIX:-}
	if [ -z "$prefix" ]; then
		[ -n "${HOME:-}" ] || die "set ARENA_PREFIX or HOME"
		prefix=$HOME/.local
	fi
	case $prefix in
	/*) ;;
	*) prefix=$(pwd)/$prefix ;;
	esac
	# The bin directory as the user spelled it, for the PATH check below.
	logical_bindir=${prefix%/}/bin
	# Record the physical prefix. update and uninstall find the install from the
	# resolved path of the running binary and refuse a manifest that disagrees.
	mkdir -p "$prefix" || die "cannot create $prefix"
	prefix=$(cd "$prefix" && pwd -P) || die "cannot resolve $prefix"
	case $prefix in
	/ | //) die "refusing to install into the root directory" ;;
	esac
	prefix=${prefix%/}
	bindir=$prefix/bin
	libdir=$prefix/lib/arena
	sharedir=$prefix/share/arena
	link=$bindir/arena

	if command -v sha256sum >/dev/null 2>&1 || command -v shasum >/dev/null 2>&1; then
		:
	else
		die "need sha256sum or shasum to verify files"
	fi

	# Every refusal about the layout comes here, before the download and before
	# any directory is made, so a refused install leaves nothing under the prefix.
	# update and uninstall resolve the running binary to its store path, so the
	# store must not sit behind a symlink. The prefix is already physical, which
	# leaves lib and lib/arena as the only components that can redirect it.
	for d in "$prefix/lib" "$libdir"; do
		if [ -L "$d" ]; then
			die "$libdir passes through a symbolic link ($d); use a prefix whose lib/arena is a real directory"
		fi
		if [ -e "$d" ] && [ ! -d "$d" ]; then die "$d is not a directory"; fi
	done
	if [ -d "$link" ] && [ ! -L "$link" ]; then die "$link is a directory"; fi

	if [ -n "$from" ]; then
		case $from in
		/*) ;;
		*) from=$(pwd)/$from ;;
		esac
		if [ ! -f "$from" ] || [ ! -x "$from" ]; then die "$from is not an executable file"; fi
		src=$from
		origin="make"
		ver=$(binary_version "$src")
		require_version "the version reported by '$src version'" "$ver"
	else
		command -v curl >/dev/null 2>&1 || die "curl is required to download arena"
		case $(uname -m) in
		x86_64) arch=amd64 ;;
		arm64 | aarch64) arch=arm64 ;;
		*) die "unsupported architecture: $(uname -m)" ;;
		esac
		base=${ARENA_RELEASE_BASE_URL:-https://github.com/Kayyo321/agent-arena/releases}
		base=${base%/}

		ver=${ARENA_VERSION:-}
		if [ -z "$ver" ]; then
			# The latest page answers with a redirect to the release tag; read it
			# rather than following it.
			location=$(curl -fsS -o /dev/null -D - "$base/latest" | tr -d '\r' |
				awk 'tolower($1) == "location:" { loc = $2 } END { print loc }')
			[ -n "$location" ] || die "could not resolve the latest release from $base/latest"
			location=${location%/}
			ver=${location##*/}
			what="the latest release tag"
		else
			what="ARENA_VERSION"
		fi
		# Release tags are vX.Y.Z; the stored version has no v.
		ver=${ver#v}
		require_version "$what" "$ver"

		archive=arena_${ver}_${os}_${arch}.tar.gz
		tmp=$(mktemp -d "${TMPDIR:-/tmp}/arena-install.XXXXXX")
		say "Downloading arena $ver for $os/$arch"
		curl -fsSL -o "$tmp/$archive" "$base/download/v$ver/$archive"
		curl -fsSL -o "$tmp/SHA256SUMS" "$base/download/v$ver/SHA256SUMS"

		want=$(awk -v f="$archive" '{ n = $2; sub(/^\*/, "", n) } n == f { print $1; exit }' "$tmp/SHA256SUMS")
		[ -n "$want" ] || die "SHA256SUMS has no entry for $archive"
		got=$(sha256_of "$tmp/$archive")
		[ "$got" = "$want" ] || die "checksum mismatch for $archive (expected $want, got $got)"

		mkdir "$tmp/x"
		tar -xzf "$tmp/$archive" -C "$tmp/x" arena
		src=$tmp/x/arena
		if [ ! -f "$src" ] || [ -L "$src" ]; then die "$archive does not contain an arena binary"; fi
		chmod 755 "$src"
		reported=$(binary_version "$src")
		[ -n "$reported" ] || die "the downloaded arena binary does not run on this system"
		# The exit trap removes the download directory.
		[ "$reported" = "$ver" ] ||
			die "the downloaded binary reports version '$(printable "$reported")', not $ver; nothing was installed"
		origin=release
	fi

	# An older make install left a plain file at bin/arena. Keep it in the store
	# under its own version; the link below replaces it in a single rename.
	if [ -f "$link" ] && [ ! -L "$link" ]; then
		old=$(binary_version "$link")
		valid_version "$old" || old=legacy
		if [ ! -e "$libdir/$old/arena" ]; then
			place "$link" "$libdir/$old"
			say "Kept the existing $link as $libdir/$old/arena"
		fi
	fi

	store=$libdir/$ver
	place "$src" "$store"
	store_bin=$store/arena

	mkdir -p "$bindir"
	tmp_link=$bindir/.arena.$$
	ln -s "$store_bin" "$tmp_link"
	mv -f "$tmp_link" "$link"
	tmp_link=

	mkdir -p "$sharedir"
	tmp_json=$sharedir/.install.json.$$
	{
		printf '{\n'
		printf '  "schemaVersion": 1,\n'
		printf '  "prefix": "%s",\n' "$(json_escape "$prefix")"
		printf '  "version": "%s",\n' "$ver"
		printf '  "storePath": "%s",\n' "$(json_escape "$store_bin")"
		printf '  "binarySha256": "%s",\n' "$(sha256_of "$store_bin")"
		printf '  "source": "%s",\n' "$origin"
		printf '  "installedAt": "%s"\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
		printf '}\n'
	} >"$tmp_json"
	mv -f "$tmp_json" "$sharedir/install.json"
	tmp_json=

	say "Installed arena $ver in $store_bin"
	case ":${PATH:-}:" in
	*":$bindir:"* | *":$logical_bindir:"*) ;;
	*)
		say "$logical_bindir is not on your PATH. Add it with:"
		# shellcheck disable=SC2016
		printf '  export PATH="%s:$PATH"\n' "$logical_bindir"
		;;
	esac
	ARENA_NO_UPDATE_CHECK=1 "$link" version </dev/null
}

main "$@"

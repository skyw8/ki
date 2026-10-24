#!/bin/sh

set -eu

REPO="skyw8/ki"

say() { printf '%s\n' "$*"; }
die() {
	printf 'ki install: %s\n' "$*" >&2
	exit 1
}

command -v uname >/dev/null 2>&1 || die "uname is required to detect the platform"

case "$(uname -s)" in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) die "unsupported system $(uname -s); the release builds linux/amd64, darwin/arm64 and windows/amd64, otherwise build from source" ;;
esac

case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) die "unsupported machine $(uname -m); build from source" ;;
esac

case "${os}-${arch}" in
linux-amd64 | darwin-arm64) ;;
*) die "no release archive for ${os}/${arch}; the release builds linux/amd64, darwin/arm64 and windows/amd64, otherwise build from source" ;;
esac

fetch() {
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL "$1"
	elif command -v wget >/dev/null 2>&1; then
		wget -qO- "$1"
	else
		die "curl or wget is required"
	fi
}

download() {
	fetch "$1" >"$2"
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | cut -d' ' -f1
	elif command -v openssl >/dev/null 2>&1; then
		openssl dgst -sha256 "$1" | sed 's/.*[ =]//'
	else
		die "sha256sum, shasum or openssl is required to verify the download"
	fi
}

version="${KI_VERSION:-}"
if [ -z "$version" ]; then
	latest="$(fetch "https://api.github.com/repos/${REPO}/releases/latest" || true)"
	version="$(printf '%s' "${latest:-}" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"
	[ -n "$version" ] || die "could not resolve the latest release; set KI_VERSION to the tag you want"
fi
version="${version#v}"

base="https://github.com/${REPO}/releases/download/v${version}"
archive="ki-${version}-${os}-${arch}.tar.gz"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM HUP

download "${base}/${archive}" "${tmp}/${archive}" || die "could not download ${base}/${archive}"
download "${base}/checksums.txt" "${tmp}/checksums.txt" || die "could not download ${base}/checksums.txt"

expected="$(awk -v name="$archive" '$2 == name { print $1 }' "${tmp}/checksums.txt")"
[ -n "$expected" ] || die "checksums.txt of v${version} has no entry for ${archive}"
actual="$(sha256 "${tmp}/${archive}")"
[ "$expected" = "$actual" ] || die "checksum mismatch for ${archive}: expected ${expected}, got ${actual}"

tar -xzf "${tmp}/${archive}" -C "$tmp"
src="${tmp}/ki-${version}-${os}-${arch}/ki"
[ -f "$src" ] || die "${archive} does not contain ki"

dir="${KI_BIN_DIR:-$HOME/.local/bin}"
mkdir -p "$dir" || die "cannot create ${dir}; set KI_BIN_DIR to a writable directory"
if command -v install >/dev/null 2>&1; then
	install -m 755 "$src" "${dir}/ki"
else
	cp "$src" "${dir}/ki" && chmod 755 "${dir}/ki"
fi

say "installed ki ${version} to ${dir}/ki"
case ":${PATH}:" in
*":${dir}:"*) ;;
*)
	say "add ${dir} to PATH to run ki directly:"
	say "  export PATH=\"${dir}:\$PATH\""
	;;
esac

if [ -f "${KI_HOME:-$HOME/.ki}/server.json" ]; then
	say ""
	say "a ki server is running from the previous build; activate ${version} with:"
	say "  ${dir}/ki restart"
	say "(this stops the old server and starts ${dir}/ki on the same address)"
fi

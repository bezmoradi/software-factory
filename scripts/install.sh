#!/bin/sh
set -eu

version='@VERSION@'
compose_sha256='@COMPOSE_SHA256@'
release_sha256='@RELEASE_SHA256@'

project=software-factory
latest=https://github.com/bezmoradi/software-factory/releases/latest/download/install.sh
base="https://github.com/bezmoradi/software-factory/releases/download/v$version"
lowest_compose=2.24.4
lowest_engine=28
LC_ALL=C
export LC_ALL

stop() {
	printf 'Install stopped: %s\n' "$1" >&2
	exit 1
}

digits() {
	case $1 in
	'' | *[!0-9]*) return 1 ;;
	esac
	return 0
}

compose_new_enough() {
	v=${1#v}
	case $v in
	*.*.*) ;;
	*) return 1 ;;
	esac
	major=${v%%.*}
	rest=${v#*.}
	minor=${rest%%.*}
	patch=${rest#*.}
	patch=${patch%%[!0-9]*}
	if ! digits "$major" || ! digits "$minor" || ! digits "$patch"; then
		return 1
	fi
	if [ "$major" -gt 2 ] || { [ "$major" -eq 2 ] && [ "$minor" -gt 24 ]; } || { [ "$major" -eq 2 ] && [ "$minor" -eq 24 ] && [ "$patch" -ge 4 ]; }; then
		return 0
	fi
	return 1
}

sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sum=$(sha256sum "$1") || return 1
	else
		sum=$(shasum -a 256 "$1") || return 1
	fi
	printf '%s\n' "${sum%% *}"
}

fetch() {
	name=$1
	want=$2
	part="$home/$name.download"
	rm -f "$part"
	if ! curl -fsSL --proto '=https' --tlsv1.2 --retry 3 -o "$part" "$base/$name"; then
		rm -f "$home/compose.yaml.download" "$home/compose.release.yaml.download"
		stop "couldn't download $base/$name; nothing in $home was changed."
	fi
	if ! got=$(sha256_of "$part") || [ "$got" != "$want" ]; then
		rm -f "$home/compose.yaml.download" "$home/compose.release.yaml.download"
		stop "$name from $base doesn't match the SHA-256 this script carries, so the download was removed and nothing in $home was changed."
	fi
}

main() {
	exec </dev/null
	case $version$compose_sha256$release_sha256 in
	*@*) stop "this is the template in the repository, not a release's script. Install with: curl -fsSL $latest | sh" ;;
	esac

	command -v curl >/dev/null 2>&1 || stop "curl isn't installed. Install curl and run this again."
	if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
		stop "neither sha256sum nor shasum is installed, so the downloads can't be checked. Install coreutils (or perl's shasum) and run this again."
	fi
	command -v docker >/dev/null 2>&1 || stop "docker isn't installed. Install Docker Desktop (macOS, Windows) or Docker Engine with the Compose plugin (Linux): https://docs.docker.com/get-docker/"
	compose=$(docker compose version --short 2>/dev/null) || stop "docker compose doesn't run. Install the Docker Compose plugin, $lowest_compose or later: https://docs.docker.com/compose/install/"
	compose_new_enough "$compose" || stop "this needs Docker Compose $lowest_compose or later, and docker compose version --short gave $compose. Update Docker Desktop, or the Compose plugin on Linux."
	[ "${compose#v}" != 5.1.4 ] || stop "Docker Compose 5.1.4 has a bug reading the YAML merge keys the Compose files use, fixed in 5.2.0. Update Docker Desktop, or the Compose plugin on Linux."
	engine=$(docker version --format '{{.Server.Version}}' 2>/dev/null) || stop "Docker's daemon doesn't answer. Start Docker Desktop, or the docker service on Linux, and run this again."
	if ! digits "${engine%%.*}" || [ "${engine%%.*}" -lt "$lowest_engine" ]; then
		stop "this needs Docker Engine $lowest_engine or later, and the daemon is $engine. Update Docker Desktop, or Docker Engine on Linux."
	fi

	socket=${SF_DOCKER_SOCKET:-/var/run/docker.sock}
	case $socket in
	/*) ;;
	*) stop "SF_DOCKER_SOCKET must be an absolute path, and it is $socket." ;;
	esac
	case $socket in
	*[!A-Za-z0-9._/-]*) stop "SF_DOCKER_SOCKET may hold only letters, digits and . _ / -, and it is $socket." ;;
	esac
	if [ -n "${SF_DOCKER_GID:-}" ]; then
		gid=$SF_DOCKER_GID
	elif [ "$(uname -s)" = Darwin ]; then
		gid=0
	else
		gid=$(stat -c %g "$socket" 2>/dev/null) || stop "couldn't read the group of $socket. Set SF_DOCKER_GID to the group that owns Docker's socket and run this again."
	fi
	digits "$gid" || stop "SF_DOCKER_GID must be digits, and it is $gid."

	[ -n "${HOME:-}" ] || [ -n "${SF_HOME:-}" ] || stop "HOME isn't set. Set SF_HOME to the folder to install into."
	home=${SF_HOME:-$HOME/software-factory}
	case $home in
	/*) ;;
	*) home="$(pwd)/$home" ;;
	esac
	[ ! -e "$home/.git" ] || stop "$home is a git clone. Run make up there, or set SF_HOME to another folder for the release."
	mkdir -p "$home" || stop "couldn't make the folder $home."
	cd "$home" || stop "couldn't go into $home."

	fetch compose.yaml "$compose_sha256"
	fetch compose.release.yaml "$release_sha256"
	mv -f compose.yaml.download compose.yaml || stop "couldn't write $home/compose.yaml."
	mv -f compose.release.yaml.download compose.release.yaml || stop "couldn't write $home/compose.release.yaml."
	printf 'services:\n  helper:\n    group_add: ["%s"]\n    volumes:\n      - "%s:/var/run/docker.sock"\n' "$gid" "$socket" >software-factory-socket.yaml || stop "couldn't write $home/software-factory-socket.yaml."

	running=$(docker ps -q --filter "label=com.docker.compose.project=$project") || stop "docker ps failed, so this can't tell whether Software Factory already runs."
	if [ -n "$running" ]; then
		echo "Software Factory already runs as the Compose project $project: its containers will be replaced with $version, and its volumes, with your projects, tokens, logins and reviews, kept."
	fi

	echo "Starting Software Factory $version, with Docker's socket $socket for the box helper (group $gid). Compose pulls the image if it is new."
	docker compose -p "$project" -f compose.yaml -f compose.release.yaml -f software-factory-socket.yaml up -d --wait --remove-orphans || stop "docker compose up failed; its messages are above, and docker compose -p $project logs shows each container's log."

	address=$(docker compose -p "$project" -f compose.yaml -f compose.release.yaml -f software-factory-socket.yaml port engine 8080 2>/dev/null) || address=
	case $address in
	127.0.0.1:*) ;;
	*) address="127.0.0.1:${SF_PORT:-7799}" ;;
	esac
	echo "Software Factory $version is installed in $home."
	echo "The engine now downloads the agents, which takes a few minutes; the page offers each one once it's installed."
	echo "Open http://$address"
}

main "$@"

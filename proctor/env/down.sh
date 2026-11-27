#!/bin/sh
set -eu

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
project=sf-proctor
state=${PROCTOR_SF_STATE:-/tmp/sf-proctor}
port=${PROCTOR_SF_PORT:-17799}
agents_volume=${project}_agents

say() { printf 'sf-proctor: %s\n' "$*" >&2; }

purge=0
for a in "$@"; do
	case "$a" in
		--purge) purge=1 ;;
		*)
			say "unknown argument $a; the only one is --purge"
			exit 2
			;;
	esac
done

case ":$PATH:" in *:/opt/homebrew/bin:*) ;; *) PATH="$PATH:/opt/homebrew/bin" ;; esac
if ! command -v docker >/dev/null 2>&1 && [ -x /Applications/Docker.app/Contents/Resources/bin/docker ]; then
	PATH="$PATH:/Applications/Docker.app/Contents/Resources/bin"
fi
export PATH

run_compose() {
	exec env -i PATH="$PATH" HOME="$HOME" ${TMPDIR:+TMPDIR="$TMPDIR"} ${USER:+USER="$USER"} ${LANG:+LANG="$LANG"} \
		${DOCKER_HOST:+DOCKER_HOST="$DOCKER_HOST"} ${DOCKER_CONTEXT:+DOCKER_CONTEXT="$DOCKER_CONTEXT"} \
		${DOCKER_CONFIG:+DOCKER_CONFIG="$DOCKER_CONFIG"} ${DOCKER_CERT_PATH:+DOCKER_CERT_PATH="$DOCKER_CERT_PATH"} \
		${DOCKER_TLS_VERIFY:+DOCKER_TLS_VERIFY="$DOCKER_TLS_VERIFY"} \
		COMPOSE_PROJECT_NAME="$project" SF_PORT="$port" SF_DOCKER_GID=0 \
		docker compose -p "$project" --project-directory "$repo" \
		-f "$repo/compose.yaml" -f "$repo/compose.dev.yaml" -f "$repo/proctor/environments/compose.proctor.yaml" "$@"
}
compose() { (run_compose "$@"); }

ours() {
	cmd=$(ps -o command= -p "$1" 2>/dev/null) || return 1
	case "$cmd" in *"docker compose -p $project "*) return 0 ;; esac
	return 1
}

stopped=0
if [ -d "$state" ] && [ ! -L "$state" ] && [ -f "$state/pids" ] && [ ! -L "$state/pids" ]; then
	while read -r name pid; do
		case "$pid" in '' | *[!0-9]*) continue ;; esac
		if ours "$pid"; then
			kill -TERM "$pid" 2>/dev/null || true
			n=0
			while kill -0 "$pid" 2>/dev/null && [ "$n" -lt 40 ]; do
				sleep 0.25
				n=$((n + 1))
			done
			kill -KILL "$pid" 2>/dev/null || true
			say "stopped $name ($pid)"
			stopped=$((stopped + 1))
		fi
	done <"$state/pids"
	rm -f "$state/pids"
fi

labelled() {
	{
		docker "$1" ls -q $2 --filter "label=com.docker.compose.project=$project" 2>/dev/null
		docker "$1" ls -q $2 --filter "label=dev.software-factory.stack=$project" 2>/dev/null
	} | sort -u
}

if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
	if [ -n "$(docker ps -aq --filter "label=com.docker.compose.project=$project" 2>/dev/null)" ]; then
		compose stop -t 60 helper >/dev/null 2>&1 || true
		compose down --remove-orphans -t 45 >/dev/null 2>&1 || true
		say "stopped Docker Compose project $project"
		stopped=$((stopped + 1))
	fi
	for c in $(labelled container -a); do
		docker rm -f "$c" >/dev/null 2>&1 && say "removed container $c"
	done
	for n in $(labelled network ""); do
		docker network rm "$n" >/dev/null 2>&1 && say "removed network $n"
	done
	for v in $(labelled volume ""); do
		if [ "$v" = "$agents_volume" ] && [ "$purge" -eq 0 ]; then
			continue
		fi
		docker volume rm "$v" >/dev/null 2>&1 && say "removed volume $v"
	done
fi

if [ -d "$state" ] && [ ! -L "$state" ]; then
	rm -f "$state/proctor.env" "$state/binding.yaml" "$state/auth-header" "$state/signin.json" "$state/signin.status" \
		"$state/answer.json" "$state/save-token.json" "$state/scrub-patterns"
fi

if [ "$stopped" -eq 0 ]; then
	say "nothing was running"
fi

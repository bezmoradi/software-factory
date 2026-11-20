#!/bin/sh
set -eu
umask 077

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
project=sf-proctor
state=${PROCTOR_SF_STATE:-/tmp/sf-proctor}
port=${PROCTOR_SF_PORT:-17799}
base=http://127.0.0.1:$port
origin=$base
demo_url=https://gitserver.test:8443/dev/demo.git
demo_path=/workspace/base/demo
box_subnets=10.213.15.240/28
gid=""

say() { printf 'sf-proctor: %s\n' "$*" >&2; }
fail() {
	say "$*"
	exit 2
}

case ":$PATH:" in *:/opt/homebrew/bin:*) ;; *) PATH="$PATH:/opt/homebrew/bin" ;; esac
if ! command -v docker >/dev/null 2>&1 && [ -x /Applications/Docker.app/Contents/Resources/bin/docker ]; then
	PATH="$PATH:/Applications/Docker.app/Contents/Resources/bin"
fi
export PATH

for tool in docker curl sed openssl lsof tr; do
	command -v "$tool" >/dev/null 2>&1 || fail "$tool is not installed or not on PATH"
done

case "$port" in
	'' | *[!0-9]*) fail "PROCTOR_SF_PORT must be a port number, got $port" ;;
esac
[ "$port" -ge 1024 ] && [ "$port" -le 65535 ] || fail "PROCTOR_SF_PORT must be between 1024 and 65535, got $port"
case "$state" in
	/*) ;;
	*) fail "PROCTOR_SF_STATE must be an absolute path, got $state" ;;
esac
case "$state" in
	"$repo" | "$repo"/*) fail "PROCTOR_SF_STATE must be outside the repository, so the key and the binding never enter it" ;;
	*'"'* | *'\'*) fail "PROCTOR_SF_STATE holds a quote or backslash, which the binding can't hold: $state" ;;
esac

run_compose() {
	exec env -i PATH="$PATH" HOME="$HOME" ${TMPDIR:+TMPDIR="$TMPDIR"} ${USER:+USER="$USER"} ${LANG:+LANG="$LANG"} \
		${DOCKER_HOST:+DOCKER_HOST="$DOCKER_HOST"} ${DOCKER_CONTEXT:+DOCKER_CONTEXT="$DOCKER_CONTEXT"} \
		${DOCKER_CONFIG:+DOCKER_CONFIG="$DOCKER_CONFIG"} ${DOCKER_CERT_PATH:+DOCKER_CERT_PATH="$DOCKER_CERT_PATH"} \
		${DOCKER_TLS_VERIFY:+DOCKER_TLS_VERIFY="$DOCKER_TLS_VERIFY"} \
		COMPOSE_PROJECT_NAME="$project" SF_PORT="$port" SF_DOCKER_GID="$gid" SF_NO_UPDATE_CHECK=1 SF_OSV=off \
		SF_BOX_SUBNETS="$box_subnets" \
		${SF_BOX_SWEEP_DRY:+SF_BOX_SWEEP_DRY="$SF_BOX_SWEEP_DRY"} \
		docker compose -p "$project" --project-directory "$repo" \
		-f "$repo/compose.yaml" -f "$repo/compose.dev.yaml" -f "$here/compose.proctor.yaml" "$@"
}
compose() { (run_compose "$@"); }

docker info >/dev/null 2>&1 || fail "Docker isn't running, or this user can't reach it"

docker_host=${DOCKER_HOST:-$(docker context inspect --format '{{.Endpoints.docker.Host}}' 2>/dev/null || true)}
case "$docker_host" in
	unix:///*) ;;
	*) fail "Proctor's docker driver reads Docker over a Unix socket, and DOCKER_HOST or the current Docker context names ${docker_host:-nothing}" ;;
esac
docker_socket=${docker_host#unix://}
case "$docker_socket" in
	*'"'* | *'\'*) fail "the path of Docker's socket holds a quote or backslash: $docker_socket" ;;
esac
[ -S "$docker_socket" ] || fail "$docker_socket isn't a socket, so Proctor's docker driver can't read Docker there"

for other in software-factory software-factory-dev; do
	if [ -n "$(docker ps -q --filter "label=com.docker.compose.project=$other" 2>/dev/null)" ] ||
		[ -n "$(docker ps -q --filter "label=dev.software-factory.stack=$other" 2>/dev/null)" ]; then
		fail "the Compose project $other is running; stop it first (docker compose -p $other down; its volumes stay), since live checks run with your own stack stopped"
	fi
done
if [ -n "$(docker ps -aq --filter "label=com.docker.compose.project=$project" 2>/dev/null)" ] ||
	[ -n "$(docker ps -aq --filter "label=dev.software-factory.stack=$project" 2>/dev/null)" ]; then
	fail "the Compose project $project has containers already; run $here/down.sh first"
fi
if lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1; then
	fail "port $port is already in use"
fi

[ -L "$state" ] && fail "$state is a symbolic link; refusing to use it"
mkdir -p "$state"
[ "$(ls -ldn "$state" | awk '{print $3}')" = "$(id -u)" ] || fail "$state is not owned by $(id -un)"
chmod 700 "$state"
if [ -s "$state/pids" ]; then
	fail "an environment is already recorded in $state; run $here/down.sh first"
fi
rm -f "$state"/*.log "$state/pids" "$state/report.json" "$state/proctor.env" "$state/binding.yaml" "$state/auth-header" \
	"$state/signin.json" "$state/signin.status" "$state/answer.json" "$state/save-token.json" "$state/scrub-patterns"
: >"$state/pids"

ok=0
on_exit() {
	if [ "$ok" -ne 1 ]; then
		say "start failed; stopping what was started (logs stay in $state)"
		"$here/down.sh" >/dev/null 2>&1 || true
	fi
}
trap on_exit EXIT

say "building the images software-factory-proctor:{dev,local,gitserver} (log: $state/build.log)"
compose build >"$state/build.log" 2>&1 || {
	tail -20 "$state/build.log" >&2
	fail "docker compose build failed"
}

gid=${SF_DOCKER_GID:-}
if [ -z "$gid" ]; then
	gid=$(docker run --rm --network none --read-only --cap-drop ALL --security-opt no-new-privileges:true \
		-v "${SF_DOCKER_SOCKET:-/var/run/docker.sock}:/s:ro" --entrypoint stat software-factory-proctor:dev -c %g /s) ||
		fail "couldn't read the group that owns Docker's socket"
fi
case "$gid" in
	'' | *[!0-9]*) fail "the group of Docker's socket isn't a number: $gid" ;;
esac
say "SF_DOCKER_GID=$gid, the group that owns Docker's socket as containers see it"

say "starting the development overlay as Compose project $project on $base"
compose up -d --wait --remove-orphans >"$state/up.log" 2>&1 || {
	tail -20 "$state/up.log" >&2
	fail "docker compose up failed"
}
(run_compose logs -f --no-color --no-log-prefix engine) >"$state/engine.log" 2>&1 </dev/null &
echo "engine-logs $!" >>"$state/pids"

curl -fsS -o /dev/null "$base/healthz" || fail "$base/healthz didn't answer"

sign_in() {
	curl -sS -o "$state/signin.json" -w '%{http_code}' -X POST -H 'Content-Type: application/json' -H "Origin: $origin" \
		--data '{}' "$base/api/session" >"$state/signin.status" || fail "the sign-in request failed"
	signed=$(sed -nE 's/^\{"token":"([A-Za-z0-9_-]{43})"\}$/\1/p' "$state/signin.json")
	rm -f "$state/signin.json"
	[ "$(cat "$state/signin.status")" = 200 ] && [ -n "$signed" ] || fail "the page didn't sign in (status $(cat "$state/signin.status"))"
	rm -f "$state/signin.status"
}

say "signing in to the page twice, so the first key is one a newer sign-in replaced"
sign_in
replaced=$signed
sign_in
key=$signed
signed=""
[ "$replaced" != "$key" ] || fail "the second sign-in gave the first key again"
printf 'Authorization: Bearer %s\n' "$key" >"$state/auth-header"

post() {
	curl -sS -o "$state/answer.json" -w '%{http_code}' -X POST -H @"$state/auth-header" -H "Origin: $origin" \
		-H 'Content-Type: application/json' --data "$2" "$base$1"
}
expect() {
	got=$(post "$1" "$2") || fail "POST $1 failed"
	[ "$got" = "$3" ] || {
		say "POST $1 answered $got, not $3:"
		sed -n 1p "$state/answer.json" | cut -c1-400 >&2
		fail "the environment couldn't be prepared"
	}
}

gitserver=$(compose ps -q gitserver)
[ -n "$gitserver" ] || fail "the dev git host isn't running"
git_token=$(docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$gitserver" | sed -n 's/^GITSERVER_TOKEN=//p')
case "$git_token" in
	'' | *[!A-Za-z0-9._-]*) fail "the dev git host's token is missing or has characters this script doesn't quote" ;;
esac

say "preparing: a commit identity, the dev git host's token, and $demo_url"
expect /api/commit-identity '{"op":"set","name":"Proctor Run","email":"12345+proctor@users.noreply.github.com"}' 200
printf '{"host":"gitserver.test:8443","token":"%s"}' "$git_token" >"$state/save-token.json"
expect /api/save-token @"$state/save-token.json" 200
rm -f "$state/save-token.json"
expect /api/add-repo "{\"url\":\"$demo_url\"}" 202
waited=0
until [ "$(post /api/projects '{}')" = 200 ] && grep -qF "\"path\":\"$demo_path\"" "$state/answer.json"; do
	[ "$waited" -lt 120 ] || fail "the clone of $demo_url didn't finish within 120 seconds"
	waited=$((waited + 1))
	sleep 1
done
rm -f "$state/answer.json"

unknown=$(openssl rand -base64 32 | tr '+/' '-_' | tr -d '=\n')
basic="Basic $key"
token="Token $key"
[ ${#unknown} -eq 43 ] && [ "$unknown" != "$key" ] && [ "$unknown" != "$replaced" ] || fail "couldn't make an unknown key"
for v in "$key" "$replaced" "$unknown" "$basic" "$token" "$git_token"; do
	case "$v" in *"'"* | *'\'*) fail "a generated value holds a quote or backslash" ;; esac
done

{
	printf "export PROCTOR_SF_PAGE_KEY='%s'\n" "$key"
	printf "export PROCTOR_SF_UNKNOWN_KEY='%s'\n" "$unknown"
	printf "export PROCTOR_SF_REPLACED_KEY='%s'\n" "$replaced"
	printf "export PROCTOR_SF_BASIC_AUTH='%s'\n" "$basic"
	printf "export PROCTOR_SF_TOKEN_SCHEME_AUTH='%s'\n" "$token"
	printf "export PROCTOR_SF_NO_SCHEME_AUTH='%s'\n" "$key"
	printf "export PROCTOR_SF_LOWERCASE_AUTH='bearer %s'\n" "$key"
	printf "export PROCTOR_SF_GIT_TOKEN='%s'\n" "$git_token"
} >"$state/proctor.env"
printf '%s\n' "$key" "$replaced" "$unknown" "$basic" "$token" "$git_token" >"$state/scrub-patterns"

{
	printf 'environment: local\n'
	printf 'drivers:\n'
	printf '  http:\n'
	printf '    base_url: "%s"\n' "$base"
	printf '    user_agent: "proctor-software-factory/1"\n'
	printf '    ready_path: "/healthz"\n'
	printf '  http_misdirected:\n'
	printf '    base_url: "http://localhost:%s"\n' "$port"
	printf '    user_agent: "proctor-software-factory/1"\n'
	printf '    ready_path: "/healthz"\n'
	printf '  docker:\n'
	printf '    socket: "%s"\n' "$docker_socket"
	printf 'identities:\n'
	printf '  page:\n'
	printf '    token_env: PROCTOR_SF_PAGE_KEY\n'
	printf '    values:\n'
	printf '      origin: "%s"\n' "$origin"
	printf '      port: "%s"\n' "$port"
	printf '    variants:\n'
	printf '      unknown: { token_env: PROCTOR_SF_UNKNOWN_KEY }\n'
	printf '      replaced: { token_env: PROCTOR_SF_REPLACED_KEY }\n'
	printf '      basic_scheme: { header_env: PROCTOR_SF_BASIC_AUTH }\n'
	printf '      token_scheme: { header_env: PROCTOR_SF_TOKEN_SCHEME_AUTH }\n'
	printf '      no_scheme: { header_env: PROCTOR_SF_NO_SCHEME_AUTH }\n'
	printf '      lowercase_scheme: { header_env: PROCTOR_SF_LOWERCASE_AUTH }\n'
	printf 'secrets:\n'
	printf '  git_token: { env: PROCTOR_SF_GIT_TOKEN }\n'
	printf 'app_logs:\n'
	printf '  file: "%s"\n' "$state/engine.log"
} >"$state/binding.yaml"
chmod 600 "$state/proctor.env" "$state/binding.yaml" "$state/auth-header" "$state/scrub-patterns"

ok=1
say "ready: the page on $base, state in $state"

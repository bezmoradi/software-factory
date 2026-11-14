#!/bin/sh
set -eu
usage='usage: scripts/release-check.sh <image@sha256:digest> <file of docker compose config --format json>'
[ $# = 2 ] || {
	echo "$usage" >&2
	exit 2
}
image=$1
config=$2
printf '%s\n' "$image" | grep -Eqx '[a-z0-9][a-z0-9._-]*(:[0-9]+)?(/[a-z0-9][a-z0-9._-]*)+@sha256:[0-9a-f]{64}' || {
	echo "release-check: not an image name with a sha256 digest: $image" >&2
	exit 2
}
[ -f "$config" ] || {
	echo "release-check: no such file: $config" >&2
	exit 2
}
problems=$(jq -r --arg image "$image" '
  def services: ["egress", "engine", "helper", "reviewer"];
  def users: {"egress": "egress", "engine": "engine", "helper": "10005:10005", "reviewer": "reviewer"};
  def servicekeys: ["cap_drop", "command", "depends_on", "entrypoint", "environment", "healthcheck", "image", "init", "logging", "mem_limit", "networks", "pids_limit", "ports", "pull_policy", "read_only", "restart", "security_opt", "stop_grace_period", "tmpfs", "user", "volumes"];
  def topkeys: ["name", "networks", "services", "volumes"];
  def bindopt: (.o // "" | tostring | split(",") | map(gsub("^\\s+|\\s+$"; "")) | any(. == "bind" or . == "rbind"));
  def among($list): . as $k | $list | any(.[]; . == $k);
  def tmpfsopts: (.type == "tmpfs" and .device == "tmpfs" and (bindopt | not));
  (.services // {}) as $s
  | (keys[] | select((among(topkeys) | not) and (startswith("x-") | not)) | "the configuration has a top-level \(.) section"),
    (if ($s | keys) != services then "the services are \($s | keys | join(", ")), not exactly \(services | join(", "))" else empty end),
    ($s | to_entries[] | .key as $n | .value
      | (keys[] | select(among(servicekeys) | not) | "\($n) sets \(.), which the release allows no service"),
        (if .image != $image then "\($n) runs \(.image // "no image"), not \($image)" else empty end),
        (if .pull_policy != "missing" then "\($n) has pull_policy \(.pull_policy // "unset"), not missing" else empty end),
        (if .cap_drop != ["ALL"] then "\($n) has cap_drop \(.cap_drop // [] | tojson), not [\"ALL\"]" else empty end),
        (if .security_opt != ["no-new-privileges:true"] then "\($n) has security_opt \(.security_opt // [] | tojson), not [\"no-new-privileges:true\"]" else empty end),
        (if .read_only != true then "\($n) has read_only \(.read_only // "unset"), not true" else empty end),
        (if .user != users[$n] then "\($n) runs as \(.user // "the image user"), not \(users[$n] // "a user the release allows")" else empty end),
        ((.ports // [])[] | select(.host_ip != "127.0.0.1") | "\($n) publishes port \(.published // "?") on \(.host_ip // "every address"), not 127.0.0.1"),
        ((.volumes // [])[] | select(.type != "volume" and .type != "tmpfs") | "\($n) mounts \(.source // "?") at \(.target // "?") as \(.type // "?"), and only volumes and tmpfs are allowed"),
        ((.depends_on // {}) | keys[] | select(among(services) | not) | "\($n) depends on \(.)")),
    (if ($s.helper.networks // {} | keys) != ["helpernet"] then "the helper is on \($s.helper.networks // {} | keys | join(", ")), not on helpernet alone" else empty end),
    ((.networks // {}) | to_entries[] | .key as $w | .value
      | (keys[] | select(among(["name", "ipam", "internal", "driver"]) | not) | "the network \($w) sets \(.)"),
        (if (.driver // "bridge") != "bridge" then "the network \($w) has the driver \(.driver), not bridge" else empty end),
        (if (.ipam // {}) != {} then "the network \($w) sets ipam" else empty end)),
    ((.volumes // {}) | to_entries[] | .key as $v | .value
      | (keys[] | select(among(["name", "driver", "driver_opts"]) | not) | "the volume \($v) sets \(.)"),
        (if (.driver // "local") != "local" then "the volume \($v) has the driver \(.driver), not local" else empty end),
        (.driver_opts // empty | select(bindopt or ((has("device") or has("type")) and (tmpfsopts | not)) or (keys - ["type", "device", "o"] | length > 0)) | "the volume \($v) has driver_opts other than a tmpfs")),
    (if [.. | (strings, (objects | keys[]))] | any(test("SF_BOX_LIVE")) then "SF_BOX_LIVE appears in the configuration" else empty end)
' "$config")
if [ -n "$problems" ]; then
	printf '%s\n' "$problems" | sed 's/^/release-check: /' >&2
	exit 1
fi
echo "release-check: $config runs $image in exactly egress, engine, helper and reviewer, every one with only the keys, user and hardening the release allows, only volume and tmpfs mounts, ports on 127.0.0.1 only, default networks and volumes, no SF_BOX_LIVE and the helper on helpernet alone"

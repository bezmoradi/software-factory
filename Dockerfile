# syntax=docker/dockerfile:1.27@sha256:4edf897a3ffa55b89f906fc8cc78afdb3f1834cc9c7083565e611a8a7d5fe99e

FROM --platform=$BUILDPLATFORM node:26.13.1-trixie-slim@sha256:e6bedef217677b276c591795e169840edd9096dbf13f29df8ef8d1856b8820bc AS page
WORKDIR /src
COPY web/package.json web/package-lock.json web/
RUN npm ci --prefix web --ignore-scripts --no-audit --no-fund
COPY web/ web/
RUN mkdir -p internal/adapters/http/page/dist && npm --prefix web run build

FROM --platform=$BUILDPLATFORM golang:1.27.4-trixie@sha256:b54b2639de8c1bfe538ec5ffc45f29b1932235ac5f6d63c059853646677112d9 AS go
SHELL ["/bin/bash", "-eo", "pipefail", "-c"]
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY configs/*.go configs/
COPY internal/ internal/
COPY tools/ tools/
COPY --from=page /src/internal/adapters/http/page/dist/ internal/adapters/http/page/dist/
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/software-factory ./cmd
RUN mkdir -p /out/licenses/go && cp "$(go env GOROOT)/LICENSE" /out/licenses/go/LICENSE \
 && go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}}@{{.Version}} {{.Dir}}{{end}}{{end}}' ./cmd | sort -u \
  | while read -r mod dir; do \
      dest="/out/licenses/$(echo "$mod" | tr '/@' '__')"; mkdir -p "$dest"; \
      found=0; for f in "$dir"/LICENSE* "$dir"/LICENCE* "$dir"/NOTICE* "$dir"/COPYING* "$dir"/PATENTS*; do \
        [ -f "$f" ] && cp "$f" "$dest/" && found=1; done; \
      [ "$found" = 1 ] || { echo "no license file for $mod" >&2; exit 1; }; \
    done

FROM --platform=$BUILDPLATFORM golang:1.27.4-trixie@sha256:b54b2639de8c1bfe538ec5ffc45f29b1932235ac5f6d63c059853646677112d9 AS fakes
WORKDIR /src
COPY go.mod go.sum ./
COPY dev/fakeacp/ fakeacp/
COPY dev/fakeclaude/ fakeclaude/
COPY dev/gitserver/ gitserver/
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/sf-fake-acp ./fakeacp \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/sf-fake-claude ./fakeclaude \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/sf-gitserver ./gitserver

FROM node:26.13.1-trixie-slim@sha256:e6bedef217677b276c591795e169840edd9096dbf13f29df8ef8d1856b8820bc AS base
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl git netcat-openbsd openssh-client rsync sshpass \
 && rm -rf /var/lib/apt/lists/* \
 && rm -rf /usr/local/lib/node_modules /usr/local/bin/npm /usr/local/bin/npx /usr/local/bin/corepack /usr/local/bin/yarn /usr/local/bin/yarnpkg /usr/local/bin/docker-entrypoint.sh /opt/yarn-* \
 && userdel --remove node \
 && groupadd --gid 10000 sf \
 && chgrp sf /usr/bin/nc.openbsd /usr/bin/rsync /usr/bin/scp /usr/bin/sftp /usr/bin/ssh /usr/bin/sshpass \
 && chmod 0750 /usr/bin/nc.openbsd /usr/bin/rsync /usr/bin/scp /usr/bin/sftp /usr/bin/ssh /usr/bin/sshpass \
 && install -d -m 0755 /etc/ssh/ssh_config.d \
 && printf '%s\n' 'IdentityAgent /run/sf/ssh-agent.sock' 'ForwardAgent no' > /etc/ssh/ssh_config.d/software-factory.conf \
 && chmod 0644 /etc/ssh/ssh_config.d/software-factory.conf \
 && useradd --uid 10001 --gid sf --no-create-home --home-dir /var/lib/software-factory --shell /usr/sbin/nologin engine \
 && useradd --uid 10002 --gid sf --no-create-home --home-dir /home/agent --shell /bin/bash agent \
 && groupadd --gid 10003 egress \
 && useradd --uid 10003 --gid egress --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin egress \
 && useradd --uid 10004 --user-group --no-create-home --home-dir /home/reviewer --shell /bin/bash reviewer \
 && useradd --uid 10005 --user-group --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin helper \
 && useradd --uid 10007 --user-group --no-create-home --home-dir /home/action --shell /usr/sbin/nologin action \
 && install -d -o engine -g sf -m 0700 /var/lib/software-factory \
 && install -d -o engine -g sf -m 2775 /workspace /workspace/repos /workspace/worktrees \
 && install -d -o engine -g sf -m 0755 /workspace/s \
 && install -d -o engine -g sf -m 0755 /opt/software-factory/agents /opt/software-factory/agents/bin \
 && install -d -o agent -g sf -m 0700 /home/agent /run/sf \
 && install -d -o reviewer -g reviewer -m 0700 /home/reviewer \
 && install -d -o engine -g sf -m 0755 /reviews \
 && install -d -o engine -g sf -m 0755 /verify \
 && install -d -o engine -g sf -m 0710 /actions \
 && install -d -o action -g action -m 0700 /home/action \
 && install -d -m 0755 /usr/share/doc/software-factory/node
COPY --from=go /out/software-factory /usr/local/bin/software-factory
COPY --chown=root:root --chmod=a=rX,u+w plugin/.claude-plugin/plugin.json /opt/software-factory/plugin/.claude-plugin/plugin.json
COPY --chown=root:root --chmod=a=rX,u+w plugin/hooks/ /opt/software-factory/plugin/hooks/
COPY --chown=root:root --chmod=a=rX,u+w configs/claude-code/managed-settings.json /etc/claude-code/managed-settings.json
COPY --from=go /out/licenses/ /usr/share/doc/software-factory/go/
COPY --from=page /src/web/node_modules/typescript/LICENSE /src/web/node_modules/typescript/NOTICE.txt /usr/share/doc/software-factory/typescript/
COPY LICENSE /usr/share/doc/software-factory/LICENSE
COPY --chmod=0644 third_party/node/LICENSE /usr/share/doc/software-factory/node/LICENSE
ENV PATH=/opt/software-factory/agents/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    SF_STATE_DIR=/var/lib/software-factory \
    SF_WORKSPACE=/workspace \
    SF_AGENTS_DIR=/opt/software-factory/agents \
    SF_PLUGIN_DIR=/opt/software-factory/plugin
WORKDIR /
USER engine
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/software-factory"]

FROM base AS dev
USER root
RUN install -d -m 0755 /usr/share/software-factory \
 && install -m 0444 /dev/null /usr/share/software-factory/dev-image
USER engine
COPY --from=fakes /out/sf-fake-acp /usr/local/bin/sf-fake-acp
COPY --from=fakes /out/sf-fake-claude /usr/local/bin/sf-fake-claude

FROM base AS gitserver
USER root
RUN userdel reviewer \
 && useradd --uid 10004 --user-group --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin gitsrv \
 && install -d -o gitsrv -g gitsrv -m 0755 /srv/git /srv/ca
COPY --from=fakes /out/sf-gitserver /usr/local/bin/sf-gitserver
USER gitsrv
EXPOSE 8443
ENTRYPOINT ["/usr/local/bin/sf-gitserver"]

FROM base AS browser
USER root
RUN sed -i \
      -e 's#http://deb.debian.org/debian-security#https://snapshot.debian.org/archive/debian-security/20261103T013555Z#' \
      -e 's#http://deb.debian.org/debian#https://snapshot.debian.org/archive/debian/20261103T013555Z#' \
      /etc/apt/sources.list.d/debian.sources \
 && ! grep -q 'deb.debian.org' /etc/apt/sources.list.d/debian.sources \
 && apt-get -o Acquire::Check-Valid-Until=false update \
 && apt-get install -y --no-install-recommends \
      chromium=156.0.8078.79-1~deb13u1 \
      chromium-common=156.0.8078.79-1~deb13u1 \
      fonts-liberation=1:2.1.5-3 \
 && rm -rf /var/lib/apt/lists/* \
 && install -d -m 0755 /etc/chromium.d \
 && printf '%s\n' 'export CHROMIUM_FLAGS="$CHROMIUM_FLAGS --no-sandbox --disable-dev-shm-usage"' > /etc/chromium.d/software-factory \
 && chmod 0644 /etc/chromium.d/software-factory \
 && install -d -o 10006 -g 10006 -m 0700 /out
USER engine

FROM browser AS dev-browser
COPY --from=fakes /out/sf-fake-acp /usr/local/bin/sf-fake-acp
COPY --from=fakes /out/sf-fake-claude /usr/local/bin/sf-fake-claude

FROM base AS runtime

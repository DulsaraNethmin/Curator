# syntax=docker/dockerfile:1
#
# curator, as one image: the binary, the UI it embeds, and the minimal ffmpeg the
# remux runs. Nothing else — the final stage is `FROM scratch`, so there is no
# shell, no package manager and no libc in it (docs/tasks/T47-image.md).
#
#   docker buildx build --platform linux/amd64,linux/arm64 -t curator .
#   docker run -p 8090:8090 -v curator-data:/data -v curator-media:/media curator
#
# Four build stages and a final one that is `FROM scratch`. Three of the four run
# on the BUILD platform and cross-compile, which is why an arm64 Mac produces an
# amd64 image without emulating one; only `ffmpeg` is native to the target,
# because it is a C build and there is no cross-toolchain worth carrying for it.

# Pinned, all three, and ffmpeg's version and tarball checksum are pinned in
# scripts/ffmpeg.sh. "The same command works next month" is the promise this
# whole phase is selling, and a floating base image is the cheapest way to break
# it.
ARG GO_VERSION=1.25.4
ARG NODE_VERSION=22.21.1
ARG ALPINE_VERSION=3.21

# --- the UI ---------------------------------------------------------------
#
# First, because the binary embeds its output and a Go build that runs before it
# produces a binary that serves a "run npm" placeholder page, says so once in the
# log, and is otherwise perfect (docs/decisions.md D16).
FROM --platform=$BUILDPLATFORM node:${NODE_VERSION}-alpine${ALPINE_VERSION} AS ui
WORKDIR /src/web

# The lockfile alone first, so a source-only change does not reinstall 400 MB of
# node_modules. `npm ci` and not `npm install`: it installs the lockfile exactly
# and fails if package.json disagrees, which is what a reproducible image needs.
COPY web/package.json web/package-lock.json ./
RUN npm ci

COPY web/ ./
# `next build && node scripts/embed.mjs`. The export lands in
# /src/internal/web/dist, outside this stage's WORKDIR, because go:embed patterns
# cannot leave their own package directory.
RUN npm run build

# --- the binary -----------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine${ALPINE_VERSION} AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/
# From the stage above rather than from the build context, which .dockerignore
# excludes for exactly this reason: the image's UI is always the one built from
# the source in the image.
COPY --from=ui /src/internal/web/dist/ ./internal/web/dist/

ARG TARGETARCH
# CGO_ENABLED=0, written out, and not left to the cross-compiler to imply.
# With cgo on, the torrent engine pulls go-libutp, go-llsqlite/crawshaw and
# crawshaw/c — so the image would carry a cgo uTP *and a second SQLite* beside
# the pure-Go one (docs/decisions.md D4). Cross-compiling turns cgo off by
# itself, which is precisely why `make check`'s arm64 step has never caught this:
# it is invisible everywhere except in an image built for its own platform.
ENV CGO_ENABLED=0 GOOS=linux
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOARCH=${TARGETARCH} go build -trimpath -ldflags "-s -w" -o /out/curator ./cmd/curator

# --- ffmpeg ---------------------------------------------------------------
#
# The one stage that is NOT $BUILDPLATFORM: this compiles C for the machine the
# image will run on, so building an amd64 image on an arm64 Mac emulates one.
# Slow, and correct — shipping one architecture's ffmpeg to both is a remux that
# dies with `exec format error` on half the installs, and it dies at play time
# rather than at build time.
#
# Built from source rather than fetched prebuilt because the prebuilt static
# builds are 80-100 MB general-purpose ffmpegs and this one is 2.2 MB: it has one
# muxer, three demuxers, two protocols and no encoder at all. The flags live in
# scripts/ffmpeg.sh, which scripts/build-ffmpeg.sh also runs, so there is one
# copy of them.
FROM alpine:${ALPINE_VERSION} AS ffmpeg
RUN apk add --no-cache build-base coreutils yasm nasm pkgconf perl tar xz
COPY scripts/ffmpeg.sh /ffmpeg.sh
RUN /ffmpeg.sh /out

# --- what a scratch image still needs -------------------------------------
#
# Assembled in a stage with a shell because the final one has none. This is also
# where the runtime directories get their ownership: Docker copies the contents
# AND the ownership of a mount point out of the image when it first populates an
# empty named volume, so /data and /media arriving here owned by 1000:1000 is
# what makes `docker compose up -d` work on a fresh install without a chown
# anywhere — see the note on PUID/PGID at the bottom of this file.
FROM --platform=$BUILDPLATFORM alpine:${ALPINE_VERSION} AS rootfs
RUN apk add --no-cache ca-certificates

# ca-certificates.crt: TMDB, YTS, TPB, 1337x and Jellyfin are all HTTPS, and
# without this file the image starts, serves the UI, and cannot fetch a single
# piece of metadata — a working curator that matches nothing.
#
# passwd and group: enough for a numeric uid to be a legal user. Nothing in
# curator looks one up, but a container whose `id` answers nothing is a
# debugging session somebody else has to have.
#
# /tmp: 1777, and empty. Only the engine writes a temporary file and it writes it
# beside its target rather than here, so this exists for the day something in the
# standard library reaches for os.TempDir and finds no directory at all.
#
# /data/README: not decoration, and not optional. Measured — Docker copies a
# mount point's contents AND ownership out of the image every time it mounts a
# named volume that is EMPTY, and stops the moment the volume has one entry in
# it. /media ships two subdirectories, so it is non-empty after the first mount
# and a `chown` on it survives. /data shipped as a bare directory would stay
# empty until curator writes, so a `chown` made before the first start is silently
# undone on the next one — which is exactly what somebody running PUID=1026 on a
# NAS would do first. One file removes the asymmetry, and it may as well be the
# file that explains it.
RUN set -eu; \
    mkdir -p /rootfs/etc/ssl/certs /rootfs/data /rootfs/media/movies /rootfs/media/downloads /rootfs/tmp; \
    cp /etc/ssl/certs/ca-certificates.crt /rootfs/etc/ssl/certs/; \
    echo 'curator:x:1000:1000:curator:/data:/sbin/nologin' > /rootfs/etc/passwd; \
    echo 'curator:x:1000:' > /rootfs/etc/group; \
    printf '%s\n' \
      'curator keeps two things here, and losing either one costs a library:' \
      '' \
      '  curator.db   the movies, the downloads and the settings' \
      '  curator.key  the key those settings are encrypted with. A database' \
      '               restored without it keeps every row and cannot read one' \
      '               secret back — the API key and the WireGuard private key' \
      '               have to be typed again. Back them up together.' \
      '' \
      'curator.db-wal is part of the database and is often newer than the .db.' \
      'Copying the .db alone captures a stale snapshot that answers plausibly.' \
      '' \
      'This image runs as uid 1000. To run as another, PUID/PGID (or --user)' \
      'set it, and BOTH volumes then have to belong to that uid:' \
      '' \
      '  docker compose up -d          # once, as the default uid' \
      '  docker compose down' \
      '  docker run --rm -v curator-data:/data -v curator-media:/media alpine chown -R 1001:1001 /data /media' \
      '  PUID=1001 PGID=1001 docker compose up -d' \
      '' \
      'That first start is not skippable. Docker rewrites the ownership of an' \
      'EMPTY named volume from the image on every mount, so a chown made before' \
      'it is silently undone. This file is what makes /data non-empty, and' \
      'therefore what makes the chown stick.' \
      > /rootfs/data/README; \
    chown -R 1000:1000 /rootfs/data /rootfs/media; \
    chmod 1777 /rootfs/tmp

# --- the image ------------------------------------------------------------
FROM scratch

COPY --from=rootfs /rootfs/ /
COPY --from=ffmpeg /out/ffmpeg /usr/local/bin/ffmpeg
COPY --from=build /out/curator /curator

# Defaults for the container, not for the repo. LIBRARY_MOVIES matters most: its
# default is ./testdata/library/movies, which is right for a fresh clone and
# would make a stranger's first scan report 29 films they do not have.
#
# /media/movies and /media/downloads are one directory apart on purpose. The
# importer hardlinks and falls back to a copy on EXDEV (docs/decisions.md D8), so
# the two have to be on one filesystem; putting them inside a single mount point
# makes that structural instead of a sentence in a README.
#
# FFMPEG_PATH is set rather than left to a PATH lookup: there is no PATH in a
# scratch image, so exec.LookPath would answer "not found" and the remux would be
# quietly absent — which is a supported state, and therefore not one that reports
# itself as a mistake.
ENV FFMPEG_PATH=/usr/local/bin/ffmpeg \
    DB_PATH=/data/curator.db \
    LIBRARY_MOVIES=/media/movies \
    DOWNLOADS_DIR=/media/downloads

# Never root. curator mounts the household's media and ships with authentication
# off by default (docs/decisions.md D25, D27), and a root default in that posture
# is the one this project has spent two phases avoiding.
#
# PUID/PGID are how this is overridden, and there is no entrypoint applying them
# because there is no shell to run one in: `user: "${PUID:-1000}:${PGID:-1000}"`
# in compose, or `--user` on docker run, and Docker sets the uid before the
# process exists. No step of that runs as root, which is the half of the
# linuxserver.io convention worth keeping — and it is why `chown -R` never runs
# across somebody's media directory.
#
# The cost is real and belongs written down rather than discovered: nothing in
# here can fix up permissions, so a uid that is not 1000 has to be given
# directories it owns — BOTH of them, measured, because /media fails second and
# with a different message once /data is fixed. A bind mount already is one, and
# that is the case PUID exists for: the host directory's owner is what PUID has
# to equal. A named volume is not — it comes up owned by 1000:1000, copied from
# this image, and uid 1001 then fails with `unable to open database file (14)`,
# which names nothing. The fix is one chown after the first start, and
# /data/README carries the four lines that do it.
USER 1000:1000

EXPOSE 8090

# Declared, in the knowledge that it forces an anonymous volume on anyone who
# runs this without `-v`. That is the point: the alternative is a household's
# database and its encryption key living in the container's writable layer, where
# `docker rm` destroys them with no warning and nothing to recover. An orphaned
# anonymous volume is a mess; a `docker volume ls` finds it.
VOLUME ["/data", "/media"]

# `curator -healthcheck` and not curl, which is not in the image and is not going
# to be. Compose needs this for `depends_on: condition: service_healthy`; it is
# not the mechanism T65 polls Jellyfin with, which is a different question asked
# of a different container.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/curator", "-healthcheck"]

ENTRYPOINT ["/curator"]

# Three labels, and deliberately not a version one. cmd/curator/version.go is the
# repo's single copy of that string, and a `version` label here would be a second
# that drifts on the first release nobody remembers to edit twice. T48 can pass
# one with --label at build time, from the same source.
LABEL org.opencontainers.image.title="curator" \
      org.opencontainers.image.description="One binary that finds, downloads, files and plays films — replacing the *arr stack." \
      org.opencontainers.image.source="https://github.com/DulsaraNethmin/Curator"

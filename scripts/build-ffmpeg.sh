#!/usr/bin/env bash
#
# Build the minimal ffmpeg curator ships, standalone, and report its size.
#
# **The size is the point.** Budget 20 MB, reconsider past 40 MB, and past that
# the answer is to ship direct play alone (docs/tasks/T44-remux.md). The measured
# number goes into docs/progress.md, because phase 9's image budget is built on
# it.
#
# The build itself — the configure flags and the pinned version — lives in
# scripts/ffmpeg.sh, because since T47 the Dockerfile's `ffmpeg` stage runs the
# same file. This script is the wrapper that exists to answer "how big is it",
# which a docker build does not tell you and an image layer hides.
#
# Docker, because the target is linux/arm64 and this is written on a Mac. On an
# Apple Silicon machine that is a native build with no emulation; elsewhere it
# is qemu and slow but correct.
#
#   ./scripts/build-ffmpeg.sh            # build and report
#   ./scripts/build-ffmpeg.sh ./out      # and copy the binary out
set -euo pipefail

VERSION="${FFMPEG_VERSION:-8.1.1}"
PLATFORM="${FFMPEG_PLATFORM:-linux/arm64}"
TAG="curator-ffmpeg:${VERSION}"
OUT="${1:-}"

cd "$(dirname "${BASH_SOURCE[0]}")/.."

dockerfile() {
  cat <<DOCKERFILE
FROM alpine:3.21 AS build
RUN apk add --no-cache build-base coreutils yasm nasm pkgconf perl tar xz
COPY scripts/ffmpeg.sh /ffmpeg.sh
ENV FFMPEG_VERSION=${VERSION}
RUN /ffmpeg.sh /out
FROM scratch
COPY --from=build /out/ffmpeg /ffmpeg
DOCKERFILE
}

echo "building ffmpeg ${VERSION} for ${PLATFORM} — minimal, no encoders"
tmp=$(mktemp -d)
trap 'rm -rf "${tmp}"' EXIT

# --output rather than `docker create` and `docker cp`: the final stage is FROM
# scratch and has no command in it, so there is no container to create.
dockerfile | docker build --platform "${PLATFORM}" -t "${TAG}" \
  --output "type=local,dest=${tmp}" -f - .

# The number this whole script exists to produce.
bytes=$(wc -c <"${tmp}/ffmpeg" | tr -d ' ')
printf '\n  ffmpeg %s, %s: %s bytes (%.1f MB)\n' \
  "${VERSION}" "${PLATFORM}" "${bytes}" "$(echo "${bytes}/1000000" | bc -l)"
printf '  budget is 20 MB; 40 MB is the abort line (docs/tasks/T44-remux.md)\n\n'

if [[ -n "${OUT}" ]]; then
  mkdir -p "${OUT}"
  cp "${tmp}/ffmpeg" "${OUT}/ffmpeg"
  echo "  wrote ${OUT}/ffmpeg"
fi

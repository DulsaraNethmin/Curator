#!/usr/bin/env bash
#
# Build the minimal ffmpeg curator ships, for linux/arm64, and report its size.
#
# This is not a general-purpose ffmpeg. It demuxes the three containers the
# library actually holds, muxes ONE — fragmented MP4 — reads from a file, writes
# to a pipe, and has no encoder, no filter and no network protocol compiled in
# at all. That is not minimalism for its own sake: `-c copy` is the whole of
# what playback does (docs/decisions.md D24), so an encoder in the binary would
# be dead weight that also makes it possible to change the mind later by
# accident.
#
# **The size is the point.** Budget 20 MB, reconsider past 40 MB, and past that
# the answer is to ship direct play alone (docs/tasks/T44-remux.md). The measured
# number goes into docs/progress.md, because phase 9's image budget is built on
# it.
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

# --disable-everything turns off every component; each --enable below is one
# curator's own invocation needs, and the list is deliberately short enough to
# read.
#
#   matroska  the .mkv releases, which are the whole reason this exists
#   mov       .mp4 and .m4v — which direct play already handles, but a remux
#             with a ?start= offset is still asked for on them when a browser
#             will not seek
#   avi       the fourth extension library's picker recognises
#   mp4       the only muxer. -f mp4 with the fragmenting movflags
#   file,pipe read a film off the disk, write it to stdout. Nothing else.
#
# The parsers and bitstream filters are what a stream COPY needs to move packets
# between containers without touching them: extract_extradata pulls the codec
# headers out where the source container kept them inline, and aac_adtstoasc
# converts AAC framing that AVI and MPEG-TS use into the one MP4 wants. They are
# not decoders and they do not re-encode anything.
readonly FLAGS=(
  --disable-everything
  --disable-autodetect
  --disable-network
  --disable-doc
  --disable-ffplay
  --disable-ffprobe
  --disable-debug
  --disable-shared
  --enable-static
  --enable-small
  --enable-demuxer=matroska,mov,avi
  --enable-muxer=mp4
  --enable-protocol=file,pipe
  --enable-parser=h264,hevc,aac,ac3,mpeg4video,vp9,av1,flac,opus,vorbis,dca
  --enable-bsf=extract_extradata,aac_adtstoasc,h264_mp4toannexb,hevc_mp4toannexb,vp9_superframe,av1_metadata
  --extra-ldflags=-static
  --pkg-config-flags=--static
)

dockerfile() {
  cat <<DOCKERFILE
FROM alpine:3.21 AS build
RUN apk add --no-cache build-base coreutils yasm nasm pkgconf perl tar xz
WORKDIR /src
ADD https://ffmpeg.org/releases/ffmpeg-${VERSION}.tar.xz ffmpeg.tar.xz
RUN tar xf ffmpeg.tar.xz --strip-components=1 && rm ffmpeg.tar.xz
RUN ./configure ${FLAGS[*]} && make -j"\$(nproc)"
# Stripped, because the symbols are several megabytes of a budget measured in
# tens of them and nobody debugs this binary — it is run with one argv.
RUN strip ffmpeg && ./ffmpeg -hide_banner -version && ls -l ffmpeg
FROM scratch
COPY --from=build /src/ffmpeg /ffmpeg
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

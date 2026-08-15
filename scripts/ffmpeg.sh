#!/bin/sh
#
# Build the minimal ffmpeg curator ships. This half runs INSIDE an Alpine build
# stage, and it is the only place the configure flags and the pinned version
# exist.
#
# There are two callers and they must not disagree:
#
#   scripts/build-ffmpeg.sh   a one-off measurement on a laptop, reporting bytes
#   Dockerfile                the `ffmpeg` stage of the image T47 builds
#
# A second copy of the flag list would drift, and the copy that drifts is the
# image's — the one nobody reads until a remux fails on somebody else's install.
# So the flags live here and both callers run this file.
#
# POSIX sh, not bash: Alpine has no bash and adding one to a build stage to get
# an array is a poor trade.
#
#   apk add --no-cache build-base coreutils yasm nasm pkgconf perl tar xz
#   ./scripts/ffmpeg.sh /out          # writes /out/ffmpeg, stripped
#
set -eu

VERSION="${FFMPEG_VERSION:-8.1.1}"
OUT="${1:-/out}"

# The tarball's SHA-256, so a connection reset half way through is a build that
# stops here saying so, rather than a `./configure` failing on a truncated tree
# forty lines later. It happened on the first run of this file.
#
# Measured from two independent downloads rather than copied from a published
# checksum: ffmpeg.org serves a .asc signature and no .sha256, and verifying the
# signature would mean gpg and the FFmpeg release key in a build stage. So this
# is integrity of the transfer, not provenance — which is the failure that
# actually occurs.
SHA256="${FFMPEG_SHA256:-b6863adde98898f42602017462871b5f6333e65aec803fdd7a6308639c52edf3}"

# This is not a general-purpose ffmpeg. It demuxes the three containers the
# library actually holds, muxes ONE — fragmented MP4 — reads from a file, writes
# to a pipe, and has no encoder, no filter and no network protocol compiled in
# at all. That is not minimalism for its own sake: `-c copy` is the whole of
# what playback does (docs/decisions.md D24), so an encoder in the binary would
# be dead weight that also makes it possible to change the mind later by
# accident.
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
FLAGS="
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
"

src="$(mktemp -d)"
cd "${src}"

# Alpine's wget is busybox's, and it has no --tries. The tarball is fetched here
# rather than by a Dockerfile `ADD` so that the pinned version has one home; the
# RUN layer caches either way, so the download only repeats when the build does.
url="https://ffmpeg.org/releases/ffmpeg-${VERSION}.tar.xz"
n=0
until wget -q -O ffmpeg.tar.xz "${url}" && echo "${SHA256}  ffmpeg.tar.xz" | sha256sum -c -; do
  n=$((n + 1))
  if [ "${n}" -ge 3 ]; then
    echo "fetching ${url} failed ${n} times" >&2
    exit 1
  fi
  echo "fetching ${url} failed, retrying (${n})" >&2
  rm -f ffmpeg.tar.xz
done

tar xf ffmpeg.tar.xz --strip-components=1
rm ffmpeg.tar.xz

# shellcheck disable=SC2086  # FLAGS is a word list on purpose
./configure ${FLAGS}
make -j"$(nproc)"

# Stripped, because the symbols are several megabytes of a budget measured in
# tens of them and nobody debugs this binary — it is run with one argv.
strip ffmpeg
./ffmpeg -hide_banner -version

mkdir -p "${OUT}"
cp ffmpeg "${OUT}/ffmpeg"
cd /
rm -rf "${src}"

printf '\n  ffmpeg %s: %s bytes\n\n' "${VERSION}" "$(wc -c <"${OUT}/ffmpeg" | tr -d ' ')"

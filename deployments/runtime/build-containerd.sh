#!/usr/bin/env bash
# Manual build of the pinned runtime with mandatory NRI participation fixes.
set -euo pipefail
script_dir=$(cd "$(dirname "$0")" && pwd)
source_commit=f2551031d7276a770f65f98c9b52e57e7dad07e8
source_digest=f01219d0d6c54ba014e69e821ae0ae88cccfcc9c5d997c628b438b9d8a6df8a6
output=$(realpath -m "${1:-./bin/containerd-2.4.1-dra-nri1}")
build_dir=$(mktemp -d "${TMPDIR:-/tmp}/dra-containerd-build.XXXXXX")
trap 'rm -rf "$build_dir"' EXIT
curl -fL --retry 3 -o "$build_dir/source.tar.gz" "https://codeload.github.com/containerd/containerd/tar.gz/$source_commit"
printf '%s  %s\n' "$source_digest" "$build_dir/source.tar.gz" | sha256sum -c -
tar -xzf "$build_dir/source.tar.gz" --strip-components=1 -C "$build_dir"
cd "$build_dir"
patch --fuzz=0 -p1 < "$script_dir/containerd-nri-required.patch"
mkdir -p "$(dirname "$output")"
CGO_ENABLED=0 go build -mod=vendor -trimpath \
  -ldflags "-s -w -X github.com/containerd/containerd/v2/version.Version=v2.4.1-dra-nri1 -X github.com/containerd/containerd/v2/version.Revision=$source_commit" \
  -o "$output" ./cmd/containerd
echo "Built $output from containerd $source_commit with the shipped NRI patch"

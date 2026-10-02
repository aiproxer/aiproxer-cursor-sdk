#!/usr/bin/env bash
#
# Assemble the plugin's native release archive and its installable tree.
#
# Builds the outer plugin executable and the plugin-private bridge launcher for
# the host platform, builds the production JavaScript, stages only the production
# npm dependencies plus the metadata the bridge needs to resolve and verify the SDK
# version, stages the private Node runtime with its license and provenance notices,
# renders the closed host manifest and the release metadata, records a checksum
# over every archive file including the plugin-private ones, and emits a
# per-platform archive with its own digest.
#
# The archive layout is not restated here. It is read from
# cmd/lip-cursor-sdk-packaging, which reports internal/packagelayout, so the
# packager, the verifier, and the private launcher cannot disagree about it.
#
# npm, Node, and Go are build-time tools here. Nothing the archive needs at run
# time is looked up on PATH, through a shell, or through a package manager.
#
# The archive is assembled for this machine's platform only. Cross-compiling a
# platform and claiming it would be an unvalidated claim, so --platform refuses
# anything but the host platform.
#
# Usage:
#   scripts/package-plugin.sh [--repo-root DIR] [--out-dir DIR]
#                             [--node-dist ZIP|DIR] [--node-runtime EXE]
#                             [--platform os/arch]
set -euo pipefail

repo_root=""
out_dir=""
node_dist=""
node_runtime=""
platform=""

while [ "$#" -gt 0 ]; do
  case "$1" in
    --repo-root) repo_root="${2:-}"; shift 2 ;;
    --out-dir) out_dir="${2:-}"; shift 2 ;;
    --node-dist) node_dist="${2:-}"; shift 2 ;;
    --node-runtime) node_runtime="${2:-}"; shift 2 ;;
    --platform) platform="${2:-}"; shift 2 ;;
    -h|--help) sed -n '2,30p' "$0"; exit 0 ;;
    *) printf 'package-plugin: unknown argument %s\n' "$1" >&2; exit 2 ;;
  esac
done

script_dir="$(cd -- "$(dirname -- "$0")" && pwd)"
[ -n "$repo_root" ] || repo_root="$(dirname -- "$script_dir")"
repo_root="$(cd -- "$repo_root" && pwd)"
[ -n "$out_dir" ] || out_dir="$repo_root/dist"

fail() {
  printf 'package-plugin: %s\n' "$1" >&2
  exit 1
}

# run runs one build-time tool and fails packaging when it does.
run() {
  local log
  log="$(mktemp)"
  if ! (cd "$1" && shift && "$@") >"$log" 2>&1; then
    printf 'package-plugin: %s failed\n%s\n' "$*" "$(cat "$log")" >&2
    rm -f "$log"
    exit 1
  fi
  cat "$log"
  rm -f "$log"
}

# run_in runs one build-time tool inside a directory.
run_in() {
  local dir="$1"
  shift
  run "$dir" "$@"
}

# The digest tool is resolved once because xargs needs an executable, not a shell
# function, and because the archive digests must not depend on which of the two
# spellings this machine happens to have.
if command -v sha256sum >/dev/null 2>&1; then
  sha256_tool=sha256sum
  sha256_args=()
elif command -v shasum >/dev/null 2>&1; then
  sha256_tool=shasum
  sha256_args=(-a 256)
else
  printf 'package-plugin: no sha256sum or shasum on this machine; the archive digests cannot be computed\n' >&2
  exit 1
fi

# sha256_of digests one file.
sha256_of() {
  "$sha256_tool" "${sha256_args[@]}" "$1" | cut -d' ' -f1
}

# release_scalar reads one flat scalar out of release.yaml. The file is a flat key
# set; anything richer belongs to the metadata renderer.
release_scalar() {
  sed -n "s/^$1:[[:space:]]*//p" "$repo_root/release.yaml" | head -n 1
}

# layout_report reads one field of the archive layout contract.
layout_report() {
  layout_json="$(GOWORK=off go run ./cmd/lip-cursor-sdk-packaging layout -platform "$layout_platform")"
  printf '%s' "$layout_json" | tr -d '\n' | sed "s/.*\"$1\": *\"\([^\"]*\)\".*/\1/"
}

# layout_raw prints the JSON text of one field, for arrays.
layout_raw() {
  printf '%s' "$layout_json" | tr -d '\n' | sed "s/.*\"$1\": *\(\[[^]]*\]\).*/\1/"
}

# layout_entries prints one required archive entry per line.
layout_entries() {
  printf '%s' "$layout_json" | tr -d '\n' | sed "s/.*\"$1\": *\[\([^]]*\)\].*/\1/" |
    tr ',' '\n' | sed 's/^ *//; s/ *$//; s/^"//; s/"$//' | grep -v '^$'
}

# node_dist_root accepts an extracted Node distribution either directly or under the
# single top-level directory an official archive unpacks into. The POSIX
# distribution keeps its runtime in bin/, the Windows one at the root.
node_dist_root() {
  local dir="$1" name="$2" candidate
  for candidate in "$dir" "$dir"/*/; do
    candidate="${candidate%/}"
    if [ -f "$candidate/$name" ] || [ -f "$candidate/bin/$name" ]; then
      printf '%s' "$candidate"
      return
    fi
  done
  fail "no Node distribution found under $dir"
}

# node_dist_runtime locates the runtime executable inside a distribution root.
node_dist_runtime() {
  local root="$1" name="$2" candidate
  for candidate in "$root/$name" "$root/bin/$name"; do
    if [ -f "$candidate" ]; then
      printf '%s' "$candidate"
      return
    fi
  done
  fail "no $name in the Node distribution at $root"
}

# find_node_license locates the license text that ships with a Node executable. A
# runtime staged without its notices is not releasable, so an absent license is a
# failure rather than a warning.
find_node_license() {
  local dir candidate
  dir="$(dirname -- "$1")"
  for _ in 1 2 3; do
    for candidate in "$dir/LICENSE" "$dir/LICENSE.txt"; do
      if [ -f "$candidate" ]; then
        printf '%s' "$candidate"
        return
      fi
    done
    dir="$(dirname -- "$dir")"
    [ "$dir" != "/" ] || break
  done
  fail "no Node LICENSE found next to $1 or its parents; a staged private runtime must ship its license notices"
}

# write_third_party_notices records, factually, what the archive redistributes and
# what is unresolved. It states the licenses the components declare and it does not
# assert a redistribution right nobody has confirmed.
write_third_party_notices() {
  local path="$1" modules="$2" runtime="$3" node_version="$4" node_source="$5" platform_tag="$6"
  local manifest pkg_name pkg_version pkg_license rel

  {
    printf '# Third-party notices\n\n'
    printf 'This file records what the plugin archive redistributes. It is evidence, not a\n'
    printf 'license grant: the licenses below are the ones the redistributed components\n'
    printf 'declare, and no redistribution right is asserted here.\n\n'
    printf 'Platform: %s\n' "$platform_tag"
    printf 'Private Node runtime: %s (source: %s)\n\n' "$node_version" "$node_source"
    printf '## Private Node runtime\n\n'
    printf -- '- Node.js is MIT licensed. The distribution LICENSE staged in this archive holds\n'
    printf '  the Node.js license grant together with the notices for the components Node\n'
    printf '  bundles (ICU, OpenSSL, c-ares, libuv, and the rest). See the staged distribution LICENSE.\n'
    printf -- '- Components the staged runtime reports about itself:\n'
    "$runtime" -p 'Object.entries(process.versions).map(([k,v])=>`  - ${k} ${v}`).join("\n")' | LC_ALL=C sort
    printf '\n## Production npm dependencies\n\n'
    printf '| package | version | declared license | staged at |\n'
    printf '| --- | --- | --- | --- |\n'
    while IFS= read -r manifest; do
      [ -n "$manifest" ] || continue
      pkg_name="$(sed -n 's/.*"name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$manifest" | head -n 1)"
      [ -n "$pkg_name" ] || continue
      pkg_version="$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$manifest" | head -n 1)"
      pkg_license="$(sed -n 's/.*"license"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$manifest" | head -n 1)"
      rel="${manifest#"$modules"/}"
      printf '| %s | %s | %s | %s |\n' "$pkg_name" "${pkg_version:-(not declared)}" \
        "${pkg_license:-(not declared in package.json)}" "$rel"
    done < <(find "$modules" -name package.json -type f | LC_ALL=C sort)
    printf '\n## Cursor SDK\n\n'
    printf -- '- @cursor/sdk is proprietary. Its staged LICENSE.md states that use is subject to\n'
    printf "  Cursor's Terms of Service (https://cursor.com/terms-of-service) and it grants no\n"
    printf '  redistribution right. It is staged because the bridge cannot resolve it otherwise.\n'
    printf -- '- The platform package ships bundled native binaries (rg and cursandbox). Their\n'
    printf '  license texts are not redistributed by the package, and no ripgrep license text\n'
    printf '  is present in the staged tree.\n'
    printf -- '- ACTION REQUIRED before any publication: a maintainer has to confirm the\n'
    printf '  redistribution rights for @cursor/sdk and for its bundled binaries. Until that\n'
    printf '  confirmation exists this archive is release-blocked.\n\n'
    printf '## Plugin\n\n'
    printf -- '- The plugin sources are MIT licensed (see the staged plugin LICENSE). Derived\n'
    printf '  connector code originates from an Apache-2.0 project; see PROVENANCE.md in the\n'
    printf '  source repository.\n'
  } >"$path"
}

host_os="$(GOWORK=off go env GOOS)"
host_arch="$(GOWORK=off go env GOARCH)"
host_platform="$host_os/$host_arch"
if [ -n "$platform" ] && [ "$platform" != "$host_platform" ]; then
  fail "refusing to assemble $platform on $host_platform: cross-compilation is not native validation, and an unvalidated platform claim is not a truthful one. Run this script on $platform to produce that archive."
fi

layout_json=""
layout_platform="$host_platform"
platform_tag="${host_platform//\//-}"
launcher_name="$(layout_report launcher_name)"
manifest_name="$(layout_report manifest)"
compatibility_name="$(layout_report compatibility)"
checksums_name="$(layout_report checksums)"
licenses_dir="$(layout_report licenses_dir)"
private_prefix="$(layout_report private_prefix)"
checksum_separator="$(layout_report checksum_separator)"
outer_executable="$(layout_report outer_executable)"
launcher_rel="$(layout_report launcher)"
bridge_package_dir="$(layout_report bridge_package_dir)"
bridge_entry="$(layout_report bridge_entry)"
bridge_dist="$(layout_report bridge_dist)"
bridge_modules="$(layout_report bridge_modules)"
private_runtime="$(layout_report private_runtime)"
# The private runtime file name comes from the archive contract, so the staged
# runtime is whatever the launcher will look for on this platform.
runtime_file_name="${private_runtime##*/}"


plugin_id="$(release_scalar plugin_id)"
version="$(release_scalar version)"
build_id="$(release_scalar build_id)"
[ -n "$plugin_id" ] || fail 'release.yaml has no plugin_id'
[ -n "$version" ] || fail 'release.yaml has no version'
[ -n "$build_id" ] || fail 'release.yaml has no build_id'

base="cursorsdk-$version-$platform_tag"
mkdir -p "$out_dir"
out_dir="$(cd -- "$out_dir" && pwd)"
install_root="$out_dir/$base"
staging="$out_dir/.staging-$base"
rm -rf "$staging"
mkdir -p "$staging"
runtime_temp=""

cleanup() {
  rm -rf "$staging"
  [ -n "$runtime_temp" ] && rm -rf "$runtime_temp"
  return 0
}
trap cleanup EXIT

# 1. Native executables. CGO off and a trimmed build id keep the outer executable
#    reproducible across packaging runs, which is what makes the manifest digest a
#    meaningful authority for the outer process.
outer_exe="$staging/$outer_executable"
launcher="$staging/$launcher_rel"
mkdir -p "$(dirname -- "$outer_exe")" "$(dirname -- "$launcher")"
outer_command="$(release_scalar command)"
[ -n "$outer_command" ] || fail 'release.yaml has no command'
GOWORK=off CGO_ENABLED=0 go build -trimpath -ldflags=-buildid= -o "$outer_exe" "$outer_command"
GOWORK=off CGO_ENABLED=0 go build -trimpath -ldflags=-buildid= -o "$launcher" "./cmd/$launcher_name"

# 2. Production JavaScript. The dev toolchain stays in the source tree; only the
#    built output and the production dependency tree are staged.
bridge_source="$repo_root/bridge-node"
run_in "$bridge_source" npm ci --no-audit --no-fund >/dev/null
run_in "$bridge_source" npm run build >/dev/null

# 3. Plugin-private bridge tree. The lockfile is present only so npm can resolve the
#    production tree; the archive itself needs no package manager.
bridge_dir="$staging/$bridge_package_dir"
mkdir -p "$bridge_dir"
cp "$bridge_source/package.json" "$bridge_dir/package.json"
cp "$bridge_source/package-lock.json" "$bridge_dir/package-lock.json"
run_in "$bridge_dir" npm ci --omit=dev --no-audit --no-fund >/dev/null
rm -f "$bridge_dir/package-lock.json"

mkdir -p "$staging/$bridge_dist"
cp -R "$bridge_source/dist/." "$staging/$bridge_dist/"
entry_name="${bridge_entry#"$bridge_package_dir"/}"
mkdir -p "$(dirname -- "$staging/$bridge_entry")"
cp "$bridge_source/$entry_name" "$staging/$bridge_entry"

# 4. Private Node runtime plus the notices that have to travel with it.
mkdir -p "$staging/$licenses_dir"
runtime_source=""
runtime_label=""
if [ -n "$node_dist" ]; then
  [ -e "$node_dist" ] || fail "-NodeDist $node_dist does not exist"
  runtime_label="$(basename -- "$node_dist")"
  if [ -d "$node_dist" ]; then
    dist_root="$(node_dist_root "$node_dist" "$runtime_file_name")"
  else
    runtime_temp="$(mktemp -d)"
    # Each platform publishes its official distribution as a zip or a tarball, so
    # both are accepted rather than one being assumed.
    case "$node_dist" in
      *.zip) unzip -q "$node_dist" -d "$runtime_temp" ;;
      *) tar -xf "$node_dist" -C "$runtime_temp" ;;
    esac
    dist_root="$(node_dist_root "$runtime_temp" "$runtime_file_name")"
  fi
  runtime_source="$(node_dist_runtime "$dist_root" "$runtime_file_name")"
  runtime_license="$(find_node_license "$runtime_source")"
elif [ -n "$node_runtime" ]; then
  runtime_source="$node_runtime"
  [ -e "$runtime_source" ] || fail "private Node runtime $runtime_source does not exist"
  runtime_label="path:$(basename -- "$runtime_source")"
  runtime_license="$(find_node_license "$(cd -- "$(dirname -- "$runtime_source")" && pwd)/$(basename -- "$runtime_source")")"
elif [ -n "${LIP_PACKAGE_NODE_RUNTIME:-}" ]; then
  runtime_source="$LIP_PACKAGE_NODE_RUNTIME"
  [ -e "$runtime_source" ] || fail "private Node runtime $runtime_source does not exist"
  runtime_label="path:$(basename -- "$runtime_source")"
  runtime_license="$(find_node_license "$runtime_source")"
else
  command -v node >/dev/null 2>&1 ||
    fail 'no private Node runtime source: pass --node-dist <distribution>, --node-runtime <executable>, or set LIP_PACKAGE_NODE_RUNTIME'
  runtime_source="$(command -v node)"
  runtime_label="path:$(basename -- "$runtime_source")"
  runtime_license="$(find_node_license "$runtime_source")"
fi

private_runtime_path="$staging/$private_runtime"
mkdir -p "$(dirname -- "$private_runtime_path")"
cp "$runtime_source" "$private_runtime_path"
chmod 0755 "$private_runtime_path"
cp "$runtime_license" "$staging/$licenses_dir/nodejs-LICENSE"
cp "$repo_root/LICENSE" "$staging/$licenses_dir/plugin-LICENSE"
node_version="$("$private_runtime_path" --version)"

# 5. Closed host manifest and release metadata, both derived from the staged tree
#    rather than described independently of it.
exe_digest="$(sha256_of "$outer_exe")"
GOWORK=off go run ./cmd/lip-cursor-sdk-packaging render \
  -repo "$repo_root" -staging "$staging" -platform "$layout_platform" \
  -exe-sha256 "$exe_digest" -node-source "$runtime_label" >/dev/null

write_third_party_notices "$staging/$licenses_dir/THIRD-PARTY-NOTICES.md" \
  "$staging/$bridge_modules" "$private_runtime_path" "$node_version" "$runtime_label" "$layout_platform"

# 6. Checksums over every archive file, plugin-private files included. The record
#    cannot cover itself, so it is written last.
(
  cd "$staging"
  find . -type f ! -name "$checksums_name" -print0 |
    xargs -0 "$sha256_tool" "${sha256_args[@]}" |
    sed "s|  \./|  |" |
    LC_ALL=C sort -k2 >"$checksums_name"
)
file_count="$(find "$staging" -type f | wc -l | tr -d ' ')"

# 7. Publish the install tree, then the per-platform archive.
rm -rf "$install_root"
mv "$staging" "$install_root"
trap - EXIT

archive="$out_dir/$base.tar.gz"
rm -f "$archive"
tar -czf "$archive" -C "$out_dir" "$base"
archive_digest="$(sha256_of "$archive")"
printf '%s%s%s\n' "$archive_digest" "$checksum_separator" "$(basename -- "$archive")" >"$archive.sha256"

printf 'plugin_id: %s\n' "$plugin_id"
printf 'plugin_version: %s\n' "$version"
printf 'build_id: %s\n' "$build_id"
printf 'platform: %s\n' "$layout_platform"
printf 'packaging_variant: private-runtime\n'
printf 'out_dir: %s\n' "$out_dir"
printf 'install_root: %s\n' "$install_root"
printf 'archive: %s\n' "$archive"
printf 'archive_sha256: %s\n' "$archive_digest"
printf 'node_version: %s\n' "$node_version"
printf 'node_source: %s\n' "$runtime_label"
printf 'file_count: %s\n' "$file_count"

#!/usr/bin/env bash
#
# Assemble the plugin's native release archive and its installable tree.
#
# Builds the outer plugin executable and the plugin-private bridge launcher for
# the host platform, builds the production JavaScript out of the source tree, stages
# the metadata the bridge needs to resolve and verify the SDK version - the package
# manifest and the lockfile that pins it - plus the private Node runtime together with
# that runtime's own bundled npm, renders the closed host manifest and the release
# metadata, records a checksum over every archive file including the plugin-private
# ones, and emits a per-platform archive with its own digest.
#
# The archive stages NO third-party package code. The Cursor SDK is proprietary and is
# not redistributed, so neither @cursor/sdk nor its dependency closure is shipped: the
# operator provisions that tree once, out of band, with the runtime this archive carries.
# The runtime's own npm is staged because the provisioning command has to run without a
# global Node or a global package manager.
#
# The archive layout is not restated here. It is read from
# cmd/lip-cursor-sdk-packaging, which reports internal/packagelayout, so the packager,
# the verifier, and the private launcher cannot disagree about it.
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

# usage prints the leading comment block of this script, so the help text cannot drift
# out of the file by an edit to the header.
usage() {
  awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$1"
}

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
    -h|--help) usage "$0"; exit 0 ;;
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

# layout_report reads one field of the archive layout contract. The contract comes from
# this script's own repository, not from the directory the packager was started in:
# `go run ./cmd/lip-cursor-sdk-packaging` is a module-relative path, so asking the
# working directory for it makes packaging work from a checkout and fail from anywhere
# else. This is the `cd "$repo_root"` the PowerShell packager's
# Invoke-Tool -WorkingDirectory corresponds to.
layout_report() {
  layout_json="$(cd "$repo_root" && GOWORK=off go run ./cmd/lip-cursor-sdk-packaging layout -platform "$layout_platform")"
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

# write_third_party_notices records, factually, what the archive redistributes, what it
# deliberately does not, and who owns what. It states the licenses the redistributed
# components declare, it asserts no redistribution right nobody has confirmed, and it
# says plainly that the provisioned dependency tree is the operator's own resolution
# rather than this project's artifact.
write_third_party_notices() {
  local path="$1" lockfile="$2" bridge_manifest="$3" runtime="$4" node_version="$5" node_kind="$6"
  local node_label="$7" platform_tag="$8" provision_command="$9" sdk_package="${10}"

  {
    printf '# Third-party notices\n\n'
    printf 'This file records what the plugin archive redistributes. It is evidence, not a\n'
    printf 'license grant: the licenses below are the ones the redistributed components\n'
    printf 'declare, and no redistribution right is asserted here.\n\n'
    printf 'Platform: %s\n' "$platform_tag"
    printf 'Private Node runtime: %s (source kind: %s, source: %s)\n\n' "$node_version" "$node_kind" "$node_label"
    printf '## Private Node runtime\n\n'
    if [ "$node_kind" != 'official-distribution' ]; then
      printf -- '- This runtime was staged from the build machine'"'"'s toolchain, not from an\n'
      printf '  official Node distribution, and no distribution digest backs it. Re-stage\n'
      printf '  with --node-dist <official archive> before publishing so the provenance of the\n'
      printf '  shipped runtime can be checked against the release SHASUMS256.txt.\n'
    fi
    printf -- '- Node.js is MIT licensed. The distribution LICENSE staged in this archive holds\n'
    printf '  the Node.js license grant together with the notices for the components Node\n'
    printf '  bundles (ICU, OpenSSL, c-ares, libuv, and the rest). See the staged distribution LICENSE.\n'
    printf -- '- The runtime ships with its own bundled npm, and so does this archive: that is\n'
    printf '  what the provisioning command below runs, so an operator needs neither a global\n'
    printf '  Node nor a global package manager. npm is MIT licensed under the same Node\n'
    printf '  distribution license staged here.\n'
    printf -- '- Components the staged runtime reports about itself:\n'
    "$runtime" -p 'Object.entries(process.versions).map(([k,v])=>`  - ${k} ${v}`).join("\n")' | LC_ALL=C sort
    printf '\n## Cursor SDK: not redistributed, operator-provisioned\n\n'
    printf -- '- $sdk_package is proprietary. Its LICENSE.md states that use is subject to\n'
    printf "  Cursor's Terms of Service (https://cursor.com/terms-of-service) and it grants no\n"
    printf '  redistribution right. Its platform package additionally ships bundled native\n'
    printf '  binaries (rg and cursandbox) whose own license texts the package does not\n'
    printf '  redistribute.\n'
    printf -- '- This archive therefore contains no $sdk_package and no dependency closure of\n'
    printf '  any kind. The checksums in this archive cover the shipped files only.\n'
    printf -- '- The operator obtains the SDK themselves, accepting Cursor'"'"'s terms, and\n'
    printf '  provisions it once with the runtime this archive ships:\n\n'
    printf '      %s\n\n' "$provision_command"
    printf -- '- npm specifically, not any package manager: `overrides` semantics differ\n'
    printf '  across package managers and the security baseline below depends on that\n'
    printf '  override being honored. A tree provisioned with another package manager is\n'
    printf '  unsupported.\n'
    printf -- '- Provisioning is an install-time operator step. The plugin never installs\n'
    printf '  anything, runs no package manager, and downloads nothing.\n'
    printf -- '- The provisioned tree is operator-attributable: it is resolved by the\n'
    printf "  operator's npm from the shipped lockfile, it is outside this project's\n"
    printf '  checksum record, and no notice in this archive covers it.\n\n'
    printf '## Locked dependency closure (pinned, not shipped)\n\n'
    printf -- '- What the shipped lockfile pins for the operator to provision:\n\n'
    write_locked_inventory "$runtime" "$lockfile" "$bridge_manifest"
    printf '\n\n## Plugin\n\n'
    printf -- '- The plugin sources are MIT licensed (see the staged plugin LICENSE). Derived\n'
    printf '  connector code originates from an Apache-2.0 project; see PROVENANCE.md in the\n'
    printf '  source repository.\n'
  } >"$path"
}

# write_locked_inventory records what the shipped lockfile pins and which half of it the
# provisioning command actually installs. The archive stages no third-party package code,
# so this is the inventory of what the operator's npm will resolve rather than of what
# the archive carries: naming the pinned versions is still evidence, and stating the
# attribution next to it is what keeps it honest.
#
# The inventory is read with the runtime this archive stages rather than by parsing JSON
# in the shell, so both packagers print the same rows from the same lockfile.
write_locked_inventory() {
  local runtime="$1" lockfile="$2" manifest="$3"
  "$runtime" -e '
    const fs = require("fs");
    const lock = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
    const manifest = JSON.parse(fs.readFileSync(process.argv[2], "utf8"));
    const packages = lock.packages || {};
    const root = packages[""] || {};
    const pinned = Object.keys(packages).filter((key) => key !== "");
    if (pinned.length === 0) throw new Error("the lockfile pins no packages");

    // The dependency directory name is read out of the lockfile keys rather than
    // written here, so this script never carries a second copy of the layout it
    // stages. Every non-root key starts with it.
    const depDir = pinned[0].split("/")[0];

    // npm resolves a dependency from the dependents own directory and then from each
    // enclosing one, so the installed set is the closure of the root dependencies under
    // that rule. Everything else the lockfile pins is development-only and --omit=dev
    // leaves it out of an install tree.
    const prefixes = (key) => {
      const out = [];
      for (let rest = key; ; ) {
        out.push(rest);
        const cut = rest.lastIndexOf("/" + depDir + "/");
        if (cut < 0) break;
        rest = rest.slice(0, cut);
      }
      out.push("");
      return out;
    };
    const resolveFrom = (key, name) => {
      for (const base of prefixes(key)) {
        const candidate = base ? base + "/" + depDir + "/" + name : depDir + "/" + name;
        if (packages[candidate]) return candidate;
      }
      return "";
    };

    const installed = new Set();
    const queue = Object.keys({
      ...(root.dependencies || {}),
      ...(root.optionalDependencies || {}),
    }).map((name) => ["", name]);
    while (queue.length > 0) {
      const [from, name] = queue.pop();
      const key = resolveFrom(from, name);
      if (!key || installed.has(key)) continue;
      installed.add(key);
      const entry = packages[key] || {};
      const next = { ...(entry.dependencies || {}), ...(entry.optionalDependencies || {}) };
      for (const dependency of Object.keys(next)) queue.push([key, dependency]);
    }

    const table = (keys) => [
      "| package | pinned version |",
      "| --- | --- |",
      ...[...keys].sort().map((key) =>
        `| ${key} | ${(packages[key] && packages[key].version) || "(not declared)"} |`),
    ].join("\n");

    console.log(table([...installed].filter((key) => packages[key])));
    const devOnly = pinned.filter((key) => !installed.has(key));
    if (devOnly.length > 0) {
      console.log("");
      console.log("- Development-only packages the lockfile pins and `--omit=dev` leaves out:");
      console.log("");
      console.log(table(devOnly));
    }

    const overrides = manifest.overrides || {};
    const names = Object.keys(overrides).sort();
    console.log("");
    console.log(
      names.length
        ? "- Security overrides the shipped bridge manifest declares, honored through npm " +
          "`overrides`" +
          `: ${names.map((name) => `${name} ${overrides[name]}`).join(", ")}.`
        : "- The shipped bridge manifest declares no npm overrides.",
    );
  ' "$lockfile" "$manifest"
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
sdk_package_name="$(layout_report sdk_package_name)"
bridge_package_json="$(layout_report bridge_package_json)"
bridge_package_lock="$(layout_report bridge_package_lock)"
private_npm_root="$(layout_report private_npm_root)"
private_npm_cli="$(layout_report private_npm_cli)"
sdk_provision_command="$(layout_report sdk_provision_command)"
private_runtime="$(layout_report private_runtime)"
private_runtime_dir="$(layout_report private_runtime_dir)"
# The private runtime file name comes from the archive contract, so the staged
# runtime is whatever the launcher will look for on this platform.
runtime_file_name="${private_runtime##*/}"
# Where the platform's own Node distribution keeps its bundled npm, relative to the
# runtime executable's installation directory. It is the same relative path the archive
# stages it at, so nothing inside the npm tree is renamed and the entry point the
# provisioning command names is the real one.
npm_rel="${private_npm_root#"$private_runtime_dir"/}"


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
# Every module-relative go invocation below runs in the repository rather than in
# whatever directory the packager was started in, so the script assembles an archive
# from any working directory. See layout_report for the same rule applied to the
# layout contract.
(cd "$repo_root" && GOWORK=off CGO_ENABLED=0 go build -trimpath -ldflags=-buildid= -o "$outer_exe" "$outer_command")
(cd "$repo_root" && GOWORK=off CGO_ENABLED=0 go build -trimpath -ldflags=-buildid= -o "$launcher" "./cmd/$launcher_name")

# 2. Production JavaScript. The dev toolchain stays in the source tree and never enters
#    the archive; only the built output does.
bridge_source="$repo_root/bridge-node"
run_in "$bridge_source" npm ci --no-audit --no-fund >/dev/null
run_in "$bridge_source" npm run build >/dev/null

# 3. Plugin-private bridge tree. The manifest and the lockfile ship because they are
#    what the operator provisions against: the manifest pins the SDK version the bridge
#    verifies at run time, and the lockfile pins the exact closure and the undici
#    security override. The dependency closure itself is NOT staged - the Cursor SDK is
#    proprietary and is not redistributed - so the archive needs no package manager of
#    its own to run.
bridge_dir="$staging/$bridge_package_dir"
mkdir -p "$bridge_dir"
cp "$bridge_source/${bridge_package_json##*/}" "$staging/$bridge_package_json"
cp "$bridge_source/${bridge_package_lock##*/}" "$staging/$bridge_package_lock"

mkdir -p "$staging/$bridge_dist"
# The built JavaScript is copied out of the source tree by the same name the archive
# contract gives it, reduced to the path relative to the bridge package directory, so
# this script stages exactly the directory the launcher and the verifier will look in.
dist_name="${bridge_dist#"$bridge_package_dir"/}"
cp -R "$bridge_source/$dist_name/." "$staging/$bridge_dist/"
entry_name="${bridge_entry#"$bridge_package_dir"/}"
mkdir -p "$(dirname -- "$staging/$bridge_entry")"
cp "$bridge_source/$entry_name" "$staging/$bridge_entry"

# 4. Private Node runtime plus the notices that have to travel with it.
#    The source kind is recorded next to the label because the three sources are not
#    the same claim: only an official distribution is a copy of a distribution, and a
#    runtime staged from the build machine's PATH says so in the archive.
mkdir -p "$staging/$licenses_dir"
runtime_source=""
runtime_label=""
runtime_kind=""
if [ -n "$node_dist" ]; then
  [ -e "$node_dist" ] || fail "-NodeDist $node_dist does not exist"
  runtime_kind="official-distribution"
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
  runtime_kind="supplied-runtime"
  runtime_label="$(basename -- "$runtime_source")"
  runtime_license="$(find_node_license "$(cd -- "$(dirname -- "$runtime_source")" && pwd)/$(basename -- "$runtime_source")")"
elif [ -n "${LIP_PACKAGE_NODE_RUNTIME:-}" ]; then
  runtime_source="$LIP_PACKAGE_NODE_RUNTIME"
  [ -e "$runtime_source" ] || fail "private Node runtime $runtime_source does not exist"
  runtime_kind="supplied-runtime"
  runtime_label="$(basename -- "$runtime_source")"
  runtime_license="$(find_node_license "$runtime_source")"
else
  command -v node >/dev/null 2>&1 ||
    fail 'no private Node runtime source: pass --node-dist <distribution>, --node-runtime <executable>, or set LIP_PACKAGE_NODE_RUNTIME'
  runtime_source="$(command -v node)"
  runtime_kind="path-fallback"
  runtime_label="$(basename -- "$runtime_source")"
  runtime_license="$(find_node_license "$runtime_source")"
fi

private_runtime_path="$staging/$private_runtime"
mkdir -p "$(dirname -- "$private_runtime_path")"
cp "$runtime_source" "$private_runtime_path"
chmod 0755 "$private_runtime_path"
cp "$runtime_license" "$staging/$licenses_dir/nodejs-LICENSE"
cp "$repo_root/LICENSE" "$staging/$licenses_dir/plugin-LICENSE"
node_version="$("$private_runtime_path" --version)"

# The runtime ships with its own npm, and so does this archive. Without it the
# provisioning command would need a global package manager, which is the prerequisite
# the private runtime exists to remove. It is resolved from the same installation the
# runtime came from, so an archive carries the npm that belongs to its runtime rather
# than whatever npm happens to be nearest on the build machine.
npm_source=""
for npm_root in "$(dirname -- "$runtime_source")" "$(dirname -- "$(dirname -- "$runtime_source")")"; do
  if [ -d "$npm_root/$npm_rel" ]; then
    npm_source="$npm_root/$npm_rel"
    break
  fi
done
[ -n "$npm_source" ] ||
  fail "no bundled npm found at $npm_rel beside $runtime_source; stage an official Node distribution with --node-dist so the archive ships the npm that provisions the Cursor SDK"
mkdir -p "$(dirname -- "$staging/$private_npm_root")"
cp -R "$npm_source" "$staging/$private_npm_root"

# 5. Closed host manifest and release metadata, both derived from the staged tree
#    rather than described independently of it. The renderer refuses to describe a tree
#    carrying third-party package code, so a packager that staged one fails here rather
#    than publishing a bundle it must not ship.
exe_digest="$(sha256_of "$outer_exe")"
(cd "$repo_root" && GOWORK=off go run ./cmd/lip-cursor-sdk-packaging render \
  -repo "$repo_root" -staging "$staging" -platform "$layout_platform" \
  -exe-sha256 "$exe_digest" -node-source-kind "$runtime_kind" -node-source "$runtime_label" >/dev/null)

write_third_party_notices "$staging/$licenses_dir/THIRD-PARTY-NOTICES.md" \
  "$staging/$bridge_package_lock" "$staging/$bridge_package_json" "$private_runtime_path" \
  "$node_version" "$runtime_kind" "$runtime_label" "$layout_platform" \
  "$sdk_provision_command" "$sdk_package_name"

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
printf 'node_source_kind: %s\n' "$runtime_kind"
printf 'node_source: %s\n' "$runtime_label"
printf 'cursor_sdk_bundled: no (operator-provisioned; %s is not redistributed)\n' "${sdk_package_name:-the Cursor SDK}"
printf 'sdk_provision_command: %s\n' "$sdk_provision_command"
printf 'file_count: %s\n' "$file_count"

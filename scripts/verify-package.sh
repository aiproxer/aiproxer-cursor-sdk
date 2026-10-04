#!/usr/bin/env bash
#
# Verify an assembled Cursor SDK plugin install tree.
#
# Reports the exact files of an install tree, the checksum of every shipped one of them,
# and the private runtime metadata, and fails when the tree does not match what the
# release claims. It checks that every required archive entry exists, that the shipped
# file set and the checksum record agree in both directions, that every shipped digest
# matches including the plugin-private files, that the closed host manifest carries the
# plugin's identity and export posture and claims only the platform the archive was
# assembled on, and that the shipped private runtime and the plugin-private bridge
# launcher actually run.
#
# The Cursor SDK is proprietary and is not redistributed, so an installed tree carries
# it only after the operator provisions it against the runtime the archive ships. Two
# tree states decide what a run means, and both are checked in full:
#
#   --tree-state installed (default) an installed tree. The operator-provisioned
#     dependency tree is outside the shipped checksum record - the plugin authenticates
#     what it ships, the operator authenticates what they provisioned - so its files are
#     neither "present but not listed" nor digested here. What has to hold instead is
#     the SDK requirement: the tree resolves the pinned SDK version, or the run fails
#     with the exact provisioning command.
#
#   --tree-state shipped a released archive nobody has provisioned yet. It must carry no
#     Cursor SDK and nothing that exists only to satisfy it, because shipping the SDK or
#     its dependency closure would assert a redistribution right nobody has verified.
#     The scope of that rule is the Cursor SDK and its closure, not the whole tree: the
#     archive does ship third-party package code, the private runtime's own bundled npm,
#     and that is expected rather than a finding.
#
# The archive layout is not restated here. It is read from
# cmd/lip-cursor-sdk-packaging, which reports internal/packagelayout.
#
# The repository is the parent of this script's directory, and there is no way to
# substitute another one. The layout contract and release.yaml below are inputs to the
# verdict, so an option that replaced them would let whoever passed it decide what an
# install tree is - the same hole as asking the working directory, with a switch. The
# verifier ships inside the repository it needs (this script, release.yaml and
# cmd/lip-cursor-sdk-packaging), so there is no caller the default does not already
# serve; a tree that lives outside a checkout is what --package-root is for.
#
# The private runtime is executed by absolute path. Nothing here requires a node on
# PATH: the private-runtime variant ships its own runtime, and a verification that
# needed a global Node would prove the opposite of what it claims.
#
# The checksums cover the shipped plugin-private files, but nothing here claims the
# host authenticates them: the host's manifest digest stays the authority for the outer
# executable only.
#
# Usage:
#   scripts/verify-package.sh --package-root DIR
#                             [--report FILE] [--expect-platform os/arch]
#                             [--tree-state shipped|installed]
set -euo pipefail

# usage prints the leading comment block of this script, so the help text cannot drift
# out of the file by an edit to the header.
usage() {
  awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$1"
}

package_root=""
report_path=""
expect_platform=""
tree_state=""

while [ "$#" -gt 0 ]; do
  case "$1" in
    --package-root) package_root="${2:-}"; shift 2 ;;
    --report) report_path="${2:-}"; shift 2 ;;
    --expect-platform) expect_platform="${2:-}"; shift 2 ;;
    --tree-state) tree_state="${2:-}"; shift 2 ;;
    -h|--help) usage "$0"; exit 0 ;;
    *) printf 'verify-package: unknown argument %s\n' "$1" >&2; exit 2 ;;
  esac
done

script_dir="$(cd -- "$(dirname -- "$0")" && pwd)"
repo_root="$(cd -- "$(dirname -- "$script_dir")" && pwd)"
[ -n "$package_root" ] || { printf 'verify-package: --package-root is required\n' >&2; exit 2; }
[ -d "$package_root" ] || { printf 'verify-package: %s is not an install directory\n' "$package_root" >&2; exit 2; }
package_root="$(cd -- "$package_root" && pwd)"

findings=()
report=()

finding() { findings+=("verify-package: FAIL: $1"); }
line() { report+=("$1"); }

# release_scalar reads one flat scalar out of release.yaml.
release_scalar() {
  sed -n "s/^$1:[[:space:]]*//p" "$repo_root/release.yaml" | head -n 1
}

# json_string reads one string field of a JSON object. A field the record does not carry
# reads as empty: `sed -n ... p` prints the capture only when the pattern matched, so an
# absent field cannot answer with the whole document. That matters twice over here - a
# report line would otherwise print the entire record, and a comparison would compare the
# record against itself.
json_string() {
  printf '%s' "$2" | tr -d '\n' | sed -n "s/.*\"$1\": *\"\([^\"]*\)\".*/\1/p"
}

# json_digest reads one recorded sha256 field. Anything that is not a digest - including
# no field at all - reads as not recorded, which is what the PowerShell verifier's field
# lookup decides too. A digest that is not recorded is not cross-checked, and the checksum
# record still covers the record itself.
json_digest() {
  case "$(json_string "$1" "$2")" in
    [0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]*) ;;
    *) return 0 ;;
  esac
  json_string "$1" "$2"
}

# json_scalar reads a scalar field, quoted or not. Booleans and numbers are not quoted,
# so they need their own reader, and like json_string this one prints only what it
# matched: an absent field is an absent field.
json_scalar() {
  printf '%s' "$2" | tr -d '\n' | sed -n "s/.*\"$1\": *\([^,}]*\).*/\1/p" | tr -d ' "' | tr -d '\r'
}

# json_list prints one element of a JSON array field per line, unquoted. An absent or
# empty array prints nothing, so a caller can tell "no element recorded" from "no such
# field" by whether anything came out. The array's own brackets are removed first, so an
# element is an element rather than a bracket hanging off the last one.
#
# The trailing `|| true` is load bearing: grep reports "nothing selected" as a failure,
# and this script runs under set -e with pipefail, so an empty array - which is exactly
# what an uncertified release records for tested_host_artifacts - would otherwise end the
# run before the report was printed. The callers read the output, not the status.
json_list() {
  printf '%s' "$2" | tr -d '\n' | sed -n "s/.*\"$1\": *\(\[[^]]*\]\).*/\1/p" | sed 's/^\[//; s/\]$//' |
    tr ',' '\n' | sed 's/^ *//; s/ *$//; s/^"//; s/"$//; s/\r$//' | grep -v '^$' || true
}

# add_list_lines reports each element of a JSON array field, prefixed, or one report
# line when the array holds nothing. It reads the elements through a here-document
# rather than a pipe so that it appends to this run's report instead of a subshell's.
add_list_lines() {
  local field="$1" record="$2" prefix="$3" fallback="$4" element
  local elements
  elements="$(json_list "$field" "$record")"
  if [ -z "$elements" ]; then
    line "$fallback"
    return
  fi
  while IFS= read -r element; do
    [ -n "$element" ] || continue
    line "$prefix$element"
  done <<EOF
$elements
EOF
}


# sha256_of digests one file.
sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
    return
  fi
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | cut -d' ' -f1
    return
  fi
  printf 'verify-package: no sha256sum or shasum on this machine\n' >&2
  exit 1
}

# probe runs one packaged executable by absolute path and prints its output.
probe() {
  "$@" 2>&1 || true
}

# probe_runs reports whether one packaged executable started and succeeded. A probe
# that cannot run is a finding, never a skip: a runtime that was staged but does not
# answer for its own version is not a working private runtime.
probe_runs() {
  "$@" >/dev/null 2>&1
}

host_os="$(GOWORK=off go env GOOS)"
host_arch="$(GOWORK=off go env GOARCH)"
host_platform="$host_os/$host_arch"

layout_json="$(cd "$repo_root" && GOWORK=off go run ./cmd/lip-cursor-sdk-packaging layout -platform "$host_platform")"
layout_report() {
  printf '%s' "$layout_json" | tr -d '\n' | sed "s/.*\"$1\": *\"\([^\"]*\)\".*/\1/"
}
layout_entries() {
  printf '%s' "$layout_json" | tr -d '\n' | sed "s/.*\"$1\": *\[\([^]]*\)\].*/\1/" |
    tr ',' '\n' | sed 's/^ *//; s/ *$//; s/^"//; s/"$//' | grep -v '^$'
}

compatibility_name="$(layout_report compatibility)"
manifest_name="$(layout_report manifest)"
checksums_name="$(layout_report checksums)"
licenses_dir="$(layout_report licenses_dir)"
private_prefix="$(layout_report private_prefix)"
provisioned_prefix="$(layout_report provisioned_prefix)"
checksum_separator="$(layout_report checksum_separator)"
outer_executable="$(layout_report outer_executable)"
launcher_rel="$(layout_report launcher)"
private_runtime="$(layout_report private_runtime)"
bridge_package_json="$(layout_report bridge_package_json)"
bridge_modules="$(layout_report bridge_modules)"
sdk_package_json="$(layout_report sdk_package_json)"
sdk_package_name="$(layout_report sdk_package_name)"
sdk_provision_command="$(layout_report sdk_provision_command)"
private_npm_cli="$(layout_report private_npm_cli)"

# The tree states are part of the layout contract too, so this script does not spell a
# state of its own: an unrecognized request is a usage error naming the states the
# archive contract knows.
[ -n "$tree_state" ] || tree_state="installed"
case " $(layout_entries tree_states | tr '\n' ' ') " in
  *" $tree_state "*) ;;
  *)
    printf 'verify-package: --tree-state %s is not one of: %s\n' \
      "$tree_state" "$(layout_entries tree_states | tr '\n' ' ')" >&2
    exit 2
    ;;
esac

record_path="$package_root/$compatibility_name"
record_json=""
archive_platform="$expect_platform"
if [ -f "$record_path" ]; then
  record_json="$(cat "$record_path")"
  [ -n "$archive_platform" ] || archive_platform="$(json_string platform "$record_json")"
fi
archive_platform="${archive_platform:-$host_platform}"
if [ "$archive_platform" != "$host_platform" ]; then
  printf 'verify-package: this install tree claims %s but verification runs %s; an archive can only be validated natively on its own platform\n' \
    "$archive_platform" "$host_platform" >&2
  exit 1
fi

# dir_mode prints the POSIX permission bits of a directory, or nothing when this
# platform does not expose them.
dir_mode() {
  stat -c '%a' "$1" 2>/dev/null || stat -f '%Lp' "$1" 2>/dev/null || printf ''
}

# path_key reduces a reported path to the form a comparison can use: forward
# slashes, no Windows extended-length prefix, and no trailing separator. A runtime
# reports process.execPath in whatever spelling its platform prefers, so comparing it
# to the staged path as written fails on a Windows tree for spelling reasons alone.
path_key() {
  printf '%s' "$1" | tr -d '\r' | tr '\\' '/' | sed -e 's|^//\?/||' -e 's|/*$||'
}

# same_path compares two paths the way the PowerShell verifier does. On a Windows
# tree the filesystem is case-insensitive, so a difference in case is not a different
# path; on POSIX it is.
same_path() {
  local left right system
  left="$(path_key "$1")"
  right="$(path_key "$2")"
  [ -n "$right" ] || return 1
  system="$(uname -s 2>/dev/null || printf '')"
  case "$system" in
    MINGW*|MSYS*|CYGWIN*|Windows_NT)
      [ "$(printf '%s' "$left" | tr 'A-Z' 'a-z')" = "$(printf '%s' "$right" | tr 'A-Z' 'a-z')" ]
      ;;
    *) [ "$left" = "$right" ]
      ;;
  esac
}

line "install root: $package_root"
line "tree state: $tree_state"

# Protected install ownership: a group- or world-writable plugin root lets any local
# user replace a checksummed companion after verification, which is exactly the
# mutation the checksum record exists to detect.
root_mode="$(dir_mode "$package_root")"
if [ -z "$root_mode" ]; then
  line 'install ownership: not machine-checkable here; install into a protected plugin root readable and writable only by its owner'
elif [ $(( 0$root_mode & 0022 )) -ne 0 ]; then
  finding "install root $package_root is writable beyond its owner (mode $root_mode); reinstall into a protected plugin root so no local user can replace a checksummed companion"
  line "install ownership: $root_mode (FAILED: writable beyond its owner)"
else
  line "install ownership: $root_mode (owner-only write required)"
fi

# 1. Required archive entries. A missing entry is an explicit prerequisite failure
#    that names the packaged location and the operator remedy.
while IFS= read -r rel; do
  [ -n "$rel" ] || continue
  if [ -e "$package_root/$rel" ]; then continue; fi
  finding "required archive entry is missing: $rel; reinstall the Cursor plugin package from a complete archive"
done < <(layout_entries required_entries)

# 2. Checksum record and file set. The record covers shipped files only, so the shipped
#    side still has to agree in both directions - an unlisted shipped file is
#    unaccounted-for content, a listed file that is gone is a broken install - while the
#    operator-provisioned tree is explicitly outside it: the plugin authenticates what
#    it ships and the operator authenticates what they provisioned. The provisioned
#    prefix is the whole of that scope, and it comes from the layout contract.
checksums_path="$package_root/$checksums_name"
checksum_order=()
mismatched=0
provisioned_count=0
if [ ! -f "$checksums_path" ]; then
  finding "checksum record is missing: $checksums_name; reinstall the Cursor plugin package from a complete archive"
else
  while IFS= read -r entry; do
    [ -n "$entry" ] || continue
    digest="${entry%%"$checksum_separator"*}"
    rel="${entry#*"$checksum_separator"}"
    case "$digest" in
      [0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]*) ;;
      *) finding "malformed checksum line: $entry"; continue ;;
    esac
    [ "${#digest}" -eq 64 ] || { finding "malformed checksum digest for $rel"; continue; }
    case " ${checksum_order[*]} " in
      *" $rel "*) finding "duplicate checksum line for $rel"; continue ;;
    esac
    # A record line for a provisioned file would claim the plugin authenticated the
    # operator's own npm resolution, which is the one claim the record scope does not
    # make.
    case "$rel" in
      "$provisioned_prefix"*)
        finding "checksum record lists the operator-provisioned $rel: $checksums_name covers shipped files only; the plugin authenticates what it ships, the operator authenticates what they provisioned"
        ;;
    esac
    checksum_order+=("$rel|$digest")
  done <"$checksums_path"
fi

while IFS= read -r -d '' rel; do
  [ "$rel" = "$checksums_name" ] && continue
  case "$rel" in
    "$provisioned_prefix"*)
      # Outside the shipped record by design, so it is counted and reported rather than
      # digested. In a shipped archive its presence at all is the finding: the archive
      # must carry no Cursor SDK and nothing that exists only to satisfy it.
      provisioned_count=$((provisioned_count + 1))
      if [ "$tree_state" = shipped ]; then
        finding "shipped archive contains the third-party package code $rel: the Cursor SDK is not redistributed, so the archive ships no $bridge_modules; the operator provisions it against the shipped runtime with: $sdk_provision_command"
      fi
      continue
      ;;
  esac
  recorded=""
  for entry in "${checksum_order[@]:-}"; do
    if [ "${entry%%|*}" = "$rel" ]; then recorded="${entry#*|}"; break; fi
  done
  if [ -z "$recorded" ]; then
    finding "file is present but not listed in $checksums_name: $rel; the archive does not account for this file"
    continue
  fi
  actual="$(sha256_of "$package_root/$rel")"
  if [ "$actual" != "$recorded" ]; then
    mismatched=$((mismatched + 1))
    finding "checksum mismatch for $rel: recorded $recorded, found $actual; reinstall the Cursor plugin package"
  fi
done < <(cd "$package_root" && find . -type f -print0 | sed -z 's|^\./||' | LC_ALL=C sort -z)

for entry in "${checksum_order[@]:-}"; do
  [ -n "$entry" ] || continue
  rel="${entry%%|*}"
  case "$rel" in
    "$provisioned_prefix"*) continue ;;
  esac
  if [ ! -f "$package_root/$rel" ]; then
    finding "file listed in $checksums_name is missing: ${rel}; reinstall the Cursor plugin package"
  fi
done

listed_count="${#checksum_order[@]}"
private_count=0
for entry in "${checksum_order[@]:-}"; do
  case "${entry%%|*}" in "$private_prefix"*) private_count=$((private_count + 1)) ;; esac
done
files_present="$(find "$package_root" -type f | wc -l | tr -d ' ')"
shipped_count=$((files_present - provisioned_count))

variant="$(json_string packaging_variant "$record_json")"
external_node="$(json_scalar external_node_required "$record_json")"
line "plugin: $(release_scalar plugin_id)"
line "platform: $archive_platform (natively assembled and validated)"
line "packaging variant: ${variant:-(no release metadata)}"
case "$external_node" in
  false) line 'external node required: no' ;;
  '') line 'external node required: unknown' ;;
  *) line 'external node required: yes' ;;
esac
line "node on PATH: not required; $private_runtime is the only runtime this tree starts"
line "files: $files_present present ($shipped_count shipped, $provisioned_count operator-provisioned), $listed_count checksummed, $mismatched checksum mismatch(es)"
line "plugin-private files checksummed: $private_count of $listed_count under $private_prefix"
line "checksums: $checksums_name (sha256, <digest>${checksum_separator}<install-root-relative path>)"
line "checksum record scope: shipped files only; $provisioned_prefix is operator-provisioned and outside the record, so the plugin authenticates what it ships and the operator authenticates what they provisioned"
line "host digest authority: $manifest_name sha256 covers $outer_executable only; nothing here claims the host authenticates companion files"

# 3. Closed host manifest: identity, export posture, and the platform claim.
manifest_path="$package_root/$manifest_name"
if [ -f "$manifest_path" ]; then
  manifest_json="$(cat "$manifest_path")"
  exe_rel="$(json_string executable "$manifest_json")"
  [ "$exe_rel" = "$outer_executable" ] ||
    finding "manifest executable is $exe_rel but this archive stages $outer_executable"
  if [ -f "$package_root/$outer_executable" ]; then
    actual="$(sha256_of "$package_root/$outer_executable")"
    if [ "$(json_string sha256 "$manifest_json")" != "$actual" ]; then
      finding "manifest sha256 does not match $outer_executable: the host would reject this install"
    else
      line "outer executable sha256: $actual"
    fi
  fi

  # Each platform entry is an object, so the array is split on the object braces
  # rather than on commas: splitting on commas would tear the objects apart.
  platforms_json="$(printf '%s' "$manifest_json" | tr -d '\n' |
    sed "s/.*\"platforms\": *\[\([^]]*\)\].*/\1/")"
  claimed=""
  while IFS= read -r entry; do
    case "$entry" in *'"os"'*) ;; *) continue ;; esac
    entry_os="$(printf '%s' "$entry" | sed "s/.*\"os\": *\"\([^\"]*\)\".*/\1/")"
    entry_arch="$(printf '%s' "$entry" | sed "s/.*\"arch\": *\"\([^\"]*\)\".*/\1/")"
    claimed="$claimed$entry_os/$entry_arch "
  done < <(printf '%s' "$platforms_json" | tr '}' '\n')
  claimed="${claimed% }"
  if [ "$claimed" != "$archive_platform" ]; then
    finding "manifest platforms claim $claimed but this archive was assembled for $archive_platform: only the natively validated platform may be claimed"
  fi

  for field in plugin_id version build_id; do
    declared="$(release_scalar "$field")"
    [ -n "$declared" ] || continue
    value="$(json_string "$field" "$manifest_json")"
    [ "$value" = "$declared" ] ||
      finding "manifest $field is $value but release.yaml declares $declared"
  done

  for pair in 'credential_mode:static' 'access_scope:local_only' 'process_sharing:per_instance' 'execution_class:agent_runtime'; do
    field="${pair%%:*}"
    want="${pair#*:}"
    got="$(printf '%s' "$manifest_json" | tr -d '\n' | sed "s/.*\"$field\": *\"\([^\"]*\)\".*/\1/")"
    [ "$got" = "$want" ] ||
      finding "manifest export $field is '$got' but the plugin declares '$want'"
  done
  line 'export posture: static, local_only, per_instance, agent_runtime'

  # The release metadata records the digest of the manifest bytes this archive carries.
  # It is recorded from the staged tree, so it is checked here rather than printed: a
  # record that describes a different manifest than the one beside it is not evidence
  # about this archive, and a recorded digest nothing ever compared is provenance
  # decoration.
  recorded_manifest_sha="$(json_digest manifest_sha256 "$record_json")"
  if [ -n "$recorded_manifest_sha" ]; then
    actual_manifest_sha="$(sha256_of "$manifest_path")"
    if [ "$recorded_manifest_sha" != "$actual_manifest_sha" ]; then
      finding "release metadata records manifest_sha256 $recorded_manifest_sha but the staged $manifest_name hashes to $actual_manifest_sha; the record does not describe this archive"
    else
      line "manifest sha256 (recorded, matches): $actual_manifest_sha"
    fi
  fi
fi

# 4. The SDK requirement, the release metadata, and the private runtime and bridge that
#    actually run.
#
#    The shipped bridge manifest is the authority for the pinned SDK version: the
#    release metadata is cross-checked against it rather than trusted on its own, because
#    the manifest is a shipped file the record cannot contradict. What the tree has to
#    resolve is the operator's own installation, which is outside the shipped record, so
#    it is checked as a requirement and never as a digest.
required_version=""
if [ -f "$package_root/$bridge_package_json" ]; then
  required_version="$(sed -n "s|.*\"$sdk_package_name\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*|\1|p" \
    "$package_root/$bridge_package_json" | head -n 1)"
fi
if [ -z "$required_version" ]; then
  finding "the shipped $bridge_package_json pins no $sdk_package_name version, so no SDK requirement can be verified; reinstall the Cursor plugin package from a complete archive"
  required_version="(unknown)"
fi
provisioned_version=""
if [ -f "$package_root/$sdk_package_json" ]; then
  provisioned_version="$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' \
    "$package_root/$sdk_package_json" | head -n 1)"
fi

if [ -n "$record_json" ]; then
  recorded_required="$(json_string cursor_sdk_required_version "$record_json")"
  case "$recorded_required" in
    [0-9]*)
      if [ "$recorded_required" != "$required_version" ]; then
        finding "release metadata records cursor_sdk_required_version $recorded_required but the shipped $bridge_package_json pins $required_version; the record does not describe this archive"
      fi
      ;;
  esac
  case "$(json_scalar cursor_sdk_bundled "$record_json")" in
    true)
      finding "release metadata records cursor_sdk_bundled true: the Cursor SDK is proprietary and is not redistributed, so no archive bundles it"
      ;;
  esac
  line "node engine required: $(json_string bridge_node_engine "$record_json")"
  line "private runtime source: $(json_string private_runtime_source "$record_json")"

  # Source identity, host contract pins, platform evidence, and the certification
  # posture are printed as the record states them, including when they are absent. An
  # auditor has to be able to read an archive's evidence off this report instead of
  # inferring it from a build log, and an absent fact has to read as absent rather than
  # as a line this script chose not to print.
  source_revision="$(json_string source_revision "$record_json")"
  line "source revision: ${source_revision:-not established; this record names no source revision}"
  line "source stamp evidence: $(json_string source_stamp_evidence "$record_json")"
  # The recorded cleanliness is tri-state and the three answers stay three answers: a
  # dirty tree, a clean tree, and a state nothing established. An absent field is the
  # third one, and reporting it as "no" would turn a missing measurement into a clean
  # build claim in the one place an operator would read it as one.
  case "$(json_scalar source_modified "$record_json")" in
    true) line 'source modified: yes (built from a work tree with uncommitted changes)' ;;
    false) line 'source modified: no (the build tree had no uncommitted changes)' ;;
    *) line 'source modified: unknown (not established: no source identity in this record, or the build tree state could not be read)' ;;
  esac
  line "host contracts pinned: $(json_string published_root_module "$record_json") $(json_string host_contract_root_version "$record_json"), $(json_string published_acp_module "$record_json") $(json_string host_contract_acp_version "$record_json")"
  add_list_lines declared_platforms "$record_json" 'declared platform: ' \
    'declared platforms: none recorded'
  add_list_lines declared_platforms_not_assembled "$record_json" \
    'declared platform this archive is not evidence for: ' \
    'declared platforms this archive is not evidence for: none; this archive is the only declared platform'

  # The certification posture is printed with the reason an uncertified record carries,
  # and the artifacts a certified one names. An uncertified record with no reason is a
  # finding rather than a printed blank, and a certified record that names no artifact
  # is one too: either state with nothing behind it is a claim the record cannot support.
  certification_state="$(json_string host_certification_state "$record_json")"
  certification_reason="$(json_string host_certification_reason "$record_json")"
  case "$certification_state" in
    certified)
      line 'host certification: certified against the host artifacts listed below'
      if [ -z "$(json_list tested_host_artifacts "$record_json")" ]; then
        finding 'release metadata declares host_certification certified but records no tested_host_artifacts; a certification with no artifact behind it is a claim, not evidence'
      fi
      ;;
    uncertified)
      line 'host certification: uncertified (no host artifact was certified against)'
      if [ -z "$certification_reason" ]; then
        finding 'release metadata declares host_certification uncertified with no host_certification_reason; an uncertified record has to say why'
      fi
      ;;
    '')
      finding 'release metadata records no host_certification_state; an archive has to declare whether it was certified against a host artifact'
      ;;
    *)
      finding "release metadata declares host certification '$certification_state'; the postures are certified and uncertified"
      ;;
  esac
  if [ -n "$certification_reason" ]; then
    line "host certification reason: $certification_reason"
  fi
  add_list_lines tested_host_artifacts "$record_json" 'tested host artifact sha256: ' \
    'tested host artifacts: none recorded'

  # What the record says about verification of this package. The packager writes the
  # record before verification can run, so the recorded state is not-performed and the
  # runs that have to be performed are named instead. This report is the evidence those
  # runs produce; a passing run never rewrites the record to claim it passed.
  verification_performed='no'
  [ "$(json_scalar package_verification_performed "$record_json")" = 'true' ] && verification_performed='yes'
  line "package verification recorded in this archive: $(json_string package_verification_state "$record_json") (performed: $verification_performed)"
  line "package verification shipped-tree run: $(json_string package_verification_shipped_command "$record_json")"
  line "package verification installed-tree run: $(json_string package_verification_installed_command "$record_json")"
fi

line "sdk provisioning command: $sdk_provision_command"
if [ "$tree_state" = shipped ]; then
  line "sdk: not redistributed, required $required_version at run time, provisioned: no (operator-provisioned; $private_npm_cli ships so the command above needs no global npm)"
else
  case "$provisioned_version" in
    '')
      finding "the Cursor SDK is not provisioned: $sdk_package_json not found in this installed tree, and this archive ships no $sdk_package_name; required version $required_version. Provision it once with: $sdk_provision_command"
      ;;
    "$required_version")
      line "sdk: required $required_version, provisioned $provisioned_version"
      ;;
    *)
      finding "the provisioned Cursor SDK is $provisioned_version but the shipped $bridge_package_json pins $required_version; provision the pinned version with: $sdk_provision_command"
      ;;
  esac
fi

if [ -n "$record_json" ]; then
  private_runtime_path="$package_root/$private_runtime"
  launcher_path="$package_root/$launcher_rel"

  # The same applies to the runtime digest: it is recorded from the staged executable,
  # so it is compared against that executable rather than reported as provenance.
  recorded_runtime_sha="$(json_digest private_runtime_sha256 "$record_json")"
  if [ -n "$recorded_runtime_sha" ] && [ -f "$private_runtime_path" ]; then
    actual_runtime_sha="$(sha256_of "$private_runtime_path")"
    if [ "$recorded_runtime_sha" != "$actual_runtime_sha" ]; then
      finding "release metadata records private_runtime_sha256 $recorded_runtime_sha but the staged $private_runtime hashes to $actual_runtime_sha; the record does not describe this archive"
    else
      line "private runtime sha256 (recorded, matches): $actual_runtime_sha"
    fi
  fi

  if [ -f "$private_runtime_path" ] && [ -f "$launcher_path" ]; then
    self_path="$(probe "$private_runtime_path" -p 'process.execPath')"
    resolved='no'
    if same_path "$self_path" "$private_runtime_path"; then resolved='yes'; fi
    line "private runtime: $private_runtime_path"
    line "private runtime resolves to itself: $resolved"
    [ "$resolved" = 'yes' ] ||
      finding "the staged private runtime resolved to '$self_path' instead of $private_runtime_path"
    if ! probe_runs "$private_runtime_path" --version; then
      finding "the staged private runtime did not run: $(probe "$private_runtime_path" --version)"
    fi
    line "private runtime version: $(probe "$private_runtime_path" --version) (recorded $(json_string private_runtime_version "$record_json"))"
    components="$(probe "$private_runtime_path" -p 'JSON.stringify(process.versions)')"
    summary=""
    for name in node icu openssl uv zlib; do
      value="$(printf '%s' "$components" | sed "s/.*\"$name\": *\"\([^\"]*\)\".*/\1/")"
      [ -n "$value" ] && [ "$value" != "$components" ] && summary="$summary $name=$value"
    done
    line "private runtime bundled components:${summary# }"

    # The launcher starts the packaged runtime and runs the bridge's own doctor, which
    # resolves the installed SDK. That is an installed-tree question: on a shipped
    # archive the bridge cannot serve a request until the operator provisions it, and
    # the SDK requirement above is the finding that says so.
    if [ "$tree_state" = shipped ]; then
      line "bridge doctor: not run; a shipped archive cannot serve a request until the operator provisions $bridge_modules"
    else
      doctor="$(probe "$launcher_path" doctor)"
      printf '%s' "$doctor" | grep -q 'doctor: ok' ||
        finding "the private bridge launcher did not pass doctor through the private runtime: $doctor"
      line "bridge doctor (launcher -> private runtime -> bridge entry): $doctor"
    fi
  else
    finding 'the private runtime or the private bridge launcher is missing, so no runtime metadata could be probed'
  fi
fi

# An empty directory is a finding, not a reported count of zero. The archive is
# required to carry the runtime's license and provenance notices; the packager fails
# without them, and a verifier that only prints how many it found would report a tree
# carrying none as evidence that it looked.
license_count="$(find "$package_root/$licenses_dir" -maxdepth 1 -type f 2>/dev/null | wc -l | tr -d ' ')"
line "licenses: $license_count notices under $licenses_dir/"
if [ "$license_count" -eq 0 ]; then
  finding "$licenses_dir carries no license or provenance notice; an archive that redistributes a private Node runtime has to ship its notices"
fi
line '--- files ---'
for entry in "${checksum_order[@]:-}"; do
  [ -n "$entry" ] || continue
  line "${entry#*|}$checksum_separator${entry%%|*}"
done
line '--- end of files ---'

text="$(printf '%s\n' "${report[@]}")"
if [ -n "$report_path" ]; then
  printf '%s\n' "$text" >"$report_path"
fi
printf '%s\n' "$text"
for item in "${findings[@]:-}"; do
  [ -n "$item" ] && printf '%s\n' "$item"
done
if [ "${#findings[@]}" -gt 0 ]; then
  printf 'verify-package: %d finding(s); the install tree does not match the release it claims\n' "${#findings[@]}"
  exit 1
fi
printf 'verify-package: ok\n'

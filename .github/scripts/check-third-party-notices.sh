#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Compares the chart table in nktp/THIRD_PARTY_NOTICES.md with the charts that
# `helm package` bundles: each dependency in nktp/charts/ and every subchart
# inside it, at any depth. Run `helm dependency build nktp` first.
#
# A row is `| <chart> | <version> |`, where a subchart is `<parent> / <chart>`
# and a deeper one `<parent> / <child> / <chart>`. A subchart published at
# 0.0.0 has no release of its own, so its row reads
# `bundled in <parent> <parent version>` instead, naming its direct parent.

set -euo pipefail

if [[ "${1:-}" != "" && "${1:-}" != "--update" ]] || [[ "$#" -gt 1 ]]; then
  echo "Usage: $0 [--update]" >&2
  exit 2
fi

cd "$(git rev-parse --show-toplevel)"

notices=nktp/THIRD_PARTY_NOTICES.md
work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT

shopt -s nullglob
archives=(nktp/charts/*.tgz)
if [[ "${#archives[@]}" -eq 0 ]]; then
  echo "::error::nktp/charts/ has no dependency archives; run helm dependency build nktp"
  exit 1
fi

field() {
  awk -v key="$1" 'index($0, key ":") == 1 { gsub(/["'\'']/, "", $2); print $2; exit }' "$2"
}

# Prints the chart directory inside a dependency archive.
unpack() {
  local dest
  dest="$(mktemp -d "${work}/unpacked.XXXXXX")"
  tar -xzf "$1" -C "${dest}"
  dirname "$(printf '%s\n' "${dest}"/*/Chart.yaml | head -n 1)"
}

# Records the subcharts of chart directory $1, whose table path is $2, then
# descends into each. Helm keeps a subchart either unpacked or as a .tgz.
walk() {
  local dir="$1" path="$2" name version sub sub_dir sub_name sub_version
  name="$(field name "${dir}/Chart.yaml")"
  version="$(field version "${dir}/Chart.yaml")"
  for sub in "${dir}"/charts/*; do
    if [[ "${sub}" == *.tgz ]]; then
      sub_dir="$(unpack "${sub}")"
    elif [[ -f "${sub}/Chart.yaml" ]]; then
      sub_dir="${sub}"
    else
      continue
    fi
    sub_name="$(field name "${sub_dir}/Chart.yaml")"
    sub_version="$(field version "${sub_dir}/Chart.yaml")"
    if [[ "${sub_version}" == 0.0.0 ]]; then
      echo "| ${path} / ${sub_name} | bundled in ${name} ${version} |" >>"${work}/expected"
    else
      echo "| ${path} / ${sub_name} | ${sub_version} |" >>"${work}/expected"
    fi
    walk "${sub_dir}" "${path} / ${sub_name}"
  done
}

: >"${work}/expected"
for archive in "${archives[@]}"; do
  chart_dir="$(unpack "${archive}")"
  parent="$(field name "${chart_dir}/Chart.yaml")"
  echo "| ${parent} | $(field version "${chart_dir}/Chart.yaml") |" >>"${work}/expected"
  walk "${chart_dir}" "${parent}"
done
sort -u -o "${work}/expected" "${work}/expected"

if [[ "${1:-}" == "--update" ]]; then
  # Update only versions for charts already listed. New or removed charts need
  # a human to review their copyright, license, source, and NOTICE files.
  awk -F'|' '
    function trim(value) {
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", value)
      return value
    }
    NR == FNR {
      name = trim($2)
      version[name] = trim($3)
      next
    }
    /^\|/ && trim($2) != "Chart" && trim($2) !~ /^-+$/ {
      name = trim($2)
      if (!(name in version) || seen[name]++) {
        print "Review added, removed, or duplicate chart: " name > "/dev/stderr"
        failed = 1
      } else {
        $3 = " " version[name] " "
      }
    }
    { print }
    END {
      for (name in version) {
        if (!seen[name]) {
          print "Review newly bundled chart: " name > "/dev/stderr"
          failed = 1
        }
      }
      if (failed) exit 1
    }
  ' OFS='|' "${work}/expected" "${notices}" >"${work}/updated"
  if ! cmp -s "${work}/updated" "${notices}"; then
    cp "${work}/updated" "${notices}"
    echo "Updated chart versions in ${notices}; review its diff before committing."
  fi
fi

awk -F'|' '
  /^\|/ && $2 !~ /^[[:space:]]*(Chart|-+)[[:space:]]*$/ {
    gsub(/^[[:space:]]+|[[:space:]]+$/, "", $2)
    gsub(/^[[:space:]]+|[[:space:]]+$/, "", $3)
    print "| " $2 " | " $3 " |"
  }
' "${notices}" | sort -u >"${work}/listed"

status=0
while IFS= read -r row; do
  echo "::error file=${notices}::bundled but not listed: ${row}"
  status=1
done < <(comm -23 "${work}/expected" "${work}/listed")
while IFS= read -r row; do
  echo "::error file=${notices}::listed but not bundled: ${row}"
  status=1
done < <(comm -13 "${work}/expected" "${work}/listed")

if [[ "${status}" -eq 0 ]]; then
  echo "${notices} lists all $(wc -l <"${work}/expected" | tr -d ' ') bundled charts."
else
  echo "Run task license:update-notices for version changes, then review ${notices}."
fi
exit "${status}"

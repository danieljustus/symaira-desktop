#!/bin/bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

VERSION="${1:?Usage: $0 <version> <output-path>}"
OUTPUT="${2:?Usage: $0 <version> <output-path>}"
VERSION="${VERSION#v}"

SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct HEAD)}"
BUILD_DATE=$(date -u -r "$SOURCE_DATE_EPOCH" +%Y-%m-%dT%H:%M:%SZ)

CLI_ARCHIVE="dist/symmeet_v${VERSION}_darwin_arm64.tar.gz"
if [ ! -f "$CLI_ARCHIVE" ]; then
  echo "Error: ${CLI_ARCHIVE} not found. Run package-cli.sh first." >&2
  exit 1
fi
CLI_SHA=$(shasum -a 256 "$CLI_ARCHIVE" | awk '{print $1}')

PINS=()
while IFS= read -r line; do
  PINS+=("$line")
done < <(python3 -c "
import json
with open('Package.resolved') as f:
    data = json.load(f)
for pin in sorted(data['pins'], key=lambda p: p['identity']):
    state = pin['state']
    print(f\"{pin['identity']}|{pin['location']}|{state.get('version', 'unknown')}|{state.get('revision', '')}\")
")

ARG_MAX_ENTRY=""
ARG_PARSER_ENTRY=""
for pin in "${PINS[@]}"; do
  IFS='|' read -r identity location version revision <<< "$pin"
  case "$identity" in
    argmax-oss-swift)      ARG_MAX_ENTRY="$identity|$location|$version|$revision" ;;
    swift-argument-parser) ARG_PARSER_ENTRY="$identity|$location|$version|$revision" ;;
  esac
done

if [ -z "$ARG_MAX_ENTRY" ] || [ -z "$ARG_PARSER_ENTRY" ]; then
  echo "Error: expected pins argmax-oss-swift and swift-argument-parser in Package.resolved" >&2
  exit 1
fi

IFS='|' read -r AP_NAME AP_LOC AP_VER AP_REV <<< "$ARG_PARSER_ENTRY"
IFS='|' read -r AM_NAME AM_LOC AM_VER AM_REV <<< "$ARG_MAX_ENTRY"

APPKIT_VER=$(grep -A2 'symaira-appkit:' project.yml | grep 'exactVersion' | awk '{print $2}' | tr -d '"')
if [ -z "$APPKIT_VER" ]; then
  echo "Error: could not determine symaira-appkit version from project.yml" >&2
  exit 1
fi

cat > "$OUTPUT" <<SPDX
{
  "spdxVersion": "SPDX-2.3",
  "dataLicense": "CC0-1.0",
  "SPDXID": "SPDXRef-DOCUMENT",
  "name": "symmeet-${VERSION}",
  "documentNamespace": "https://github.com/danieljustus/symaira-meet/symmeet-${VERSION}",
  "creationInfo": {
    "created": "${BUILD_DATE}",
    "creators": [
      "Tool: symaira-meet-release"
    ],
    "licenseListVersion": "3.25"
  },
  "packages": [
    {
      "SPDXID": "SPDXRef-Package-symmeet",
      "name": "symmeet",
      "versionInfo": "${VERSION}",
      "supplier": "NOASSERTION",
      "downloadLocation": "https://github.com/danieljustus/symaira-meet",
      "filesAnalyzed": false,
      "checksums": [
        {
          "algorithm": "SHA256",
          "checksumValue": "${CLI_SHA}"
        }
      ],
      "licenseDeclared": "Apache-2.0",
      "copyrightText": "Copyright 2024 Daniel Justus",
      "comment": "SHA256 of symmeet_v${VERSION}_darwin_arm64.tar.gz"
    },
    {
      "SPDXID": "SPDXRef-Package-swift-argument-parser",
      "name": "swift-argument-parser",
      "versionInfo": "${AP_VER}",
      "supplier": "NOASSERTION",
      "downloadLocation": "${AP_LOC}",
      "filesAnalyzed": false,
      "checksums": [
        {
          "algorithm": "SHA1",
          "checksumValue": "${AP_REV}"
        }
      ],
      "licenseDeclared": "Apache-2.0",
      "copyrightText": "Copyright 2020 Apple Inc.",
      "comment": "SHA1 is the pinned git revision from Package.resolved"
    },
    {
      "SPDXID": "SPDXRef-Package-argmax-oss-swift",
      "name": "argmax-oss-swift",
      "versionInfo": "${AM_VER}",
      "supplier": "NOASSERTION",
      "downloadLocation": "${AM_LOC}",
      "filesAnalyzed": false,
      "checksums": [
        {
          "algorithm": "SHA1",
          "checksumValue": "${AM_REV}"
        }
      ],
      "licenseDeclared": "MIT",
      "copyrightText": "Copyright 2024 Argmax, Inc.",
      "comment": "Includes WhisperKit and SpeakerKit products; SHA1 is the pinned git revision from Package.resolved"
    },
    {
      "SPDXID": "SPDXRef-Package-symaira-appkit",
      "name": "symaira-appkit",
      "versionInfo": "${APPKIT_VER}",
      "supplier": "NOASSERTION",
      "downloadLocation": "https://github.com/danieljustus/symaira-appkit.git",
      "filesAnalyzed": false,
      "licenseDeclared": "Apache-2.0",
      "copyrightText": "Copyright 2024 Daniel Justus",
      "comment": "Agent app dependency from project.yml"
    },
    {
      "SPDXID": "SPDXRef-Package-whisperkit-coreml-models",
      "name": "whisperkit-coreml-models",
      "versionInfo": "runtime",
      "supplier": "NOASSERTION",
      "downloadLocation": "https://huggingface.co/argmaxinc/whisperkit-coreml",
      "filesAnalyzed": false,
      "licenseDeclared": "MIT",
      "copyrightText": "Copyright 2024 Argmax, Inc.",
      "comment": "CoreML speech models downloaded at runtime from HuggingFace"
    }
  ],
  "relationships": [
    {
      "spdxElementId": "SPDXRef-DOCUMENT",
      "relatedSpdxElement": "SPDXRef-Package-symmeet",
      "relationshipType": "DESCRIBES"
    },
    {
      "spdxElementId": "SPDXRef-Package-symmeet",
      "relatedSpdxElement": "SPDXRef-Package-swift-argument-parser",
      "relationshipType": "DEPENDS_ON"
    },
    {
      "spdxElementId": "SPDXRef-Package-symmeet",
      "relatedSpdxElement": "SPDXRef-Package-argmax-oss-swift",
      "relationshipType": "DEPENDS_ON"
    },
    {
      "spdxElementId": "SPDXRef-Package-symmeet",
      "relatedSpdxElement": "SPDXRef-Package-symaira-appkit",
      "relationshipType": "DEPENDS_ON"
    },
    {
      "spdxElementId": "SPDXRef-Package-symmeet",
      "relatedSpdxElement": "SPDXRef-Package-whisperkit-coreml-models",
      "relationshipType": "DEPENDS_ON"
    }
  ]
}
SPDX

echo "SBOM written to ${OUTPUT}"

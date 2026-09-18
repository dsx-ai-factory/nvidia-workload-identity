#!/bin/bash
set -euo pipefail

: "${RC_TAG:?RC_TAG is required}"
: "${RC_VERSION:?RC_VERSION is required}"
: "${GITHUB_SHA:?GITHUB_SHA is required}"
node .github/rc/validate-version.cjs
[[ "$RC_TAG" == "v${RC_VERSION}" ]]
[[ "$(git rev-parse "${RC_TAG}^{commit}")" == "$GITHUB_SHA" ]]

# semantic-release 25 stores channel metadata in a separate ref for each tag.
note_ref="refs/notes/semantic-release-${RC_TAG}"
if ! git fetch --no-tags origin "+${note_ref}:${note_ref}"; then
  echo "Existing RC has no readable channel metadata. Ask a maintainer to repair the original release before retrying. No tag was changed." >&2
  exit 1
fi
git notes --ref="semantic-release-${RC_TAG}" show "$RC_TAG" | node -e '
let input = "";
process.stdin.setEncoding("utf8");
process.stdin.on("data", chunk => { input += chunk; });
process.stdin.on("end", () => {
  try {
    const note = JSON.parse(input);
    if (!Array.isArray(note.channels) || !note.channels.includes("rc")) {
      throw new Error("missing rc channel");
    }
  } catch {
    console.error("Existing RC channel metadata is invalid. A maintainer must repair it before retrying.");
    process.exitCode = 1;
  }
});
'

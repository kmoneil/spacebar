#!/bin/sh
# Print the version this build should carry.
#
# A build is a release only when it sits exactly on a semver tag. Everything
# else says so in its own version string rather than borrowing the last tag's,
# because a binary that reports 0.2.0 when it is 0.2.0 plus eleven commits is
# unpinnable: a bug report naming that version describes eleven different
# builds.
#
# A tag that is not semver is refused rather than cleaned up. `make build` on
# `v1.2` would otherwise publish something no version constraint can match, and
# the failure would surface at the far end, in somebody's dependency solver.
set -eu

cd "$(dirname "$0")/.."

# semver, without the build metadata field: nothing here produces one, and
# accepting a field we never emit is a rule that has never been tested.
semver='^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'

if ! git rev-parse --git-dir >/dev/null 2>&1; then
	# A source tarball with no .git. Not a failure: it still builds, it just
	# cannot know what it is.
	echo "0.0.0-unknown"
	exit 0
fi

if tag=$(git describe --tags --exact-match 2>/dev/null); then
	version=${tag#v}
	if ! printf '%s' "$version" | grep -Eq "$semver"; then
		echo "scripts/version.sh: '$tag' is not a semver tag (want vX.Y.Z)" >&2
		exit 1
	fi
	# --dirty is checked separately: `git describe --exact-match` ignores it,
	# and a release built from a modified tree is not the tag it claims.
	if ! git diff --quiet HEAD 2>/dev/null; then
		printf '%s-dirty\n' "$version"
		exit 0
	fi
	printf '%s\n' "$version"
	exit 0
fi

# Not on a tag. Describe against the last one if there is one, so the string
# still says where in history this build came from.
sha=$(git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
dirty=""
git diff --quiet HEAD 2>/dev/null || dirty="-dirty"

if last=$(git describe --tags --abbrev=0 --match 'v[0-9]*' 2>/dev/null); then
	count=$(git rev-list --count "$last"..HEAD 2>/dev/null || echo 0)
	printf '%s-dev.%s+%s%s\n' "${last#v}" "$count" "$sha" "$dirty"
else
	printf '0.0.0-dev+%s%s\n' "$sha" "$dirty"
fi

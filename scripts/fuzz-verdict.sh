#!/bin/sh
# Classify a failed fuzz run.
#
# `go test -fuzz` exits non-zero for two quite different reasons and reports
# them almost identically. One is a find: an input reached a panic, a
# difference, or a failed property, and Go wrote it under testdata/fuzz. The
# other is the fuzzing engine itself falling over: a worker that died without a
# crasher, a timeout in the coordinator, golang/go#75804. None of those say
# anything about the code under test.
#
# Treating the second as the first is how a nightly sweep teaches people to
# press re-run, and a habit of pressing re-run is how a real crasher gets waved
# through. So the verdict is stated out loud, in the job log, rather than
# inferred from an exit code by whoever is reading it at the time.
#
# Usage: scripts/fuzz-verdict.sh <log-file> <package-dir>
# Exits 1 on a find, 0 on an engine failure.
set -eu

log=${1:?usage: fuzz-verdict.sh <log-file> <package-dir>}
pkgdir=${2:-.}

if [ ! -f "$log" ]; then
	echo "fuzz-verdict: no log at $log" >&2
	exit 1
fi

# A find always leaves the input on disk. That is the strongest signal
# available and it does not depend on matching Go's wording, which changes.
#
# Asked of git rather than of mtimes: the log is written by `tee` for the whole
# run, so its own timestamp is the end of the run and every crasher written
# during it is older. A file that is untracked is a file this run produced.
if [ -n "$(git status --porcelain -- "$pkgdir/testdata/fuzz" 2>/dev/null)" ]; then
	echo "verdict: FIND"
	echo "A new input is under $pkgdir/testdata/fuzz."
	echo "Commit it, and add it as an f.Add seed in the same change so the"
	echo "regression is visible in the test source and not only in a corpus file."
	exit 1
fi

if grep -q "Failing input written to" "$log"; then
	echo "verdict: FIND"
	echo "Go wrote a failing input."
	exit 1
fi

# Known engine failures. Each one is a symptom of the harness rather than of
# the target, and each is listed so that adding to this set is a deliberate
# edit somebody has to justify.
for pattern in \
	"fuzzing process terminated without fuzzing a value" \
	"fuzzing process hung or terminated unexpectedly: exit status" \
	"failed to start fuzzing process" \
	"waiting for fuzzing process"; do
	if grep -q "$pattern" "$log"; then
		echo "verdict: ENGINE"
		echo "Matched '$pattern'."
		echo "This is the fuzzing harness, not the code under test. See golang/go#75804."
		exit 0
	fi
done

echo "verdict: FIND"
echo "The run failed and nothing identifies it as an engine fault."
echo "Read the log. If this is a new engine failure mode, add it to this script"
echo "rather than re-running until it passes."
exit 1

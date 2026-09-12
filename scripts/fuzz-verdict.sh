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

# The coordinator's own deadline, reported as a failure.
#
# A run that ends `--- FAIL: FuzzX (601.02s)` with `context deadline exceeded`
# under it and no input on disk is the -fuzztime budget running out, not a
# find. The coordinator wakes on its parent context's Done channel and asks
# whether the error it just read is the one it uses to stop its workers. That
# worker context is a child of the parent, and a parent closes its own channel
# before it cancels its children, so a coordinator scheduled inside that window
# reads no error from the child, decides the deadline was somebody else's, and
# records it. Go's own CI hits it: golang/go#72088 and golang/go#72104 are
# one-second fuzz runs failing at 1.10s with exactly this line.
#
# Matched on the whole line rather than as a substring, and listed apart from
# the patterns above because it is the only one that needs to be. It is safe
# because the crasher check has already run: a deadline that lands while a
# crash is being minimized still writes the input to testdata/fuzz, so that
# case is a find before it ever reaches here.
if grep -qE '^[[:space:]]*context deadline exceeded$' "$log"; then
	echo "verdict: ENGINE"
	echo "The fuzz budget ran out and the coordinator reported its own deadline"
	echo "as a failure. Nothing was written under $pkgdir/testdata/fuzz, which"
	echo "a find always does. See golang/go#72088."
	exit 0
fi

echo "verdict: FIND"
echo "The run failed and nothing identifies it as an engine fault."
echo "Read the log. If this is a new engine failure mode, add it to this script"
echo "rather than re-running until it passes."
exit 1

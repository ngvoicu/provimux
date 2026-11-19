# First pass of testsum.jq: from raw `go test -json` lines (jq -R -c), keep only the
# result events, with the fields testsum.jq reads.
(try fromjson catch null)
| select(type == "object" and (.Action == "pass" or .Action == "fail" or .Action == "skip" or .Action == "build-fail"))
| {Action, Package, Test, ImportPath, FailedBuild}

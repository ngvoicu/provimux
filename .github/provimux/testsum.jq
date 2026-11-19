# Summarise the output of `go test -json` (read raw: jq -R -s -f testsum.jq LOG).
# Lines that are not JSON are ignored. Result:
#   term:         {package: "pass"|"fail"|"skip"}, the package's terminal event
#   build_failed: packages whose test binary (or a dependency) did not build
#   failed:       ["<package> <test>"] failing tests; "<package> [package]" for a package
#                 that failed without a failing test (panic outside a test, TestMain, timeout)
#   passed:       ["<package> <test>"] passing tests
[splits("\n") | select(startswith("{")) | (try fromjson catch null) | select(type == "object")] as $ev
| ($ev | map(select(.Test == null and .Package != null
                    and (.Action == "pass" or .Action == "fail" or .Action == "skip")))
       | map({key: .Package, value: .Action}) | from_entries) as $term
| (([$ev[] | select(.Action == "build-fail") | (.ImportPath // "") | sub(" \\[.*$"; "")]
    + [$ev[] | select(.Test == null and .Action == "fail" and ((.FailedBuild // "") != "")) | .Package])
   | map(select(. != "")) | unique) as $bf
| ([$ev[] | select(.Test != null and .Action == "fail") | "\(.Package) \(.Test)"] | unique) as $ft
| ($ft | map(split(" ")[0]) | unique) as $ftpkgs
| ([$term | to_entries[] | select(.value == "fail") | .key
    | select(. as $p | ($bf | index($p)) == null and ($ftpkgs | index($p)) == null)
    | "\(.) [package]"]) as $pkgfail
| {
    term: $term,
    build_failed: $bf,
    failed: ($ft + $pkgfail | unique),
    passed: ([$ev[] | select(.Test != null and .Action == "pass") | "\(.Package) \(.Test)"] | unique)
  }

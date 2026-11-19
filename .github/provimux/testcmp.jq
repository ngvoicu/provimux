# Compare the candidate's tests with the pristine upstream tag's (check.sh verdict).
# jq -n --slurpfile cand C.json --slurpfile pris P.json --slurpfile rerun R.json
#       --slurpfile cpkgs CP.json --slurpfile ppkgs PP.json
#       --argjson cexit N --argjson pexit N --argjson rexit N|null -f testcmp.jq
# cand/pris/rerun: testsum.jq summaries of raw `go test -json` output (rerun = the
# candidate's packages with a failing test, run again whole, in one go test); cpkgs/ppkgs:
# the package lists (`go list ./...`) of each tree; cexit/pexit/rexit: go test's exit
# status on each run (rexit null: no rerun ran; -1: no valid exit status).
#
# The candidate's failures are those of the first run AND those of the rerun (a test
# the first run never reached, because an earlier test stopped the package, and that
# fails on the rerun is a failure). Each fails the check unless one of two waivers
# holds, and nothing else waives it:
#   - baseline: the SAME named test (package and test name) failed on the pristine tag.
#               Never for a package-level failure ("<package> [package]": build, setup,
#               TestMain, a panic outside a test): an upstream failure does not show it
#               is the same one;
#   - flaky:    the whole-package rerun has an explicit pass for it: the rerun exited
#               with a valid status (0 with no failing package, 1 with one), its package
#               has a terminal result and built, and the test passed and did not fail
#               (for "[package]": the package passed). A test that failed on the rerun
#               never has one.
# A package that does not build on the candidate, or on the rerun, fails the check (an
# upstream failure never waives that), and so does a package
# without a terminal result on either tree or an exit status that does not match.
$cand[0] as $c | $pris[0] as $p | $rerun[0] as $r | $cpkgs[0] as $cp | $ppkgs[0] as $pp
| def pkg: split(" ")[0];
  def testname: split(" ")[1:] | join(" ");
  def missing($s; $pkgs): [$pkgs[] | select($s.term[.] == null)];
  def nfail($s): [$s.term[] | select(. == "fail")] | length;
  def exitok($s; $e): ($e == 0 and nfail($s) == 0) or ($e == 1 and nfail($s) > 0);
  def exitcheck($name; $s; $e):
    if ($e != 0 and $e != 1) then ["go test on \($name) exited \($e)"]
    elif ($e == 0 and nfail($s) > 0) then ["go test on \($name) exited 0 but \(nfail($s)) package(s) failed"]
    elif ($e == 1 and nfail($s) == 0) then ["go test on \($name) exited 1 without a failing package"]
    else [] end;
  ($rexit != null and exitok($r; $rexit)) as $rerun_valid
| def baseline($f): ($f | testname) != "[package]" and ($p.failed | index($f)) != null;
  def retry_pass($f):
    ($f | pkg) as $pk
    | $rerun_valid
      and $r.term[$pk] != null
      and ($r.build_failed | index($pk)) == null
      and (if ($f | testname) == "[package]" then $r.term[$pk] == "pass"
           else ($r.passed | index($f)) != null and ($r.failed | index($f)) == null end);
  [($c.failed + (if $rexit != null then $r.failed else [] end))[]
   | select(pkg as $pk | ($c.build_failed | index($pk)) == null)] | unique as $cf
| [$cf[] | select(baseline(.))] as $upstream
| [$cf[] | select((baseline(.) | not) and retry_pass(.))] as $flaky
| [$cf[] | select((baseline(.) | not) and (retry_pass(.) | not))] as $new
| ([$c.failed[] | select(pkg as $pk | ($c.build_failed | index($pk)) == null) | pkg] | unique) as $rerun_wanted
| missing($c; $cp) as $mc | missing($p; $pp) as $mp
| (exitcheck("the candidate"; $c; $cexit) + exitcheck("the tag"; $p; $pexit)
   + (if $rexit != null and ($rerun_valid | not) then ["the package rerun has no valid exit status (\($rexit)); no failure counts as flaky"] else [] end)
   + (if ($new | length) > 0 then ["\($new | length) test(s) fail on the candidate, not on the tag (or fail at package level), and have no explicit pass on the package rerun"] else [] end)
   + (if ($c.build_failed | length) > 0 then ["\($c.build_failed | length) package(s) do not build on the candidate"] else [] end)
   + (if $rexit != null and ($r.build_failed | length) > 0 then ["\($r.build_failed | length) package(s) do not build on the package rerun"] else [] end)
   + (if ($mc | length) > 0 then ["\($mc | length) package(s) without a result on the candidate"] else [] end)
   + (if ($mp | length) > 0 then ["\($mp | length) package(s) without a result on the tag"] else [] end)) as $reasons
| {
    ok: ($reasons | length == 0),
    reasons: $reasons,
    new_failures: $new,
    candidate_build_failures: $c.build_failed,
    failing_on_tag_too: $upstream,
    flaky: $flaky,
    missing_results: {candidate: $mc, tag: $mp},
    candidate: {exit: $cexit, packages: ($cp | length), passed: ($c.passed | length), failed: ($c.failed | length),
                failed_packages: nfail($c)},
    tag: {exit: $pexit, packages: ($pp | length), passed: ($p.passed | length), failed: ($p.failed | length),
          failed_packages: nfail($p)},
    rerun: {exit: $rexit, valid: $rerun_valid, wanted: $rerun_wanted, packages: ($r.term | keys),
            build_failed: $r.build_failed}
  }

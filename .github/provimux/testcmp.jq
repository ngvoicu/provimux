# Compare the candidate's tests with the pristine upstream tag's (check.sh).
# jq -n --slurpfile cand C.json --slurpfile pris P.json --slurpfile rerun R.json
#       --slurpfile cpkgs CP.json --slurpfile ppkgs PP.json
#       --argjson cexit N --argjson pexit N -f testcmp.jq
# cand/pris/rerun: testsum.jq summaries (rerun = the candidate's failing packages run
# again, each whole package, merged into one summary); cpkgs/ppkgs: the package lists
# (`go list ./...`) of each tree; cexit/pexit: go test's exit status on each tree.
#
# A failure fails the check when it failed on the candidate, failed again when its
# whole package was rerun (or the rerun gave no result), and did not fail on the tag:
# the test failed there too, or its package failed to build there or failed as a whole
# without the test passing. A test the tag does not have counts (it is the owner's).
# A package that fails to build on the candidate fails the check, and so does a package
# without a terminal result on either tree or an exit status that does not match.
$cand[0] as $c | $pris[0] as $p | $rerun[0] as $r | $cpkgs[0] as $cp | $ppkgs[0] as $pp
| def pkg: split(" ")[0];
  def testname: split(" ")[1:] | join(" ");
  def missing($s; $pkgs): [$pkgs[] | select($s.term[.] == null)];
  def exitcheck($name; $s; $e):
    ([$s.term[] | select(. == "fail")] | length) as $nf
    | if ($e != 0 and $e != 1) then ["go test on \($name) exited \($e)"]
      elif ($e == 0 and $nf > 0) then ["go test on \($name) exited 0 but \($nf) package(s) failed"]
      elif ($e == 1 and $nf == 0) then ["go test on \($name) exited 1 without a failing package"]
      else [] end;
  def excused($f):
    ($f | pkg) as $pk | ($f | testname) as $t
    | ($p.failed | index($f)) != null
      or ($p.build_failed | index($pk)) != null
      or ($p.term[$pk] == "fail" and ($t == "[package]" or ($p.passed | index($f)) == null));
  [$c.failed[] | select(pkg as $pk | ($c.build_failed | index($pk)) == null)] as $cf
| [$cf[] | select(. as $f | $r.term[$f | pkg] == null or ($r.failed | index($f)) != null)] as $confirmed
| [$cf[] | select(. as $f | ($confirmed | index($f)) == null)] as $flaky
| [$confirmed[] | select(excused(.) | not)] as $new
| [$confirmed[] | select(excused(.))] as $upstream
| missing($c; $cp) as $mc | missing($p; $pp) as $mp
| (exitcheck("the candidate"; $c; $cexit) + exitcheck("the tag"; $p; $pexit)
   + (if ($new | length) > 0 then ["\($new | length) test(s) fail on the candidate (also on a rerun of the package) and not on the tag"] else [] end)
   + (if ($c.build_failed | length) > 0 then ["\($c.build_failed | length) package(s) do not build on the candidate"] else [] end)
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
                failed_packages: ([$c.term[] | select(. == "fail")] | length)},
    tag: {exit: $pexit, packages: ($pp | length), passed: ($p.passed | length), failed: ($p.failed | length),
          failed_packages: ([$p.term[] | select(. == "fail")] | length)},
    rerun_packages: ($r.term | keys)
  }

# Warnings in raw `go test -json` output (jq -R -c, line by line; check.sh verdict): an
# output line of a test that holds "provimux-warning: <text>" (a test that reports
# something and does not fail, such as the Devin catalog test, SYNC.md). The output is
# the candidate's, so it is data: only the text after the marker, in a restricted
# character set, at most 400 characters; lines that are not JSON are ignored.
(try fromjson catch null)
| select(type == "object" and .Action == "output" and (.Test | type) == "string" and (.Output | type) == "string")
| .Package as $pkg | .Test as $test
| .Output | capture("provimux-warning: (?<w>[^\n]*)") | .w
| gsub("[^A-Za-z0-9 ._,;:()=/'+-]"; "") | .[0:400]
| select(length > 0)
| {package: ($pkg // "" | gsub("[^A-Za-z0-9._/-]"; "")), test: ($test | gsub("[^A-Za-z0-9_/-]"; "")), text: .}

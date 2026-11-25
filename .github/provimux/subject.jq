# A commit subject as the release notes list it (provimux-release.yml, "Write release
# notes"; jq -R -r, one subject per line): without board numbers. Removes a "(T-<digits>)"
# (also "(T-1, T-2)") with the spaces before it, and a bare "T-<digits>" token (not part
# of a longer word) with the spaces before it; then leading spaces. Nothing else changes.
gsub("\\s*\\(T-[0-9]+(\\s*[,;]\\s*T-[0-9]+)*\\)"; "")
| gsub("\\s*(?<![A-Za-z0-9_-])T-[0-9]+(?![A-Za-z0-9_-])"; "")
| sub("^\\s+"; "")

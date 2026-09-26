## zvec-grep

`zvec_grep_search` answers semantic, fuzzy, relationship, and cross-file questions
whose wording or location is unknown; exact words, names, paths, keys, and regexes
stay with `Grep`/`Glob`.

The index is user-owned — manage it with `/zg-status`, `/zg-index`, `/zg-remove`;
never run `zg index` in a shell.

Results are a ranked sample: expand the hits with Read or Grep, and re-check
`possibly_stale` items with Grep before relying on them. A result that starts
with `note: refresh skipped` could not refresh the index, so treat every hit in it
as unverified.

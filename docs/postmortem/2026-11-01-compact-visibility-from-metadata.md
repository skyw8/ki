# Compact visibility vanished on the indexed transcript path

## Symptoms

After the compact view started keeping only assistant prose of at least 50
characters, the browser showed every assistant reply folded as soon as the
session was served from the metadata transcript cache. The same session
rendered correctly through the CLI's offline path, and the full-entry
projection test disagreed with the indexed one.

## Cause

`ReadTranscript` decodes each JSONL line into an `Entry` whose message text is
replaced by `metadataPreview`: whitespace is collapsed and the result is cut to
164 bytes. The compact projection measured `Message.Text()` to decide whether
an assistant reply is visible. On the metadata path that text was always
shorter than the threshold, so every reply folded regardless of its real
length. Collapsing whitespace made the preview shorter still, so even a larger
cutoff would not have been exact.

The compact page then reads full bodies only for entries selected as visible.
Once the selection was wrong, the bodies were never fetched and the mistake was
self-sustaining.

## Fix

- The metadata decoder records the true plain-text rune count of a message in
  the unexported `Entry.messageTextRunes` before truncating its preview. It is
  not part of the wire format and does not change the index.
- Compact visibility uses `max(messageTextRunes, runes(Message.Text()))`, so
  full entries (field unset) keep using their complete text, while
  metadata-only projections make the same decision as the full transcript.
- The preview itself stays at 164 bytes. Growing it to the visibility
  threshold would multiply the memory held by the transcript cache and would
  still be wrong for messages whose whitespace collapses below the cutoff.

## Regression coverage

`TestIndexedTranscriptMatchesFullProjections` compares the indexed and full
compact projections, and the server compact view test keeps an assistant reply
far above the threshold visible through the metadata cache. Both failed before
the decoder carried the length and pass afterwards.

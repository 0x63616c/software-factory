// formatTranscript renders a raw JSONL transcript readably: one pretty-printed
// event per line, blank-line separated, inside a single <pre> block. One text
// node for the whole thing (rather than one DOM element per event) is what
// keeps a few-hundred-KB transcript from locking up the tab — #556 explicitly
// allows shipping download-only if a fancier viewer can't carry that well, and
// this is the plainest thing that can. A line that fails to parse is kept
// verbatim rather than dropped: a malformed event is itself evidence, not
// noise to hide.
export function formatTranscript(raw: string): string {
  const lines = raw.split("\n").filter((line) => line.trim() !== "");
  if (lines.length === 0) return "(empty transcript)";
  return lines
    .map((line) => {
      try {
        return JSON.stringify(JSON.parse(line), null, 2);
      } catch {
        return line;
      }
    })
    .join("\n\n");
}

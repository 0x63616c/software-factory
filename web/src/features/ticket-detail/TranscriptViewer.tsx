import { useQuery } from "@tanstack/react-query";
import axios from "axios";
import { useState } from "react";
import { formatTranscript } from "@/features/ticket-detail/transcript";

export function transcriptQueryKey(transcriptPath: string) {
  return ["attempt-transcript", transcriptPath] as const;
}

// TranscriptViewer fetches an Attempt's transcript only once a reader asks
// for it — `enabled` stays false until the toggle opens, so a run with many
// attempts never pulls transcripts nobody is looking at. ADR-0012 defers live
// tailing: this renders one already-landed transcript, never a stream.
export function TranscriptViewer({ transcriptPath }: { transcriptPath: string }) {
  const [open, setOpen] = useState(false);
  const query = useQuery({
    queryKey: transcriptQueryKey(transcriptPath),
    queryFn: () => axios.get<string>(transcriptPath),
    enabled: open,
  });

  if (!open) {
    return (
      <button type="button" onClick={() => setOpen(true)}>
        View transcript
      </button>
    );
  }

  if (query.isPending) return <p>Loading transcript…</p>;
  if (query.isError) {
    const message = query.error instanceof Error ? query.error.message : "unknown error";
    return <p role="alert">Could not load transcript: {message}</p>;
  }
  return (
    <pre data-testid="transcript-viewer" style={{ maxHeight: "20rem", overflow: "auto" }}>
      {formatTranscript(query.data.data)}
    </pre>
  );
}

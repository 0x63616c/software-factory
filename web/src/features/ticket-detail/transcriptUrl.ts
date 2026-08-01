import axios from "axios";

// transcriptDownloadUrl builds the same-origin path main.tsx's
// axios.defaults.baseURL ("/api") already routes through nginx to the API
// (ADR-0012 "Exposure and authentication"). A plain <a href download> is
// enough — the browser carries the Cloudflare Access session cookie on a
// same-origin navigation, so no fetch/blob plumbing is needed here.
export function transcriptDownloadUrl(transcriptPath: string): string {
  const base = axios.defaults.baseURL ?? "/api";
  return `${base}${transcriptPath}`;
}

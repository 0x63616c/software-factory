import type { AttemptOutput, UsageOutput } from "@/api/generated";

// formatTokenCounts renders the four token counts ADR-0012 fixes: input
// (labelled as including its cached part) and output (labelled as including
// its reasoning part), so the two inclusion relationships work.Usage's doc
// comments describe are never misread as four independent numbers to add.
// incomplete appends a note rather than hiding the total: the measured part
// is still real information, and "incomplete" is what tells a reader not to
// treat it as the whole story (ADR-0012, "the one thing that must not be got
// wrong").
function formatTokenCounts(
  inputTokens: number,
  cachedInputTokens: number,
  outputTokens: number,
  reasoningTokens: number,
  incomplete: boolean,
): string {
  const text = `${inputTokens.toLocaleString()} in (${cachedInputTokens.toLocaleString()} cached) · ${outputTokens.toLocaleString()} out (${reasoningTokens.toLocaleString()} reasoning)`;
  return incomplete ? `${text} — incomplete` : text;
}

// formatUsage renders a Step's or Run's rolled-up usage.
export function formatUsage(usage: UsageOutput): string {
  return formatTokenCounts(
    usage.inputTokens,
    usage.cachedInputTokens,
    usage.outputTokens,
    usage.reasoningTokens,
    !usage.complete,
  );
}

// formatAttemptUsage renders one Attempt's usage, or "unknown" when it was
// never measured — never "0": missing provider usage is not the same fact as
// a measured Attempt that spent nothing.
export function formatAttemptUsage(attempt: AttemptOutput): string {
  if (
    attempt.usageState !== "measured" ||
    attempt.inputTokens === null ||
    attempt.cachedInputTokens === null ||
    attempt.outputTokens === null ||
    attempt.reasoningTokens === null
  ) {
    return "unknown";
  }
  return formatTokenCounts(
    attempt.inputTokens,
    attempt.cachedInputTokens,
    attempt.outputTokens,
    attempt.reasoningTokens,
    false,
  );
}

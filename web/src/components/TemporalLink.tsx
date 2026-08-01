// One button for every deep link into the Temporal Web UI. The mark is
// Temporal's interlocking-rings logo, drawn inline so no asset pipeline or
// external fetch is involved.
export function TemporalLink({
  href,
  label = "Open in Temporal",
}: {
  href: string;
  label?: string;
}) {
  return (
    <a className="temporal-link" href={href} target="_blank" rel="noreferrer">
      <svg
        className="temporal-mark"
        viewBox="0 0 24 24"
        width="14"
        height="14"
        fill="none"
        stroke="currentColor"
        strokeWidth="2.4"
        aria-hidden="true"
      >
        <circle cx="9.5" cy="9.5" r="6" />
        <circle cx="14.5" cy="14.5" r="6" />
      </svg>
      {label}
    </a>
  );
}

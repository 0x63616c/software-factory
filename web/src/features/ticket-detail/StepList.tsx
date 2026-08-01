import type { StepOutput } from "@/api/generated";
import { StepRow } from "@/features/ticket-detail/StepRow";

export function StepList({ steps }: { steps: StepOutput[] }) {
  if (steps.length === 0) return <p className="section-empty">No steps recorded yet.</p>;
  return (
    <ol className="step-list" data-testid="step-list">
      {steps.map((step) => (
        <li key={step.ordinal}>
          <StepRow step={step} />
        </li>
      ))}
    </ol>
  );
}

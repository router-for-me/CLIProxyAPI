import React from 'react';

// UsageFooter — sticky footer above the composer. Shows the most recent
// message's token usage plus an estimated cost when pricing is known.
// Falls back to em-dash everywhere a value is missing (per design doc).
export default function UsageFooter({ lastUsage, pricingPerMillion }) {
  const prompt = lastUsage?.prompt_tokens;
  const completion = lastUsage?.completion_tokens;
  const total = (prompt ?? 0) + (completion ?? 0);

  let cost = '—';
  if (pricingPerMillion && Number.isFinite(prompt) && Number.isFinite(completion)) {
    const dollars =
      (prompt * pricingPerMillion.input + completion * pricingPerMillion.output) / 1_000_000;
    cost = `$${dollars.toFixed(6)}`;
  }

  return (
    <div className="playground-usage">
      <span>prompt: {Number.isFinite(prompt) ? prompt : '—'}</span>
      <span>completion: {Number.isFinite(completion) ? completion : '—'}</span>
      <span>total: {Number.isFinite(prompt) && Number.isFinite(completion) ? total : '—'}</span>
      <span>cost: {cost}</span>
    </div>
  );
}

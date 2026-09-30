import React from 'react';

// UsageFooter — sticky footer above the composer. Shows the most recent
// message's token usage plus an estimated cost when pricing is known.
// `pricing` is the model_pricing row shape returned by
// GET /models-catalog/:id/pricing ({ input_per_1m_usd, output_per_1m_usd });
// a normalized { input, output } shape is also accepted. Missing values
// render as an em-dash.
export default function UsageFooter({ lastUsage, pricing }) {
  const prompt = lastUsage?.prompt_tokens;
  const completion = lastUsage?.completion_tokens;
  const total = Number.isFinite(prompt) && Number.isFinite(completion) ? prompt + completion : lastUsage?.total_tokens;

  const inputRate = pricing?.input_per_1m_usd ?? pricing?.input;
  const outputRate = pricing?.output_per_1m_usd ?? pricing?.output;

  let cost = '—';
  if (Number.isFinite(inputRate) && Number.isFinite(outputRate) && Number.isFinite(prompt) && Number.isFinite(completion)) {
    const dollars = (prompt * inputRate + completion * outputRate) / 1_000_000;
    cost = `$${dollars.toFixed(6)}`;
  }

  return (
    <div className="playground-usage">
      <span>prompt: {Number.isFinite(prompt) ? prompt : '—'}</span>
      <span>completion: {Number.isFinite(completion) ? completion : '—'}</span>
      <span>total: {Number.isFinite(total) ? total : '—'}</span>
      <span>cost: {cost}</span>
    </div>
  );
}

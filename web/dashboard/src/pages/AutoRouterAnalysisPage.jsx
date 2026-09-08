import React from 'react';
import AutoRouterAnalysisPageInner from '../components/autorouter/AutoRouterAnalysisTabs.jsx';

// AutoRouterAnalysisPage is the routed entry for /analysis/auto-routers. The
// implementation lives in components/autorouter/AutoRouterAnalysisTabs.jsx —
// the page grew to four tabs (Overview / Decision distribution / Simulation /
// Replay) and moved into a component so the tab tree stays in one module.
export default function AutoRouterAnalysisPage() {
  return <AutoRouterAnalysisPageInner />;
}

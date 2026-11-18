/**
 * Papa Proxy mark: three quota capsules at different levels, the same shape the
 * quota runway uses for accounts. Inline SVG so the single-file build needs no asset.
 */
const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32">
<rect width="32" height="32" rx="8" fill="#4a57d9"/>
<g fill="#ffffff" fill-opacity="0.28">
<rect x="7" y="7" width="4.5" height="18" rx="2.25"/>
<rect x="13.75" y="7" width="4.5" height="18" rx="2.25"/>
<rect x="20.5" y="7" width="4.5" height="18" rx="2.25"/>
</g>
<g fill="#ffffff">
<rect x="7" y="19" width="4.5" height="6" rx="2.25"/>
<rect x="13.75" y="13" width="4.5" height="12" rx="2.25"/>
<rect x="20.5" y="7" width="4.5" height="18" rx="2.25"/>
</g>
</svg>`;

export const PAPA_MARK_SVG = `data:image/svg+xml;utf8,${encodeURIComponent(svg)}`;

// Inline stroke icons (no icon font, no network).
const base = { width: 16, height: 16, viewBox: "0 0 24 24", fill: "none", stroke: "currentColor", strokeWidth: 1.8, strokeLinecap: "round", strokeLinejoin: "round", "aria-hidden": true } as const;

export const IconOverview = () => (<svg {...base}><rect x="3" y="3" width="7" height="9" rx="1.5" /><rect x="14" y="3" width="7" height="5" rx="1.5" /><rect x="14" y="12" width="7" height="9" rx="1.5" /><rect x="3" y="16" width="7" height="5" rx="1.5" /></svg>);
export const IconAccounts = () => (<svg {...base}><path d="M4 7h16M4 12h16M4 17h10" /></svg>);
export const IconSessions = () => (<svg {...base}><path d="M7 7h10a3 3 0 0 1 0 6H9a3 3 0 0 0 0 6h10" /><circle cx="5" cy="7" r="2" /><circle cx="19" cy="19" r="2" /></svg>);
export const IconActivity = () => (<svg {...base}><path d="M3 12h4l3-8 4 16 3-8h4" /></svg>);
export const IconUsage = () => (<svg {...base}><path d="M4 20V10M10 20V4M16 20v-7M22 20H2" /></svg>);
export const IconSettings = () => (<svg {...base}><circle cx="12" cy="12" r="3" /><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1A1.7 1.7 0 0 0 4.6 9a1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z" /></svg>);
export const IconRefresh = () => (<svg {...base}><path d="M21 12a9 9 0 1 1-2.6-6.4L21 8" /><path d="M21 3v5h-5" /></svg>);
export const IconSearch = () => (<svg {...base}><circle cx="11" cy="11" r="7" /><path d="m20 20-3.5-3.5" /></svg>);
export const IconSun = () => (<svg {...base}><circle cx="12" cy="12" r="4" /><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" /></svg>);
export const IconMoon = () => (<svg {...base}><path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z" /></svg>);
export const IconClose = () => (<svg {...base}><path d="M18 6 6 18M6 6l12 12" /></svg>);
export const IconMenu = () => (<svg {...base}><path d="M4 6h16M4 12h16M4 18h16" /></svg>);
export const IconAlert = () => (<svg {...base}><path d="M12 9v4M12 17h.01" /><path d="M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z" /></svg>);
export const IconInfo = () => (<svg {...base}><circle cx="12" cy="12" r="9" /><path d="M12 16v-4M12 8h.01" /></svg>);
export const IconCheck = () => (<svg {...base}><path d="M20 6 9 17l-5-5" /></svg>);
export const IconStar = () => (<svg {...base} fill="currentColor" stroke="none"><path d="m12 3 2.8 5.7 6.2.9-4.5 4.4 1.1 6.2L12 17.3 6.4 20.2l1.1-6.2L3 9.6l6.2-.9z" /></svg>);
export const IconDownload = () => (<svg {...base}><path d="M12 3v12M7 10l5 5 5-5M4 21h16" /></svg>);
export const IconLogout = () => (<svg {...base}><path d="M15 3h4a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2h-4M10 17l5-5-5-5M15 12H3" /></svg>);
export const IconLink = () => (<svg {...base}><path d="M10 13a5 5 0 0 0 7.5.5l3-3a5 5 0 0 0-7-7l-1.7 1.7" /><path d="M14 11a5 5 0 0 0-7.5-.5l-3 3a5 5 0 0 0 7 7l1.7-1.7" /></svg>);
export const IconChevron = ({ dir = "down" }: { dir?: "down" | "up" }) => (<svg {...base} width={12} height={12} style={{ transform: dir === "up" ? "rotate(180deg)" : undefined }}><path d="m6 9 6 6 6-6" /></svg>);
export const Logo = () => (
  <svg width="16" height="16" viewBox="0 0 24 24" fill="none" aria-hidden="true">
    <path d="M4 18V9M10 18V5M16 18v-6" stroke="var(--accent)" strokeWidth="2.4" strokeLinecap="round" />
    <path d="M20 18v-3" stroke="var(--text-3)" strokeWidth="2.4" strokeLinecap="round" />
  </svg>
);

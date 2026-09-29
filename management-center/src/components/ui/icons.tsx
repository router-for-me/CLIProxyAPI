import type { SVGProps } from 'react';

// Inline SVG icons (Lucide, ISC). We embed paths to keep the WebUI single-file/offline friendly.
// Source: https://github.com/lucide-icons/lucide (via lucide-static).

export interface IconProps extends SVGProps<SVGSVGElement> {
  size?: number;
}

const baseSvgProps: SVGProps<SVGSVGElement> = {
  xmlns: 'http://www.w3.org/2000/svg',
  viewBox: '0 0 24 24',
  fill: 'none',
  stroke: 'currentColor',
  strokeWidth: 2,
  strokeLinecap: 'round',
  strokeLinejoin: 'round',
  'aria-hidden': 'true',
  focusable: 'false',
};

export function IconSlidersHorizontal({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <line x1="21" x2="14" y1="4" y2="4" />
      <line x1="10" x2="3" y1="4" y2="4" />
      <line x1="21" x2="12" y1="12" y2="12" />
      <line x1="8" x2="3" y1="12" y2="12" />
      <line x1="21" x2="16" y1="20" y2="20" />
      <line x1="12" x2="3" y1="20" y2="20" />
      <line x1="14" x2="14" y1="2" y2="6" />
      <line x1="8" x2="8" y1="10" y2="14" />
      <line x1="16" x2="16" y1="18" y2="22" />
    </svg>
  );
}

export function IconKey({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <path d="m15.5 7.5 2.3 2.3a1 1 0 0 0 1.4 0l2.1-2.1a1 1 0 0 0 0-1.4L19 4" />
      <path d="m21 2-9.6 9.6" />
      <circle cx="7.5" cy="15.5" r="5.5" />
    </svg>
  );
}

export function IconBot({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <path d="M12 8V4H8" />
      <rect width="16" height="12" x="4" y="8" rx="2" />
      <path d="M2 14h2" />
      <path d="M20 14h2" />
      <path d="M15 13v2" />
      <path d="M9 13v2" />
    </svg>
  );
}

export function IconModelCluster({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <rect x="3" y="5" width="6" height="6" rx="1.5" />
      <rect x="15" y="5" width="6" height="6" rx="1.5" />
      <rect x="9" y="13" width="6" height="6" rx="1.5" />
      <path d="M9 8h6" />
      <path d="M12 11v2" />
      <path d="M7.5 11v2" />
      <path d="M16.5 11v2" />
    </svg>
  );
}

export function IconFilterAll({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <rect x="3.5" y="3.5" width="5" height="5" rx="1.4" />
      <rect x="15.5" y="3.5" width="5" height="5" rx="1.4" />
      <rect x="3.5" y="15.5" width="5" height="5" rx="1.4" />
      <rect x="15.5" y="15.5" width="5" height="5" rx="1.4" />
      <path d="M8.5 8.5 10.75 10.75" />
      <path d="M15.5 8.5 13.25 10.75" />
      <path d="M8.5 15.5 10.75 13.25" />
      <path d="M15.5 15.5 13.25 13.25" />
      <circle cx="12" cy="12" r="1.6" fill="currentColor" stroke="none" />
    </svg>
  );
}

export function IconFileText({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <path d="M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7Z" />
      <path d="M14 2v4a2 2 0 0 0 2 2h4" />
      <path d="M10 9H8" />
      <path d="M16 13H8" />
      <path d="M16 17H8" />
    </svg>
  );
}

export function IconShield({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <path d="M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z" />
    </svg>
  );
}

export function IconSettings({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z" />
      <circle cx="12" cy="12" r="3" />
    </svg>
  );
}

export function IconPlug({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <path d="M12 22v-5" />
      <path d="M9 8V2" />
      <path d="M15 8V2" />
      <path d="M6 8h12v4a6 6 0 0 1-12 0Z" />
    </svg>
  );
}

export function IconScrollText({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <path d="M15 12h-5" />
      <path d="M15 8h-5" />
      <path d="M19 17V5a2 2 0 0 0-2-2H4" />
      <path d="M8 21h12a2 2 0 0 0 2-2v-1a1 1 0 0 0-1-1H11a1 1 0 0 0-1 1v1a2 2 0 1 1-4 0V5a2 2 0 1 0-4 0v2a1 1 0 0 0 1 1h3" />
    </svg>
  );
}

export function IconInfo({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <circle cx="12" cy="12" r="10" />
      <path d="M12 16v-4" />
      <path d="M12 8h.01" />
    </svg>
  );
}

export function IconRefreshCw({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <path d="M21 12a9 9 0 1 1-9-9c2.52 0 4.93 1 6.74 2.74L21 8" />
      <path d="M21 3v5h-5" />
    </svg>
  );
}

export function IconDownload({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <path d="M12 15V3" />
      <path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4" />
      <path d="m7 10 5 5 5-5" />
    </svg>
  );
}

export function IconUpload({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <path d="M12 3v12" />
      <path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4" />
      <path d="m17 8-5-5-5 5" />
    </svg>
  );
}

export function IconTrash2({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <path d="M3 6h18" />
      <path d="M19 6v14c0 1-1 2-2 2H7c-1 0-2-1-2-2V6" />
      <path d="M8 6V4c0-1 1-2 2-2h4c1 0 2 1 2 2v2" />
      <line x1="10" x2="10" y1="11" y2="17" />
      <line x1="14" x2="14" y1="11" y2="17" />
    </svg>
  );
}

export function IconMaximize2({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <path d="M15 3h6v6" />
      <path d="m21 3-7 7" />
      <path d="M9 21H3v-6" />
      <path d="m3 21 7-7" />
    </svg>
  );
}

export function IconMinimize2({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <path d="M4 14h6v6" />
      <path d="m10 14-7 7" />
      <path d="M20 10h-6V4" />
      <path d="m14 10 7-7" />
    </svg>
  );
}

export function IconPlus({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <path d="M5 12h14" />
      <path d="M12 5v14" />
    </svg>
  );
}

export function IconPencil({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <path d="M21.174 6.812a1 1 0 0 0-3.986-3.987L3.842 16.174a2 2 0 0 0-.5.83l-1.321 4.352a.5.5 0 0 0 .623.622l4.353-1.32a2 2 0 0 0 .83-.497z" />
      <path d="m15 5 4 4" />
    </svg>
  );
}

export function IconAlertTriangle({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3Z" />
      <path d="M12 9v4" />
      <path d="M12 17h.01" />
    </svg>
  );
}

export function IconCheckCircle2({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <circle cx="12" cy="12" r="10" />
      <path d="m9 12 2 2 4-4" />
    </svg>
  );
}

export function IconNetwork({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <rect x="16" y="16" width="6" height="6" rx="1" />
      <rect x="2" y="16" width="6" height="6" rx="1" />
      <rect x="9" y="2" width="6" height="6" rx="1" />
      <path d="M5 16v-3a1 1 0 0 1 1-1h12a1 1 0 0 1 1 1v3" />
      <path d="M12 12V8" />
    </svg>
  );
}

export function IconLoader2({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <path d="M21 12a9 9 0 1 1-6.219-8.56" />
    </svg>
  );
}

export function IconChevronUp({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <path d="m18 15-6-6-6 6" />
    </svg>
  );
}

export function IconChevronDown({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <path d="m6 9 6 6 6-6" />
    </svg>
  );
}

export function IconChevronLeft({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <path d="m15 18-6-6 6-6" />
    </svg>
  );
}

export function IconSearch({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <path d="m21 21-4.34-4.34" />
      <circle cx="11" cy="11" r="8" />
    </svg>
  );
}

export function IconX({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <path d="M18 6 6 18" />
      <path d="m6 6 12 12" />
    </svg>
  );
}

export function IconCheck({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <path d="M20 6 9 17l-5-5" />
    </svg>
  );
}

export function IconEye({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <path d="M2.062 12.348a1 1 0 0 1 0-.696 10.75 10.75 0 0 1 19.876 0 1 1 0 0 1 0 .696 10.75 10.75 0 0 1-19.876 0" />
      <circle cx="12" cy="12" r="3" />
    </svg>
  );
}

export function IconEyeOff({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <path d="M10.733 5.076a10.744 10.744 0 0 1 11.205 6.575 1 1 0 0 1 0 .696 10.747 10.747 0 0 1-1.444 2.49" />
      <path d="M14.084 14.158a3 3 0 0 1-4.242-4.242" />
      <path d="M17.479 17.499a10.75 10.75 0 0 1-15.417-5.151 1 1 0 0 1 0-.696 10.75 10.75 0 0 1 4.446-5.143" />
      <path d="m2 2 20 20" />
    </svg>
  );
}

export function IconInbox({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <polyline points="22 12 16 12 14 15 10 15 8 12 2 12" />
      <path d="M5.45 5.11 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z" />
    </svg>
  );
}

export function IconSatellite({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <path d="m13.5 6.5-3.148-3.148a1.205 1.205 0 0 0-1.704 0L6.352 5.648a1.205 1.205 0 0 0 0 1.704L9.5 10.5" />
      <path d="M16.5 7.5 19 5" />
      <path d="m17.5 10.5 3.148 3.148a1.205 1.205 0 0 1 0 1.704l-2.296 2.296a1.205 1.205 0 0 1-1.704 0L13.5 14.5" />
      <path d="M9 21a6 6 0 0 0-6-6" />
      <path d="M9.352 10.648a1.205 1.205 0 0 0 0 1.704l2.296 2.296a1.205 1.205 0 0 0 1.704 0l4.296-4.296a1.205 1.205 0 0 0 0-1.704l-2.296-2.296a1.205 1.205 0 0 0-1.704 0z" />
    </svg>
  );
}

export function IconTimer({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <line x1="10" x2="14" y1="2" y2="2" />
      <line x1="12" x2="15" y1="14" y2="11" />
      <circle cx="12" cy="14" r="8" />
    </svg>
  );
}

export function IconDollarSign({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <line x1="12" x2="12" y1="2" y2="22" />
      <path d="M17 5H9.5a3.5 3.5 0 0 0 0 7h5a3.5 3.5 0 0 1 0 7H6" />
    </svg>
  );
}

export function IconGithub({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <path d="M15 22v-4a4.8 4.8 0 0 0-1-3.5c3 0 6-2 6-5.5.08-1.25-.27-2.48-1-3.5.28-1.15.28-2.35 0-3.5 0 0-1 0-3 1.5-2.64-.5-5.36-.5-8 0C6 2 5 2 5 2c-.3 1.15-.3 2.35 0 3.5A5.403 5.403 0 0 0 4 9c0 3.5 3 5.5 6 5.5-.39.49-.68 1.05-.85 1.65-.17.6-.22 1.23-.15 1.85v4" />
      <path d="M9 18c-4.51 2-5-2-7-2" />
    </svg>
  );
}

export function IconHeart({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <path d="M19 14c1.49-1.46 3-3.21 3-5.5A5.5 5.5 0 0 0 16.5 3c-1.76 0-3 .5-4.5 2-1.5-1.5-2.74-2-4.5-2A5.5 5.5 0 0 0 2 8.5c0 2.29 1.51 4.04 3 5.5l7 7Z" />
    </svg>
  );
}

export function IconExternalLink({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <path d="M15 3h6v6" />
      <path d="M10 14 21 3" />
      <path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6" />
    </svg>
  );
}

export function IconBookOpen({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <path d="M12 7v14" />
      <path d="M3 18a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1h5a4 4 0 0 1 4 4 4 4 0 0 1 4-4h5a1 1 0 0 1 1 1v13a1 1 0 0 1-1 1h-6a3 3 0 0 0-3 3 3 3 0 0 0-3-3z" />
    </svg>
  );
}

export function IconCode({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <polyline points="16 18 22 12 16 6" />
      <polyline points="8 6 2 12 8 18" />
    </svg>
  );
}

export function IconSidebarDashboard({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <rect width="7" height="9" x="3" y="3" rx="1" />
      <rect width="7" height="5" x="14" y="3" rx="1" />
      <rect width="7" height="9" x="14" y="12" rx="1" />
      <rect width="7" height="5" x="3" y="16" rx="1" />
    </svg>
  );
}

export function IconSidebarQuickStart({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <path d="M4 14a1 1 0 0 1-.78-1.63l9.9-10.2a.5.5 0 0 1 .86.46l-1.92 6.02A1 1 0 0 0 13 10h7a1 1 0 0 1 .78 1.63l-9.9 10.2a.5.5 0 0 1-.86-.46l1.92-6.02A1 1 0 0 0 11 14z" />
    </svg>
  );
}

export const IconSidebarConfig = IconSlidersHorizontal;

export const IconSidebarPlugins = IconPlug;

export function IconSidebarStore({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <path d="m2 7 4.41-4.41A2 2 0 0 1 7.83 2h8.34a2 2 0 0 1 1.42.59L22 7" />
      <path d="M4 12v8a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-8" />
      <path d="M15 22v-4a2 2 0 0 0-2-2h-2a2 2 0 0 0-2 2v4" />
      <path d="M2 7h20" />
      <path d="M22 7v3a2 2 0 0 1-2 2a2.7 2.7 0 0 1-1.59-.63.7.7 0 0 0-.82 0A2.7 2.7 0 0 1 16 12a2.7 2.7 0 0 1-1.59-.63.7.7 0 0 0-.82 0A2.7 2.7 0 0 1 12 12a2.7 2.7 0 0 1-1.59-.63.7.7 0 0 0-.82 0A2.7 2.7 0 0 1 8 12a2.7 2.7 0 0 1-1.59-.63.7.7 0 0 0-.82 0A2.7 2.7 0 0 1 4 12a2 2 0 0 1-2-2V7" />
    </svg>
  );
}

export const IconSidebarProviders = IconNetwork;

export function IconSidebarAuthFiles({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <path d="M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z" />
      <path d="m9 12 2 2 4-4" />
    </svg>
  );
}

export function IconSidebarOauth({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <path d="M2 21a8 8 0 0 1 13.292-6" />
      <circle cx="10" cy="8" r="5" />
      <path d="m16 19 2 2 4-4" />
    </svg>
  );
}

export function IconSidebarQuota({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <path d="m12 14 4-4" />
      <path d="M3.34 19a10 10 0 1 1 17.32 0" />
    </svg>
  );
}

export const IconSidebarLogs = IconScrollText;

export function IconSidebarSystem({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <rect width="20" height="8" x="2" y="2" rx="2" />
      <rect width="20" height="8" x="2" y="14" rx="2" />
      <line x1="6" x2="6.01" y1="6" y2="6" />
      <line x1="6" x2="6.01" y1="18" y2="18" />
    </svg>
  );
}

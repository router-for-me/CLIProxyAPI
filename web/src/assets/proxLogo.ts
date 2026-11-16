/** The prox mark (supplied 512x512 SVG; ids/title stripped). Same asset in every theme. */
export const PROX_LOGO_SVG =
  "<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 512 512'>" +
  "<rect width='512' height='512' rx='136' fill='#161618'/>" +
  "<path d='M 124 96 H 164 C 178 96 190 102 200 114 L 288 210 Q 300 224 312 224 V 256 H 232 L 164 168 Q 156 160 144 160 H 124 A 32 32 0 0 1 124 96 Z' fill='#C15F3C'/>" +
  "<path d='M 124 96 H 164 C 178 96 190 102 200 114 L 288 210 Q 300 224 312 224 V 256 H 232 L 164 168 Q 156 160 144 160 H 124 A 32 32 0 0 1 124 96 Z' transform='translate(0 512) scale(1 -1)' fill='#4796E3'/>" +
  "<rect x='82' y='224' width='348' height='64' rx='32' fill='#FFFFFF'/>" +
  '</svg>';
export const PROX_LOGO_DATA_URI = `data:image/svg+xml,${encodeURIComponent(PROX_LOGO_SVG)}`;

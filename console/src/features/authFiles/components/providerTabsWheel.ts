type ScrollStrip = Pick<HTMLElement, 'scrollWidth' | 'clientWidth' | 'scrollLeft'>;
type StripWheelEvent = Pick<
  WheelEvent,
  'deltaX' | 'deltaY' | 'deltaMode' | 'ctrlKey' | 'defaultPrevented' | 'preventDefault'
>;

/** Map vertical wheels to the strip; preserve native horizontal gestures and zoom. */
export function scrollProviderTabs(strip: ScrollStrip, event: StripWheelEvent): void {
  const maxScroll = strip.scrollWidth - strip.clientWidth;
  if (
    event.ctrlKey ||
    event.defaultPrevented ||
    maxScroll <= 0 ||
    event.deltaY === 0 ||
    Math.abs(event.deltaX) > Math.abs(event.deltaY)
  ) {
    return;
  }

  // Consume vertical wheels even at the ends so the page does not jump underneath the strip.
  event.preventDefault();
  const unit = event.deltaMode === 1 ? 16 : event.deltaMode === 2 ? strip.clientWidth : 1;
  strip.scrollLeft = Math.max(0, Math.min(maxScroll, strip.scrollLeft + event.deltaY * unit));
}

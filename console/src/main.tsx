import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import '@/styles/fonts.css';
import '@/styles/global.scss';
import { PAPA_ICON } from '@/assets/papaIcon';
import App from './App.tsx';

document.title = 'Papa Proxy';
document.documentElement.setAttribute('translate', 'no');
document.documentElement.classList.add('notranslate');

const faviconEl = document.querySelector<HTMLLinkElement>('link[rel="icon"]');
if (faviconEl) {
  faviconEl.href = PAPA_ICON;
  faviconEl.type = 'image/webp';
} else {
  const newFavicon = document.createElement('link');
  newFavicon.rel = 'icon';
  newFavicon.type = 'image/webp';
  newFavicon.href = PAPA_ICON;
  document.head.appendChild(newFavicon);
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>
);

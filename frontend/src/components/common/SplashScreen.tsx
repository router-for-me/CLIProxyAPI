import { useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { INLINE_LOGO_JPEG } from '@/assets/logoInline';
import './SplashScreen.scss';

interface SplashScreenProps {
  onFinish: () => void;
  fadeOut?: boolean;
}

const FADE_OUT_DURATION = 400;

export function SplashScreen({ onFinish, fadeOut = false }: SplashScreenProps) {
  const { t } = useTranslation();

  useEffect(() => {
    if (!fadeOut) return;
    const finishTimer = setTimeout(() => {
      onFinish();
    }, FADE_OUT_DURATION);

    return () => {
      clearTimeout(finishTimer);
    };
  }, [fadeOut, onFinish]);

  return (
    <div className={`splash-screen ${fadeOut ? 'fade-out' : ''}`}>
      <div className="splash-content">
        <img src={INLINE_LOGO_JPEG} alt="CPAMC" className="splash-logo" />
        <h1 className="splash-title">{t('splash.title')}</h1>
        <p className="splash-subtitle">{t('splash.subtitle')}</p>
        <div className="splash-loader">
          <div className="splash-loader-bar" />
        </div>
      </div>
    </div>
  );
}

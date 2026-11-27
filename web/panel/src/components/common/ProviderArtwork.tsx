import { useContext, useState, type ReactNode } from 'react';
import { PluginProviderLogos } from '@/features/plugins/providerLogos';
import { normalizeOAuthProviderKey } from '@/utils/providerKeys';

type ArtworkProps = {
  src: string | null;
  className?: string;
  children: ReactNode;
};

function ArtworkImage({ src, className, children }: ArtworkProps) {
  const [failed, setFailed] = useState(false);
  return src && !failed ? (
    <img src={src} alt="" className={className} onError={() => setFailed(true)} />
  ) : (
    children
  );
}

export function ProviderArtwork({ provider, src, ...props }: ArtworkProps & { provider?: string }) {
  const logos = useContext(PluginProviderLogos);
  const source = src ?? (provider && logos.get(normalizeOAuthProviderKey(provider))) ?? null;
  return <ArtworkImage key={source} src={source} {...props} />;
}

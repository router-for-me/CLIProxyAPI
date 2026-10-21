import { useState } from 'react';
import { IconPlug } from '@/components/ui/icons';

/** 提供商品牌图标；插件 logo 加载失败时回退到插头图标。 */
export function BrandGlyph({
  src,
  size,
  className,
}: {
  src: string;
  size: number;
  className?: string;
}) {
  const [failedSrc, setFailedSrc] = useState<string | null>(null);
  if (src && failedSrc !== src) {
    return (
      <img
        src={src}
        alt=""
        width={size}
        height={size}
        className={className}
        onError={() => setFailedSrc(src)}
      />
    );
  }
  return <IconPlug size={size} aria-hidden="true" className={className} />;
}

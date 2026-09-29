/**
 * 赞助商展示数据（网络配置分区「代理 URL」字段标签下方的赞助跳转行）。
 * 纯前端常量，与后端配置无关；新增/移除赞助商只需改动这里。
 * 数组为空时赞助行整体不渲染。
 */
import bestproxyLogo from '@/assets/icons/bestproxy.png';

export type Sponsor = {
  /** 赞助商名称（直接展示，不做翻译）。 */
  name: string;
  /** 点击跳转地址。 */
  url: string;
  /** 可选小图标（与名称并排展示）。 */
  logo?: string;
};

export const SPONSORS: readonly Sponsor[] = [
  {
    name: 'BestProxy.com',
    url: 'https://bestproxy.com/?keyword=ayh7otlb',
    logo: bestproxyLogo,
  },
];

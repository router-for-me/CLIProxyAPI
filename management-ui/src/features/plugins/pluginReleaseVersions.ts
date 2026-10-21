import { apiCallApi, getApiCallErrorMessage } from '@/services/api';
import { isRecord } from '@/utils/helpers';

export interface PluginReleaseVersion {
  tagName: string;
  name: string;
  publishedAt: string;
  prerelease: boolean;
  htmlUrl: string;
  assetNames: string[];
}

const GITHUB_API_BASE = 'https://api.github.com';
const GITHUB_HOSTS = new Set(['github.com', 'www.github.com']);
const GITHUB_RELEASES_PAGE_SIZE = 50;

export const supportsPluginVersionSelection = (installType: string): boolean =>
  installType.trim().toLowerCase() === 'github-release';

const stripGitSuffix = (value: string) => value.replace(/\.git$/i, '');

export const getGitHubRepositorySlug = (repository: string): string => {
  const trimmed = repository.trim();
  if (!trimmed) return '';

  if (/^https?:\/\//i.test(trimmed)) {
    try {
      const url = new URL(trimmed);
      if (!GITHUB_HOSTS.has(url.hostname.toLowerCase())) return '';
      const [owner = '', repo = ''] = url.pathname.replace(/^\/+/, '').split('/');
      if (!owner || !repo) return '';
      return `${owner}/${stripGitSuffix(repo)}`;
    } catch {
      return '';
    }
  }

  const withoutHost = trimmed.replace(/^github\.com\//i, '').replace(/^\/+/, '');
  const [owner = '', repo = ''] = withoutHost.split('/');
  if (!owner || !repo) return '';
  return `${owner}/${stripGitSuffix(repo)}`;
};

export const buildGitHubReleasesPageURL = (repository: string): string => {
  const slug = getGitHubRepositorySlug(repository);
  return slug ? `https://github.com/${slug}/releases` : '';
};

export const isValidManualReleaseTag = (value: string): boolean => {
  const trimmed = value.trim();
  return Boolean(trimmed) && /^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$/.test(trimmed);
};

const normalizeAssetNames = (value: unknown): string[] => {
  if (!Array.isArray(value)) return [];
  return value
    .map((asset) => (isRecord(asset) && typeof asset.name === 'string' ? asset.name.trim() : ''))
    .filter(Boolean);
};

const normalizeRelease = (value: unknown): PluginReleaseVersion | null => {
  if (!isRecord(value) || typeof value.tag_name !== 'string') return null;
  const tagName = value.tag_name.trim();
  if (!tagName) return null;

  return {
    tagName,
    name: typeof value.name === 'string' ? value.name.trim() : '',
    publishedAt: typeof value.published_at === 'string' ? value.published_at : '',
    prerelease: value.prerelease === true,
    htmlUrl: typeof value.html_url === 'string' ? value.html_url.trim() : '',
    assetNames: normalizeAssetNames(value.assets),
  };
};

export const fetchPluginReleaseVersions = async (
  repository: string
): Promise<PluginReleaseVersion[]> => {
  const slug = getGitHubRepositorySlug(repository);
  if (!slug) {
    throw new Error('Repository is not a GitHub repository');
  }

  const result = await apiCallApi.request({
    method: 'GET',
    url: `${GITHUB_API_BASE}/repos/${slug}/releases?per_page=${GITHUB_RELEASES_PAGE_SIZE}`,
    header: {
      Accept: 'application/vnd.github+json',
      'X-GitHub-Api-Version': '2022-11-28',
    },
  });

  if (result.statusCode < 200 || result.statusCode >= 300) {
    throw new Error(getApiCallErrorMessage(result));
  }

  if (!Array.isArray(result.body)) {
    throw new Error('GitHub releases response is not a list');
  }

  return result.body
    .map(normalizeRelease)
    .filter((release): release is PluginReleaseVersion => Boolean(release));
};

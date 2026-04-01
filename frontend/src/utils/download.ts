export type DownloadBlobOptions = {
  filename: string;
  blob: Blob;
  revokeDelayMs?: number;
};

export function downloadBlob({ filename, blob, revokeDelayMs = 1000 }: DownloadBlobOptions) {
  const url = window.URL.createObjectURL(blob);
  const link = document.createElement('a');
  link.href = url;
  link.download = filename;
  link.rel = 'noopener';
  link.style.display = 'none';
  document.body.appendChild(link);
  link.click();

  window.setTimeout(() => {
    window.URL.revokeObjectURL(url);
    link.remove();
  }, revokeDelayMs);
}


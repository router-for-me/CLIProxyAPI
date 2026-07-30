import { useState } from "react";
import { buildQuery } from "../queryString";

export function useExport() {
  const [downloading, setDownloading] = useState(false);

  async function exportCsv(
    params: Record<string, string | string[] | undefined>,
    token?: string,
  ) {
    setDownloading(true);
    try {
      const url = new URL("/api/v1/export", window.location.origin);
      url.search = buildQuery(params).toString();
      const headers: Record<string, string> = {};
      if (token) headers["X-Dashboard-Token"] = token;
      const resp = await fetch(url, { headers });
      if (!resp.ok) throw new Error(`${resp.status}`);
      const blob = await resp.blob();
      const objUrl = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = objUrl;
      a.download = "usage_export.csv";
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
      URL.revokeObjectURL(objUrl);
    } finally {
      setDownloading(false);
    }
  }

  return { export: exportCsv, downloading };
}
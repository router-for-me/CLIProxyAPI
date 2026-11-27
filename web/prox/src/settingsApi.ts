import { ApiError, PLUGIN_ID, request } from "./api";
import type { Change, Settings } from "./types";

export interface Validation {
  valid: boolean;
  changes?: Change[] | null;
  errors?: { field: string; message: string }[] | null;
  generation?: string;
}

export async function validate(patch: Record<string, unknown>): Promise<Validation> {
  try {
    return await request<Validation>("/settings/validate", { method: "POST", body: patch });
  } catch (e) {
    if (e instanceof ApiError && e.status === 422) return e.body as Validation;
    throw e;
  }
}

/**
 * Saves settings the way the architecture requires: the plugin validates,
 * CPA persists the change to config.yaml and hot-reloads the plugin, and we
 * wait until the plugin reports the expected configuration generation.
 */
export async function applySettings(patch: Record<string, unknown>): Promise<Validation> {
  const v = await validate(patch);
  if (!v.valid) return v;
  await request(`/v0/management/plugins/${PLUGIN_ID}/config`, { method: "PATCH", body: patch, absolute: true });
  const deadline = Date.now() + 15000;
  while (Date.now() < deadline) {
    await new Promise((r) => setTimeout(r, 500));
    try {
      const s = await request<Settings>("/settings");
      if (s.generation === v.generation) return v;
    } catch {
      // The plugin is reloading; keep waiting.
    }
  }
  throw new ApiError(0, "reload_timeout", "CPA saved the setting, but the plugin did not report it within 15 seconds. Check CPA's log.");
}

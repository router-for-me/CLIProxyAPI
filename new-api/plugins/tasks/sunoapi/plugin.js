// Unofficial plugin
// https://github.com/Suno-API/Suno-API
export const meta = {
  apiVersion: 1,
  key: "sunoapi",
  name: "SunoAPI",
  icon: "text",
  description: {
    en: "SunoAPI project music and lyrics generation",
    zh: "SunoAPI 项目 音乐与歌词生成",
  },
  version: "1.1.0",
  author: { name: "QuantumNous" },
  channelTypes: [36],
  models: ["suno_music", "suno_lyrics"],
  fetchMode: "batch",
  // Vendor paths already equal the /suno native routes, so no URL changes.
  upstreams: ["vendor", "new_api"],
  usageSchema: {
    // Number of generated music or lyrics clips.
    clips: {
      type: "number",
      unit: "count",
      description: { en: "Song or lyrics generation unit price", zh: "生成歌曲或歌词单价" },
    },
    // Suno generation action.
    action: {
      enum: ["music", "lyrics"],
      enumLabels: { music: { en: "Generate songs", zh: "生成歌曲" }, lyrics: { en: "Generate lyrics", zh: "生成歌词" } },
      description: { en: "Generate songs or lyrics", zh: "生成歌曲或歌词" },
    },
  },
  protocols: [{ name: "openai_responses", supports: ["stream", "sync", "background"] }],
  routes: [
    { method: "POST", path: "/suno/submit/:action", type: "submit", decode: "decodeSubmit", render: "renderSubmit" },
    { method: "POST", path: "/suno/fetch", type: "dynamic", decode: "decodeBatch", render: "renderTasks" },
    { method: "GET", path: "/suno/fetch/:task_id", type: "query", render: "renderTask" },
  ],
};

function trimmed(value) {
  return String(value || "").trim();
}

function responsesText(req) {
  const texts = [];
  const input = req.input;
  if (typeof input === "string") texts.push(input);
  else if (Array.isArray(input)) {
    for (const item of input) {
      if (typeof item === "string") {
        texts.push(item);
        continue;
      }
      if (!item || typeof item !== "object" || Array.isArray(item)) continue;
      const content = item.content === undefined ? [item] : Array.isArray(item.content) ? item.content : [item.content];
      for (const part of content) {
        if (typeof part === "string") {
          texts.push(part);
          continue;
        }
        if (!part || typeof part !== "object" || Array.isArray(part)) continue;
        if (["input_text", "text"].includes(part.type) && typeof part.text === "string") texts.push(part.text);
      }
    }
  }
  return texts
    .filter(function (text) {
      return trimmed(text);
    })
    .join("\n");
}

function actionName(ctx) {
  return String((ctx.params || {}).action || ctx.action || "").toUpperCase();
}

function decodeNativeSubmit(ctx) {
  if (!ctx.body || ctx.body.kind !== "json") throw new Error("JSON body required");
  const action = actionName(ctx);
  if (action !== "MUSIC" && action !== "LYRICS") throw new Error("invalid_action");
  return {
    kind: "submit",
    model: action === "MUSIC" ? "suno_music" : "suno_lyrics",
    action: action,
    requestBody: ctx.body.value,
  };
}

function decodeNativeBatch(ctx) {
  if (!ctx.body || ctx.body.kind !== "json") throw new Error("JSON body required");
  const body = ctx.body.value || {};
  return { kind: "query", taskIds: Array.isArray(body.ids) ? body.ids : [] };
}

function validateAndNormalize(ctx) {
  const body = Object.assign({}, ctx.requestBody || {});
  if (body.make_instrumental === undefined) body.make_instrumental = false;
  const action = actionName(ctx);
  if (action === "MUSIC") {
    if (!body.mv) body.mv = "chirp-v3-0";
  } else if (action === "LYRICS") {
    if (!body.prompt) throw new Error("prompt_empty");
  } else {
    throw new Error("invalid_action");
  }
  return { action: action, body: body };
}

export function buildSubmitRequest(ctx) {
  const normalized = validateAndNormalize(ctx);
  const incoming = ctx.requestHeaders || {};
  const headers = { Authorization: "Bearer " + ctx.apiKey };
  if (typeof incoming["Content-Type"] === "string" && incoming["Content-Type"]) headers["Content-Type"] = incoming["Content-Type"];
  if (typeof incoming.Accept === "string" && incoming.Accept) headers.Accept = incoming.Accept;
  return {
    url: ctx.baseUrl + "/suno/submit/" + normalized.action,
    method: "POST",
    headers: headers,
    body: normalized.body,
    action: normalized.action,
  };
}

export function parseSubmitResponse(ctx, resp) {
  const body = resp.body || {};
  if (body.code !== "success") throw new Error(String(body.message || ""));
  if (!body.data) throw new Error("task_id is empty");
  // The native create presenter runs after persistence, so retaining the
  // acknowledgement message is required to preserve Suno's submit envelope.
  // The first pre-poll status view therefore contains this temporary message;
  // the first provider poll replaces task.data with the normal Suno payload.
  return { taskId: body.data, taskData: { message: String(body.message || "") } };
}

export function extractUsage(ctx) {
  if (ctx.usagePurpose === "billing_ratios") return null;
  const model = trimmed(ctx.upstreamModel || ctx.model || (ctx.requestBody || {}).model).toLowerCase();
  const action = actionName(ctx).toLowerCase() || (model === "suno_lyrics" ? "lyrics" : "music");
  return { clips: action === "lyrics" ? 1 : 2, action: action };
}

export function buildBatchQueryRequest(ctx, tasks) {
  return {
    url: ctx.baseUrl + "/suno/fetch",
    method: "POST",
    headers: { "Content-Type": "application/json", Authorization: "Bearer " + ctx.apiKey },
    body: {
      ids: (tasks || []).map(function (task) {
        return task.taskId;
      }),
    },
  };
}

// Required v1 per-task hooks remain defined for contract compatibility. Suno's
// host polling path uses the batch hooks below.
export function buildQueryRequest(ctx) {
  return buildBatchQueryRequest(ctx, [ctx]);
}

export function parseBatchResult(ctx, body) {
  if (body.code !== "success") throw new Error(String(body.message || ""));
  return (body.data || []).map(function (item) {
    return {
      taskId: item.task_id || "",
      action: item.action || "",
      status: item.status || "",
      reason: item.fail_reason || "",
      submitTime: item.submit_time || 0,
      startTime: item.start_time || 0,
      finishTime: item.finish_time || 0,
      data: item.data,
    };
  });
}

export function parseTaskResult(ctx, body) {
  const results = parseBatchResult(ctx, { code: 200, data: [body] });
  return results.length ? results[0] : { status: "UNKNOWN" };
}

function artifactData(ctx) {
  const data = (ctx && ctx.data) || [];
  if (data.data && typeof data.data === "object" && data.data.task_id && Object.prototype.hasOwnProperty.call(data.data, "data")) {
    const nested = data.data.data;
    return Array.isArray(nested) ? nested : nested && typeof nested === "object" ? [nested] : [];
  }
  return Array.isArray(data) ? data : data && typeof data === "object" ? [data] : [];
}

function artifactKey(type, song) {
  return type + "-" + utils.hmacSHA256(String(song.id), "new-api:suno:artifact-key");
}

export function listArtifacts(task) {
  if (task.status !== "SUCCESS") return [];
  const artifacts = [];
  for (const song of artifactData(task)) {
    if (!song || !String(song.id || "").trim()) continue;
    if (String(song.audio_url || "").trim()) {
      artifacts.push({ key: artifactKey("audio", song), type: "audio", mimeType: "audio/mpeg" });
    }
    if (String(song.image_url || "").trim()) {
      artifacts.push({ key: artifactKey("cover", song), type: "image" });
    }
  }
  return artifacts;
}

export function buildContentRequest(ctx) {
  const song = artifactData(ctx).find(function (item) {
    if (!item || !String(item.id || "").trim()) return false;
    return artifactKey("audio", item) === ctx.artifactKey || artifactKey("cover", item) === ctx.artifactKey;
  });
  if (!song) throw new Error("artifact_not_found");
  let url = "";
  if (artifactKey("audio", song) === ctx.artifactKey) url = String(song.audio_url || "").trim();
  if (artifactKey("cover", song) === ctx.artifactKey) url = String(song.image_url || "").trim();
  if (!url) throw new Error("artifact_not_found");
  return { url: url, method: ctx.clientRequest.method, credentialless: true };
}

export function extractUsageOnComplete(task, taskResult, body) {
  const values = Array.isArray(body) ? body : body && typeof body === "object" ? [body] : [];
  if (values.length === 0) return {};
  const music = values.some(function (item) {
    return item && (trimmed(item.audio_url) || trimmed(item.video_url));
  });
  return { clips: values.length, action: music ? "music" : "lyrics" };
}

function escapedAttribute(value) {
  return trimmed(value).replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

function responseContent(ctx, task) {
  const declared = trimmed(ctx.upstreamModel || ctx.model).toLowerCase();
  const songs = artifactData(task);
  const lyrics = [];
  for (const song of songs) {
    if (!song) continue;
    const text = trimmed(song.text);
    if (!text) continue;
    const title = trimmed(song.title);
    lyrics.push(title ? title + "\n" + text : text);
  }
  const lyricsMode =
    declared === "suno_lyrics" ||
    (declared !== "suno_music" &&
      !songs.some(function (song) {
        return song && trimmed(song.audio_url);
      }));
  if (lyricsMode) {
    return [{ type: "output_text", text: lyrics.join("\n\n") || "Lyrics generation completed.", annotations: [], logprobs: [] }];
  }
  const content = [{ type: "output_text", text: lyrics.join("\n\n") || "Music generation completed.", annotations: [], logprobs: [] }];
  for (const song of songs) {
    if (!song || !trimmed(song.audio_url)) continue;
    const key = artifactKey("audio", song);
    const artifact = ctx && ctx.artifacts && ctx.artifacts[key];
    const url = trimmed(artifact && artifact.url);
    if (!url) throw new Error("audio artifact is unavailable");
    content.push({ type: "output_text", text: '<audio controls src="' + escapedAttribute(url) + '"></audio>', annotations: [], logprobs: [] });
  }
  return content;
}

function responseText(ctx, task) {
  return responseContent(ctx, task)
    .map(function (part) {
      return part.text;
    })
    .join("\n\n");
}

export const protocols = {
  openai_responses: {
    decodeRequest: function (ctx) {
      if (!ctx.body || ctx.body.kind !== "json") throw new Error("JSON body required");
      const req = ctx.body.value;
      if (!req || typeof req !== "object" || Array.isArray(req)) throw new Error("request body must be an object");
      const declared = trimmed(ctx.upstreamModel || ctx.model).toLowerCase();
      if (declared !== "suno_music" && declared !== "suno_lyrics") throw new Error("model must be suno_music or suno_lyrics");
      if (req.input !== undefined && typeof req.input !== "string" && !Array.isArray(req.input)) throw new Error("input must be a string or array");
      if (req.metadata !== undefined && (!req.metadata || typeof req.metadata !== "object" || Array.isArray(req.metadata)))
        throw new Error("metadata must be an object");
      const input = responsesText(req);
      const requestBody = Object.assign({}, req.metadata || {});
      if (declared === "suno_lyrics") {
        if (!trimmed(requestBody.prompt)) requestBody.prompt = input || trimmed(req.prompt);
        if (!trimmed(requestBody.prompt)) throw new Error("input is required");
        return { kind: "submit", model: ctx.model, action: "LYRICS", requestBody: requestBody };
      }
      if (!trimmed(requestBody.gpt_description_prompt)) requestBody.gpt_description_prompt = input || trimmed(req.prompt);
      if (!trimmed(requestBody.gpt_description_prompt) && !trimmed(requestBody.prompt)) throw new Error("input is required");
      return { kind: "submit", model: ctx.model, action: "MUSIC", requestBody: requestBody };
    },
    renderEvents: function (ctx, task, previousState) {
      const status = String(task.status || "UNKNOWN").toUpperCase();
      const value = Number(String(task.progress || "").replace("%", ""));
      const progress = Number.isFinite(value) && value >= 0 && value <= 100 ? value : null;
      const state = { status: status, progress: progress };
      if (status === "SUCCESS") {
        const text = responseText(ctx, task);
        const events = previousState && previousState.status === status ? [] : [{ type: "output", data: text }];
        return { events: events, state: state, done: true };
      }
      if (status === "FAILURE")
        return { events: [{ type: "error", code: "task_failed", message: task.fail_reason || "task failed" }], state: state, done: true };
      if (previousState && previousState.status === status && previousState.progress === progress) return { events: [], state: state, done: false };
      const event = { type: "progress", message: status.toLowerCase() };
      if (progress !== null) event.progress = progress;
      return { events: [event], state: state, done: false };
    },
    renderFinal: function (ctx, task) {
      return { output: [{ type: "message", status: "completed", role: "assistant", content: responseContent(ctx, task) }], metadata: { vendor: "sunoapi" } };
    },
  },
};

function nativeTask(task) {
  return {
    created_at: task.created_at || 0,
    updated_at: task.updated_at || 0,
    task_id: task.task_id || "",
    platform: task.platform || "sunoapi",
    status: task.status || "",
    fail_reason: task.fail_reason || "",
    submit_time: task.created_at || 0,
    finish_time: task.finished_at || 0,
    progress: task.progress || "",
    data: task.data === undefined ? null : task.data,
  };
}

export const native = {
  decodeSubmit: decodeNativeSubmit,
  decodeBatch: decodeNativeBatch,
  renderSubmit: function (ctx, task) {
    const data = task.data && typeof task.data === "object" ? task.data : {};
    return { code: "success", message: String(data.message || ""), data: String(task.task_id || "") };
  },
  renderTask: function (ctx, task) {
    return { code: "success", message: "", data: nativeTask(task) };
  },
  renderTasks: function (ctx, tasks) {
    return { code: "success", message: "", data: tasks.map(nativeTask) };
  },
  error: function (ctx, error) {
    return { code: error.code, message: error.message, data: null };
  },
};

export const meta = {
  apiVersion: 1,
  key: "vidu",
  name: "Vidu",
  icon: "Vidu.Color",
  description: {
    en: "Shengshu Vidu video generation (text-to-video, image-to-video, first-and-last-frame, and reference-to-video)",
    zh: "生数 Vidu 视频生成（文生视频、图生视频、首尾帧、参考生视频）",
  },
  version: "1.0.2",
  author: { name: "QuantumNous" },
  channelTypes: [52],
  models: ["viduq2", "viduq1", "vidu2.0", "vidu1.5"],
  fetchMode: "per_task",
  usageSchema: {
    // estimated/actual Vidu credits
    credits: {
      type: "number",
      unit: "credit",
      description: { en: "Vidu credit unit price", zh: "Vidu 积分单价" },
    },
    // Requested video duration in seconds.
    duration: {
      type: "number",
      unit: "second",
      description: { en: "Video generation unit price", zh: "视频生成单价" },
    },
    // Requested output video resolution.
    resolution: {
      enum: ["360p", "540p", "720p", "1080p"],
      enumLabels: {
        "360p": { en: "360p", zh: "360p" },
        "540p": { en: "540p", zh: "540p" },
        "720p": { en: "720p", zh: "720p" },
        "1080p": { en: "1080p", zh: "1080p" },
      },
      description: { en: "Output video resolution", zh: "输出视频分辨率" },
    },
  },
  // credits is 0 in examples because this plugin does not estimate vendor credits;
  // schema completeness requires the key. Actual credits arrive from upstream.
  usageExamples: [
    { label: "q2 5s 720p", facts: { credits: 0, duration: 5, resolution: "720p" } },
    { label: "q1 5s 1080p", facts: { credits: 0, duration: 5, resolution: "1080p" } },
    { label: "2.0 4s 360p", facts: { credits: 0, duration: 4, resolution: "360p" } },
    { label: "2.0 4s 720p", facts: { credits: 0, duration: 4, resolution: "720p" } },
    { label: "2.0 8s 720p", facts: { credits: 0, duration: 8, resolution: "720p" } },
  ],
  protocols: [{ name: "openai_responses", supports: ["stream", "sync", "background"] }, "openai_video"],
};

const RESOLUTIONS = ["360p", "540p", "720p", "1080p"];

function trimmed(value) {
  return String(value || "").trim();
}

function isQ2Model(model) {
  return String(model || "").indexOf("viduq2") === 0;
}

function defaultDuration(model) {
  if (model === "vidu2.0") return 4;
  return 5;
}

function defaultResolution(model) {
  if (isQ2Model(model)) return "720p";
  if (model === "vidu2.0") return "360p";
  return "1080p";
}

function normalizeResolution(value, model) {
  if (model === "viduq1") return "1080p";
  const raw = trimmed(value).toLowerCase();
  if (RESOLUTIONS.indexOf(raw) >= 0) return raw;
  const parts = raw.replace("*", "x").split("x");
  if (parts.length === 2) {
    const width = Number(parts[0]);
    const height = Number(parts[1]);
    if (width > 0 && height > 0) {
      const max = Math.max(width, height);
      if (max >= 1920) return "1080p";
      if (max >= 1280) return "720p";
      if (max >= 960) return "540p";
      return "360p";
    }
  }
  return defaultResolution(model);
}

function outboundDuration(req, model) {
  const n = Number(req && req.duration);
  if (Number.isFinite(n) && n > 0) return n;
  return defaultDuration(model);
}

function outboundResolution(req, model) {
  if (req && req.resolution) return normalizeResolution(req.resolution, model);
  const metadata = (req && req.metadata) || {};
  if (metadata.resolution) return normalizeResolution(metadata.resolution, model);
  if (req && req.size) return normalizeResolution(req.size, model);
  return defaultResolution(model);
}

function hasViduImages(req, hasInputReferenceFile) {
  if (hasInputReferenceFile) return true;
  if (Array.isArray(req && req.images) && req.images.length) return true;
  return Boolean(trimmed(req && req.input_reference) || trimmed(req && req.image));
}

function validateViduCombo(model, duration, resolution, hasImages) {
  if (model === "vidu2.0" && !hasImages) {
    throw new Error("vidu2.0 does not support text-to-video");
  }
  if (model === "viduq1") {
    if (duration !== undefined && Number(duration) !== 5) throw new Error("viduq1 duration must be 5");
    return;
  }
  if (model === "vidu2.0") {
    const n = duration === undefined ? 4 : Number(duration);
    if (n === 4) {
      if (["360p", "720p", "1080p"].indexOf(resolution) < 0) throw new Error("vidu2.0 duration 4 only allows resolution 360p, 720p, or 1080p");
      return;
    }
    if (n === 8) {
      if (resolution !== "720p") throw new Error("vidu2.0 duration 8 only allows resolution 720p");
      return;
    }
    throw new Error("vidu2.0 duration must be 4 or 8");
  }
  if (isQ2Model(model)) {
    if (duration === undefined) return;
    const n = Number(duration);
    if (!Number.isInteger(n) || n < 1 || n > 10) throw new Error("viduq2 duration must be between 1 and 10");
    return;
  }
  if (duration !== undefined) {
    const n = Number(duration);
    if (!Number.isInteger(n) || n <= 0 || n > 3600) throw new Error("seconds must be between 1 and 3600");
  }
}

function filePlaceholder(image) {
  if (!image || typeof image !== "object" || Array.isArray(image) || !image.__fileRef) return image;
  const placeholder = { __fileRef: image.__fileRef, encoding: image.encoding };
  if (image.mimeType) placeholder.mimeType = image.mimeType;
  if (image.maxBytes !== undefined && image.maxBytes !== null) placeholder.maxBytes = image.maxBytes;
  return placeholder;
}

function responsesInput(req) {
  const texts = [],
    images = [];
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
        if (["input_image", "image_url"].includes(part.type)) {
          let image = part.image_url;
          if (image && typeof image === "object") image = image.url;
          if (trimmed(image)) images.push(trimmed(image));
        }
      }
    }
  }
  return {
    prompt: texts
      .filter(function (text) {
        return trimmed(text);
      })
      .join("\n"),
    images: images,
  };
}

function responsesVideoText(ctx) {
  const artifact = ctx && ctx.artifacts && ctx.artifacts.video;
  const url = trimmed(artifact && artifact.url);
  if (!url) throw new Error("video artifact is unavailable");
  const escaped = url.replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  return '<video controls src="' + escaped + '"></video>';
}

function actionFor(req) {
  if (req.metadata && req.metadata.action) {
    const aliases = {
      generate: "image_to_video",
      textGenerate: "text_to_video",
      firstTailGenerate: "first_tail_to_video",
      referenceGenerate: "reference_to_video",
      remixGenerate: "remix",
    };
    return aliases[req.metadata.action] || req.metadata.action;
  }
  if (!req.images || req.images.length === 0) return "text_to_video";
  if (req.images.length === 2) return "first_tail_to_video";
  if (req.images.length > 2) return "reference_to_video";
  return "image_to_video";
}

function pathFor(action) {
  if (action === "image_to_video") return "/img2video";
  if (action === "first_tail_to_video") return "/start-end2video";
  if (action === "reference_to_video") return "/reference2video";
  return "/text2video";
}

export function buildSubmitRequest(ctx) {
  const req = ctx.requestBody;
  const action = actionFor(req);
  const metadata = req.metadata || {};
  let model = ctx.upstreamModel || "viduq1";
  if (action === "reference_to_video" && model.includes("viduq2")) model = "viduq2";
  const images = Array.isArray(req.images) ? req.images.map(filePlaceholder) : null;
  const body = Object.assign(
    {
      model: model,
      images: images,
      prompt: req.prompt || null,
      duration: outboundDuration(req, model),
      resolution: outboundResolution(req, model),
      movement_amplitude: "auto",
    },
    metadata
  );
  delete body.action;
  if (!body.prompt) delete body.prompt;
  if (!body.bgm) delete body.bgm;
  if (Array.isArray(body.images)) body.images = body.images.map(filePlaceholder);
  return {
    url: ctx.baseUrl + "/ent/v2" + pathFor(action),
    method: "POST",
    headers: { "Content-Type": "application/json", Accept: "application/json", Authorization: "Token " + ctx.apiKey },
    body: body,
    action: action,
  };
}

export function parseSubmitResponse(ctx, resp) {
  if (!resp.body || resp.body.state === "failed") throw new Error("task failed");
  if (!resp.body.task_id) throw new Error("missing task_id");
  return { taskId: resp.body.task_id, taskData: resp.body };
}

export function extractUsage(ctx) {
  if (ctx.usagePurpose === "billing_ratios") return null;
  const req = ctx.requestBody || {};
  const model = ctx.upstreamModel || req.model;
  return { duration: outboundDuration(req, model), resolution: outboundResolution(req, model) };
}

export function buildQueryRequest(ctx) {
  return {
    url: ctx.baseUrl + "/ent/v2/tasks/" + ctx.taskId + "/creations",
    method: "GET",
    headers: { Accept: "application/json", Authorization: "Token " + ctx.apiKey },
  };
}

export function parseTaskResult(ctx, body) {
  const statuses = { created: "SUBMITTED", queueing: "SUBMITTED", processing: "IN_PROGRESS", success: "SUCCESS", failed: "FAILURE" };
  const status = statuses[body.state];
  if (!status) return { status: "UNKNOWN", reason: "unknown task state: " + String(body.state || "") };
  const url = body.creations && body.creations.length ? body.creations[0].url || "" : "";
  const result = { status: status, reason: body.state === "failed" ? body.err_code || "" : "" };
  if (url) result.url = url;
  return result;
}

function artifactData(ctx) {
  const data = (ctx && ctx.data) || {};
  if (data.data && typeof data.data === "object" && data.data.task_id && Object.prototype.hasOwnProperty.call(data.data, "data")) return data.data.data || {};
  return data;
}

function artifactVideoURL(ctx) {
  const creations = artifactData(ctx).creations;
  return Array.isArray(creations) && creations.length ? String(creations[0].url || "").trim() : "";
}

export function listArtifacts(task) {
  return task.status === "SUCCESS" && artifactVideoURL(task) ? [{ key: "video", type: "video" }] : [];
}

export function buildContentRequest(ctx) {
  if (ctx.artifactKey !== "video") throw new Error("artifact_not_found");
  const url = artifactVideoURL(ctx);
  if (!url) throw new Error("artifact_not_found");
  return { url: url, method: ctx.clientRequest.method, credentialless: true };
}

export function extractUsageOnComplete(_task, _taskResult, body) {
  if (!body || !Object.prototype.hasOwnProperty.call(body, "credits") || body.credits === undefined || body.credits === null) return null;
  const credits = Number(body.credits);
  if (!Number.isFinite(credits)) return null;
  return { credits: credits };
}

export const protocols = {
  openai_responses: {
    decodeRequest: function (ctx) {
      if (!ctx.body || ctx.body.kind !== "json") throw new Error("JSON body required");
      const req = ctx.body.value;
      if (!req || typeof req !== "object" || Array.isArray(req)) throw new Error("request body must be an object");
      const model = trimmed(req.model);
      if (!model) throw new Error("model is required");
      if (req.input !== undefined && typeof req.input !== "string" && !Array.isArray(req.input)) throw new Error("input must be a string or array");
      if (req.images !== undefined && !Array.isArray(req.images)) throw new Error("images must be an array");
      if (req.metadata !== undefined && (!req.metadata || typeof req.metadata !== "object" || Array.isArray(req.metadata)))
        throw new Error("metadata must be an object");
      const input = responsesInput(req);
      const prompt = input.prompt || trimmed(req.prompt);
      const images = [];
      for (const image of [req.image, req.input_reference].concat(req.images || [], input.images)) {
        if (trimmed(image) && !images.includes(trimmed(image))) images.push(trimmed(image));
      }
      if (!prompt && images.length === 0) throw new Error("input is required");
      const requestBody = { model: model, prompt: prompt };
      if (images.length) requestBody.images = images;
      if (Object.prototype.hasOwnProperty.call(req, "seconds")) requestBody.duration = req.seconds;
      else if (Object.prototype.hasOwnProperty.call(req, "duration")) requestBody.duration = req.duration;
      if (Object.prototype.hasOwnProperty.call(req, "size")) requestBody.size = req.size;
      if (Object.prototype.hasOwnProperty.call(req, "metadata")) requestBody.metadata = req.metadata;
      const duration = requestBody.duration === undefined ? undefined : Number(requestBody.duration);
      validateViduCombo(model, duration, outboundResolution(requestBody, model), images.length > 0);
      return { kind: "submit", model: model, action: actionFor(requestBody), requestBody: requestBody };
    },
    renderEvents: function (ctx, task, previousState) {
      const status = String(task.status || "UNKNOWN").toUpperCase();
      const value = Number(String(task.progress || "").replace("%", ""));
      const progress = Number.isFinite(value) && value >= 0 && value <= 100 ? value : null;
      const state = { status: status, progress: progress };
      if (status === "SUCCESS") {
        const text = responsesVideoText(ctx);
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
    renderFinal: function (ctx, _task) {
      return {
        output: [
          {
            type: "message",
            status: "completed",
            role: "assistant",
            content: [{ type: "output_text", text: responsesVideoText(ctx), annotations: [], logprobs: [] }],
          },
        ],
        metadata: { vendor: "vidu" },
      };
    },
  },
};

const legacyRenderers = {
  openai_video: function (task) {
    const statusMap = { NOT_START: "queued", SUBMITTED: "queued", QUEUED: "queued", IN_PROGRESS: "in_progress", SUCCESS: "completed", FAILURE: "failed" };
    const output = {
      id: task.task_id,
      object: "video",
      model: "",
      status: statusMap[task.status] || "unknown",
      progress: Number(String(task.progress || "0").replace("%", "")),
      created_at: task.created_at,
    };
    if (task.updated_at) output.completed_at = task.updated_at;
    if (task.data && task.data.state === "failed" && task.data.err_code) output.error = { message: task.data.err_code, code: task.data.err_code };
    return output;
  },
};

protocols.openai_video = {
  decodeRequest: function (ctx) {
    if (!ctx.body || (ctx.body.kind !== "json" && ctx.body.kind !== "multipart")) throw new Error("JSON or multipart body required");
    let req;
    let hasInputReferenceFile = false;
    if (ctx.body.kind === "json") {
      if (!ctx.body.value || Array.isArray(ctx.body.value)) throw new Error("JSON object required");
      req = Object.assign({}, ctx.body.value);
    } else {
      const first = function (name) {
        const values = (ctx.body.fields || {})[name] || [];
        if (values.length > 1) throw new Error(name + " must be provided once");
        return values[0];
      };
      req = {};
      const fields = ctx.body.fields || {};
      for (const name of Object.keys(fields)) {
        req[name] = first(name);
      }
      for (const file of ctx.body.files || []) {
        if (file.field !== "input_reference") throw new Error("unexpected file field: " + file.field);
        if (hasInputReferenceFile) throw new Error("input_reference must be provided once");
        hasInputReferenceFile = true;
      }
      if (req.metadata !== undefined) {
        let parsed;
        try {
          parsed = JSON.parse(req.metadata);
        } catch (e) {
          throw new Error("metadata must be a JSON object string");
        }
        if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error("metadata must be a JSON object string");
        req.metadata = parsed;
      }
      if (req.seconds !== undefined) req.seconds = Number(req.seconds);
      else if (req.duration !== undefined) req.seconds = Number(req.duration);
    }
    const model = ctx.upstreamModel || ctx.model || req.model;
    const seconds = req.seconds === undefined ? req.duration : req.seconds;
    if (seconds !== undefined) req.duration = Number(seconds);
    else req.duration = defaultDuration(model);
    if (hasInputReferenceFile) {
      req.images = [{ __fileRef: "request_file:input_reference", encoding: "dataUrl", maxBytes: 15728640 }];
    } else {
      const image = trimmed(req.input_reference || req.image);
      if (image && (!Array.isArray(req.images) || req.images.length === 0)) req.images = [image];
    }
    req.resolution = outboundResolution(req, model);
    const hasImages = hasViduImages(req, hasInputReferenceFile);
    validateViduCombo(model, req.duration, req.resolution, hasImages);
    return {
      kind: "submit",
      model: ctx.model,
      action: actionFor(req),
      requestBody: Object.assign({}, req, { model: ctx.model }),
    };
  },
  render: function (ctx, task) {
    return legacyRenderers.openai_video(task);
  },
};

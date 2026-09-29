// Billable dimensions differ per model family: only MiniMax-H3 charges for
// input media, and each family renders its own set of output resolutions.
const VIDEO_SECONDS_FIELD = {
  type: "number",
  unit: "second",
  description: { en: "Video generation unit price", zh: "视频生成单价" },
};

const RESOLUTION_DESCRIPTION = { en: "Output video resolution", zh: "输出视频分辨率" };

// MiniMax-H3 allows duration 4 to 15 and renders 768P or 2K.
const H3_USAGE_SCHEMA = {
  seconds: VIDEO_SECONDS_FIELD,
  resolution: {
    enum: ["768P", "2K"],
    description: RESOLUTION_DESCRIPTION,
  },
  // Input image count (estimated at submit, actual on completion).
  input_images: {
    type: "number",
    unit: "count",
    description: { en: "Input image unit price", zh: "输入图片单价" },
  },
  // Input video duration in seconds (reserved at the request maximum, actual on completion).
  input_video_seconds: {
    type: "number",
    unit: "second",
    description: { en: "Input video unit price", zh: "输入视频单价" },
  },
};

// Hailuo 2.3 and 2.3-Fast allow duration 6 or 10 at 768P or 1080P.
const HAILUO_23_USAGE_SCHEMA = {
  seconds: VIDEO_SECONDS_FIELD,
  resolution: {
    enum: ["768P", "1080P"],
    description: RESOLUTION_DESCRIPTION,
  },
};

// Hailuo 02 keeps the 2.3 durations and adds 512P.
const HAILUO_02_USAGE_SCHEMA = {
  seconds: VIDEO_SECONDS_FIELD,
  resolution: {
    enum: ["512P", "768P", "1080P"],
    description: RESOLUTION_DESCRIPTION,
  },
};

// The 01 series renders 720P only, at a fixed duration of 6 seconds, so no
// other dimension can change its price.
const HAILUO_01_USAGE_SCHEMA = {
  seconds: VIDEO_SECONDS_FIELD,
};

export const meta = {
  apiVersion: 1,
  key: "hailuo",
  name: "Hailuo Video",
  icon: "Hailuo.Color",
  description: {
    en: "MiniMax Hailuo video generation (text-to-video, image-to-video, and MiniMax-H3 multimodal reference)",
    zh: "MiniMax 海螺视频生成（文生视频、图生视频、MiniMax-H3 多模态参考生视频）",
  },
  version: "1.2.0",
  author: { name: "QuantumNous" },
  channelTypes: [35],
  models: [
    "MiniMax-H3",
    "MiniMax-Hailuo-2.3",
    "MiniMax-Hailuo-2.3-Fast",
    "MiniMax-Hailuo-02",
    "T2V-01-Director",
    "T2V-01",
    "I2V-01-Director",
    "I2V-01-live",
    "I2V-01",
    "S2V-01",
  ],
  fetchMode: "per_task",
  // Fallback for a channel alias that resolves to more than one model: the
  // union of every family's fields and resolutions.
  usageSchema: Object.assign({}, H3_USAGE_SCHEMA, {
    resolution: {
      enum: ["512P", "768P", "720P", "1080P", "2K"],
      description: RESOLUTION_DESCRIPTION,
    },
  }),
  usageExamples: [
    { label: "2.3/02 768P 6s", facts: { seconds: 6, resolution: "768P", input_images: 0, input_video_seconds: 0 } },
    { label: "2.3/02 768P 10s", facts: { seconds: 10, resolution: "768P", input_images: 0, input_video_seconds: 0 } },
    { label: "2.3/02 1080P 6s", facts: { seconds: 6, resolution: "1080P", input_images: 0, input_video_seconds: 0 } },
    { label: "02 512P 6s", facts: { seconds: 6, resolution: "512P", input_images: 0, input_video_seconds: 0 } },
    { label: "02 512P 10s", facts: { seconds: 10, resolution: "512P", input_images: 0, input_video_seconds: 0 } },
    { label: "01-series 720P 6s", facts: { seconds: 6, resolution: "720P", input_images: 0, input_video_seconds: 0 } },
    { label: "H3 768P 5s", facts: { seconds: 5, resolution: "768P", input_images: 0, input_video_seconds: 0 } },
    { label: "H3 2K 5s · 9 images", facts: { seconds: 5, resolution: "2K", input_images: 9, input_video_seconds: 0 } },
    { label: "H3 2K 5s · input video", facts: { seconds: 5, resolution: "2K", input_images: 0, input_video_seconds: 15 } },
  ],
  usageProfiles: [
    {
      models: ["MiniMax-H3"],
      schema: H3_USAGE_SCHEMA,
      examples: [
        { label: "H3 768P 5s", facts: { seconds: 5, resolution: "768P", input_images: 0, input_video_seconds: 0 } },
        { label: "H3 2K 5s", facts: { seconds: 5, resolution: "2K", input_images: 0, input_video_seconds: 0 } },
        { label: "H3 2K 5s · 9 images", facts: { seconds: 5, resolution: "2K", input_images: 9, input_video_seconds: 0 } },
        { label: "H3 2K 5s · input video", facts: { seconds: 5, resolution: "2K", input_images: 0, input_video_seconds: 15 } },
      ],
    },
    {
      models: ["MiniMax-Hailuo-2.3", "MiniMax-Hailuo-2.3-Fast"],
      schema: HAILUO_23_USAGE_SCHEMA,
      examples: [
        { label: "2.3 768P 6s", facts: { seconds: 6, resolution: "768P" } },
        { label: "2.3 768P 10s", facts: { seconds: 10, resolution: "768P" } },
        { label: "2.3 1080P 6s", facts: { seconds: 6, resolution: "1080P" } },
      ],
    },
    {
      models: ["MiniMax-Hailuo-02"],
      schema: HAILUO_02_USAGE_SCHEMA,
      examples: [
        { label: "02 512P 6s", facts: { seconds: 6, resolution: "512P" } },
        { label: "02 512P 10s", facts: { seconds: 10, resolution: "512P" } },
        { label: "02 768P 6s", facts: { seconds: 6, resolution: "768P" } },
        { label: "02 768P 10s", facts: { seconds: 10, resolution: "768P" } },
        { label: "02 1080P 6s", facts: { seconds: 6, resolution: "1080P" } },
      ],
    },
    {
      models: ["T2V-01-Director", "T2V-01", "I2V-01-Director", "I2V-01-live", "I2V-01", "S2V-01"],
      schema: HAILUO_01_USAGE_SCHEMA,
      examples: [{ label: "01-series 720P 6s", facts: { seconds: 6 } }],
    },
  ],
  protocols: [{ name: "openai_responses", supports: ["stream", "sync", "background"] }, "openai_video"],
};

function trimmed(value) {
  return String(value || "").trim();
}

function isModernHailuo(model) {
  return model === "MiniMax-Hailuo-2.3" || model === "MiniMax-Hailuo-2.3-Fast" || model === "MiniMax-Hailuo-02";
}

function defaultResolution(model) {
  if (model === "MiniMax-Hailuo-2.3" || model === "MiniMax-Hailuo-2.3-Fast" || model === "MiniMax-Hailuo-02") return "768P";
  return "720P";
}

function resolutionFor(size, model) {
  const value = String(size || "");
  if (value.includes("1080")) return "1080P";
  if (value.includes("768")) return "768P";
  if (value.includes("720")) return isModernHailuo(model) ? "768P" : "720P";
  if (value.includes("512")) return "512P";
  return defaultResolution(model);
}

function outboundDuration(req) {
  const n = Number(req && req.duration);
  if (Number.isFinite(n) && n > 0) return n;
  return 6;
}

function outboundResolution(req, model) {
  if (req && req.resolution) return resolutionFor(req.resolution, model);
  const metadata = (req && req.metadata) || {};
  if (metadata.resolution) return resolutionFor(metadata.resolution, model);
  if (req && req.size) return resolutionFor(req.size, model);
  return defaultResolution(model);
}

function hasHailuoImage(req, hasInputReferenceFile) {
  if (hasInputReferenceFile) return true;
  const metadata = (req && req.metadata) || {};
  return Boolean(
    trimmed(req && req.input_reference) ||
    trimmed(req && req.image) ||
    (Array.isArray(req && req.images) && req.images.length) ||
    metadata.first_frame_image ||
    metadata.last_frame_image ||
    metadata.subject_reference
  );
}

const H3_MODEL = "MiniMax-H3";
const H3_MIN_DURATION = 4;
const H3_MAX_DURATION = 15;
const H3_DEFAULT_DURATION = 5;
const H3_MAX_FRAME_IMAGES = 2;
const H3_MAX_REFERENCE_IMAGES = 9;
const H3_MAX_REFERENCE_VIDEOS = 3;
const H3_MAX_REFERENCE_AUDIOS = 3;
const H3_MAX_INPUT_VIDEO_SECONDS = 15;
const H3_RATIOS = ["adaptive", "21:9", "16:9", "4:3", "1:1", "3:4", "9:16"];

// MiniMax-H3 speaks the /v2 video generation contract: a multimodal `content`
// array instead of flat frame fields, an explicit `ratio`, 768P/2K resolutions,
// a task id path parameter on query, and a `{task: {...}}` query envelope.
function isH3(model) {
  return model === H3_MODEL;
}

function h3Duration(req) {
  const raw = req.duration;
  if (raw === undefined || raw === null || raw === "") return H3_DEFAULT_DURATION;
  const seconds = Number(raw);
  if (!Number.isInteger(seconds) || seconds < H3_MIN_DURATION || seconds > H3_MAX_DURATION) {
    throw new Error(H3_MODEL + " duration must be an integer between " + H3_MIN_DURATION + " and " + H3_MAX_DURATION + " seconds");
  }
  return seconds;
}

function h3Resolution(req) {
  const metadata = req.metadata || {};
  const raw = trimmed(metadata.resolution) || trimmed(req.resolution) || trimmed(req.size);
  if (!raw) return "768P";
  const value = raw.toUpperCase();
  if (value.includes("2K")) return "2K";
  if (value.includes("768")) return "768P";
  throw new Error(H3_MODEL + " resolution must be 768P or 2K");
}

function h3MediaItem(type, url, role) {
  const item = { type: type, role: role };
  item[type] = { url: url };
  return item;
}

// Accepts a single value or an array; file placeholders stay objects and are
// resolved by the host after the body is built.
function h3MediaList(source, key) {
  const raw = source[key];
  if (raw === undefined || raw === null) return [];
  const values = Array.isArray(raw) ? raw : [raw];
  return values.filter(function (value) {
    return value && typeof value === "object" ? true : Boolean(trimmed(value));
  });
}

function h3FrameImages(req) {
  const metadata = req.metadata || {};
  const images = h3MediaList(req, "images");
  if (images.length > H3_MAX_FRAME_IMAGES) throw new Error(H3_MODEL + " accepts at most " + H3_MAX_FRAME_IMAGES + " frame images");
  const frames = [];
  if (metadata.first_frame_image) frames.push(h3MediaItem("image_url", metadata.first_frame_image, "first_frame"));
  if (metadata.last_frame_image) frames.push(h3MediaItem("image_url", metadata.last_frame_image, "last_frame"));
  if (frames.length) return frames;
  return images.map(function (url, index) {
    return h3MediaItem("image_url", url, index === 0 ? "first_frame" : "last_frame");
  });
}

function validateH3Content(items) {
  let hasText = false;
  let hasFrame = false;
  let hasReference = false;
  let firstFrames = 0;
  let lastFrames = 0;
  let referenceImages = 0;
  let referenceVideos = 0;
  let referenceAudios = 0;
  let inputImages = 0;
  for (const item of items) {
    if (!item || typeof item !== "object" || Array.isArray(item)) continue;
    const role = trimmed(item.role);
    if (item.type === "text" && trimmed(item.text)) {
      hasText = true;
      continue;
    }
    if (item.type === "image_url") {
      inputImages += 1;
      if (!role || role === "first_frame") {
        firstFrames += 1;
        hasFrame = true;
      } else if (role === "last_frame") {
        lastFrames += 1;
        hasFrame = true;
      } else if (role === "middle_frame") {
        hasFrame = true;
      } else if (role === "reference_image") {
        referenceImages += 1;
        hasReference = true;
      }
      continue;
    }
    if (item.type === "video_url") {
      referenceVideos += 1;
      hasReference = true;
      continue;
    }
    if (item.type === "audio_url") {
      referenceAudios += 1;
      hasReference = true;
    }
  }
  if (!hasText) throw new Error(H3_MODEL + " requires a non-empty text item");
  if (firstFrames > 1) throw new Error(H3_MODEL + " accepts at most one first_frame image");
  if (lastFrames > 1) throw new Error(H3_MODEL + " accepts at most one last_frame image");
  if (referenceImages > H3_MAX_REFERENCE_IMAGES) throw new Error(H3_MODEL + " accepts at most " + H3_MAX_REFERENCE_IMAGES + " reference images");
  if (inputImages > H3_MAX_REFERENCE_IMAGES) throw new Error(H3_MODEL + " accepts at most " + H3_MAX_REFERENCE_IMAGES + " input images");
  if (referenceVideos > H3_MAX_REFERENCE_VIDEOS) throw new Error(H3_MODEL + " accepts at most " + H3_MAX_REFERENCE_VIDEOS + " reference videos");
  if (referenceAudios > H3_MAX_REFERENCE_AUDIOS) throw new Error(H3_MODEL + " accepts at most " + H3_MAX_REFERENCE_AUDIOS + " reference audios");
  if (hasFrame && hasReference) throw new Error(H3_MODEL + " cannot mix frame images with reference media");
  return items;
}

// metadata.content is the full multimodal passthrough; otherwise the content
// array is assembled from prompt, frame images, and reference media.
function h3Content(req) {
  const metadata = req.metadata || {};
  const prompt = trimmed(req.prompt);
  if (metadata.content !== undefined && metadata.content !== null) {
    if (!Array.isArray(metadata.content)) throw new Error("metadata.content must be an array");
    const items = metadata.content;
    const hasText = items.some(function (item) {
      return item && item.type === "text" && trimmed(item.text);
    });
    if (hasText) return validateH3Content(items);
    if (!prompt) throw new Error(H3_MODEL + " metadata.content requires a text item or a prompt");
    return validateH3Content([{ type: "text", text: prompt }].concat(items));
  }
  const content = prompt ? [{ type: "text", text: prompt }] : [];
  for (const frame of h3FrameImages(req)) content.push(frame);
  const videos = h3MediaList(metadata, "reference_video");
  if (videos.length > H3_MAX_REFERENCE_VIDEOS) throw new Error(H3_MODEL + " accepts at most " + H3_MAX_REFERENCE_VIDEOS + " reference videos");
  for (const video of videos) content.push(h3MediaItem("video_url", video, "reference_video"));
  const audios = h3MediaList(metadata, "reference_audio");
  if (audios.length > H3_MAX_REFERENCE_AUDIOS) throw new Error(H3_MODEL + " accepts at most " + H3_MAX_REFERENCE_AUDIOS + " reference audios");
  for (const audio of audios) content.push(h3MediaItem("audio_url", audio, "reference_audio"));
  if (!content.length) throw new Error(H3_MODEL + " requires a prompt or a media input");
  return validateH3Content(content);
}

function h3HasVisualContent(content) {
  return content.some(function (item) {
    return item && (item.type === "image_url" || item.type === "video_url");
  });
}

// ratio is mandatory upstream and `adaptive` is only meaningful when the
// aspect ratio can be inherited from a visual input.
function h3Ratio(req, content) {
  const metadata = req.metadata || {};
  const ratio = trimmed(metadata.ratio);
  if (!ratio) return h3HasVisualContent(content) ? "adaptive" : "16:9";
  if (!H3_RATIOS.includes(ratio)) throw new Error(H3_MODEL + " ratio must be one of " + H3_RATIOS.join(", "));
  if (ratio === "adaptive" && !h3HasVisualContent(content)) throw new Error(H3_MODEL + " ratio adaptive requires an image or video input");
  return ratio;
}

function h3QueryTask(body) {
  const task = body && typeof body === "object" && !Array.isArray(body) ? body.task : null;
  return task && typeof task === "object" && !Array.isArray(task) ? task : null;
}

function h3APIError(body) {
  const error = body && typeof body === "object" && !Array.isArray(body) ? body.error : null;
  if (!error || typeof error !== "object" || Array.isArray(error)) return null;
  const message = trimmed(error.message);
  if (!message) return null;
  const statusCode = Number(error.http_code || error.code || 0);
  return { message: message, statusCode: Number.isInteger(statusCode) ? statusCode : 0 };
}

// Older T2V-01*/I2V-01*/S2V-01 official tables disagree on 1080P support (research: 未验证).
// Keep those models permissive: duration 6 only, resolution optional.
function validateHailuoCombo(model, duration, resolution, hasImage) {
  if (isH3(model)) return;
  if (model === "MiniMax-Hailuo-2.3-Fast" && !hasImage) {
    throw new Error("MiniMax-Hailuo-2.3-Fast supports image-to-video only");
  }
  if (!isModernHailuo(model)) {
    if (duration !== undefined && Number(duration) !== 6) throw new Error(model + " duration must be 6");
    return;
  }
  const n = duration === undefined ? 6 : Number(duration);
  if (n !== 6 && n !== 10) throw new Error(model + " duration must be 6 or 10");
  if (n === 10) {
    if (model === "MiniMax-Hailuo-02" && hasImage) {
      if (resolution !== "768P" && resolution !== "512P") throw new Error("MiniMax-Hailuo-02 duration 10 only allows resolution 768P or 512P");
      return;
    }
    if (resolution !== "768P") throw new Error(model + " duration 10 only allows resolution 768P");
    return;
  }
  const allowed = model === "MiniMax-Hailuo-02" && hasImage ? ["512P", "768P", "1080P"] : ["768P", "1080P"];
  if (allowed.indexOf(resolution) < 0) throw new Error(model + " duration 6 only allows resolution " + allowed.join(" or "));
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

export function buildSubmitRequest(ctx) {
  const req = ctx.requestBody || {};
  const model = ctx.upstreamModel;
  const metadata = req.metadata || {};
  if (isH3(model)) {
    const content = h3Content(req);
    const h3Body = {
      model: model,
      content: content,
      resolution: h3Resolution(req),
      duration: h3Duration(req),
      ratio: h3Ratio(req, content),
    };
    ["callback_url", "aigc_watermark"].forEach(function (key) {
      if (metadata[key] !== undefined && metadata[key] !== null) h3Body[key] = metadata[key];
    });
    return {
      url: ctx.baseUrl + "/v2/video_generation",
      method: "POST",
      headers: { "Content-Type": "application/json", Accept: "application/json", Authorization: "Bearer " + ctx.apiKey },
      body: h3Body,
      action: h3HasVisualContent(content) ? "image_to_video" : "text_to_video",
    };
  }
  const body = {
    model: model,
    prompt: req.prompt || undefined,
    duration: outboundDuration(req),
    resolution: outboundResolution(req, model),
  };
  ["prompt_optimizer", "fast_pretreatment", "callback_url", "aigc_watermark", "first_frame_image", "last_frame_image", "subject_reference"].forEach(
    function (key) {
      if (metadata[key] !== undefined && metadata[key] !== null) body[key] = metadata[key];
    }
  );
  return {
    url: ctx.baseUrl + "/v1/video_generation",
    method: "POST",
    headers: { "Content-Type": "application/json", Accept: "application/json", Authorization: "Bearer " + ctx.apiKey },
    body: body,
    action: hasHailuoImage(req, false) ? "image_to_video" : "text_to_video",
  };
}

export function parseSubmitResponse(ctx, resp) {
  const body = resp.body || {};
  const apiError = isH3(ctx.upstreamModel) ? h3APIError(body) : null;
  if (apiError) throw new Error(apiError.message);
  const base = body.base_resp;
  // /v1 always wraps the create response in a base_resp envelope; /v2 returns a
  // bare task_id and only adds base_resp when the call is rejected.
  if (base) {
    if (base.status_code !== 0) throw new Error(base.status_msg || "hailuo submit failed");
  } else if (!isH3(ctx.upstreamModel)) {
    throw new Error("hailuo submit failed");
  }
  if (!body.task_id) throw new Error("missing task_id");
  return { taskId: body.task_id, taskData: body };
}

export function extractUsage(ctx) {
  if (ctx.usagePurpose === "billing_ratios") return null;
  const req = ctx.requestBody || {};
  const model = ctx.upstreamModel || req.model;
  if (isH3(model)) {
    const content = h3Content(req);
    return {
      seconds: h3Duration(req),
      resolution: h3Resolution(req),
      input_images: content.filter(function (item) {
        return item && item.type === "image_url";
      }).length,
      // Input URLs do not expose duration. Reserve the documented total limit;
      // polling replaces it with usage.input_seconds after success.
      input_video_seconds: content.some(function (item) {
        return item && item.type === "video_url";
      })
        ? H3_MAX_INPUT_VIDEO_SECONDS
        : 0,
    };
  }
  return { seconds: outboundDuration(req), resolution: outboundResolution(req, model), input_images: 0, input_video_seconds: 0 };
}

export function buildQueryRequest(ctx) {
  // Polling carries no relay info; the host fills these identities from the
  // persisted task properties.
  const path = isH3(ctx.upstreamModel || ctx.model)
    ? "/v2/query/video_generation/" + encodeURIComponent(ctx.taskId)
    : "/v1/query/video_generation?task_id=" + encodeURIComponent(ctx.taskId);
  return {
    url: ctx.baseUrl + path,
    method: "GET",
    headers: { Accept: "application/json", Authorization: "Bearer " + ctx.apiKey },
  };
}

export function parseTaskResult(ctx, body) {
  const apiError = h3APIError(body);
  if (apiError) {
    if (apiError.statusCode === 408 || apiError.statusCode === 429 || apiError.statusCode >= 500) throw new Error(apiError.message);
    return { code: apiError.statusCode, status: "FAILURE", progress: "100%", reason: apiError.message };
  }
  const h3Task = h3QueryTask(body);
  if (h3Task) {
    const h3Statuses = { queued: "QUEUED", running: "IN_PROGRESS", succeeded: "SUCCESS", failed: "FAILURE", cancelled: "FAILURE" };
    const h3Status = h3Statuses[h3Task.status];
    if (!h3Status) {
      return { status: "UNKNOWN", reason: "unrecognized status: " + String(h3Task.status || "") };
    }
    const h3Result = { code: 0, status: h3Status, progress: h3Status === "QUEUED" ? "30%" : h3Status === "IN_PROGRESS" ? "50%" : "100%" };
    if (h3Status === "SUCCESS") {
      const url = trimmed(h3Task.content && h3Task.content.url);
      if (url) h3Result.url = url;
    }
    if (h3Status === "FAILURE") {
      h3Result.reason = trimmed(h3Task.error && h3Task.error.message) || "task " + trimmed(h3Task.status);
    }
    return h3Result;
  }
  if (body.base_resp && body.base_resp.status_code !== 0) {
    return { code: body.base_resp.status_code || 0, status: "FAILURE", progress: "100%", reason: body.base_resp.status_msg || "" };
  }
  const base = body.base_resp || {};
  const statuses = { Preparing: "IN_PROGRESS", Queueing: "IN_PROGRESS", Processing: "IN_PROGRESS", Success: "SUCCESS", Fail: "FAILURE" };
  const status = statuses[body.status];
  if (!status) {
    return { status: "UNKNOWN", reason: "unrecognized status: " + String(body.status || "") };
  }
  const progress = status === "SUCCESS" || status === "FAILURE" ? "100%" : body.status === "Processing" ? "50%" : "30%";
  const reason = status === "FAILURE" ? "task failed" : "";
  return { code: base.status_code || 0, status: status, progress: progress, reason: reason };
}

function artifactData(ctx) {
  const data = (ctx && ctx.data) || {};
  if (data.data && typeof data.data === "object" && data.data.task_id && Object.prototype.hasOwnProperty.call(data.data, "data")) return data.data.data || {};
  return data;
}

function artifactFileID(ctx) {
  return trimmed(artifactData(ctx).file_id);
}

// /v2 tasks expose a public CDN URL instead of a downloadable file id.
function h3ArtifactURL(ctx) {
  const task = h3QueryTask(artifactData(ctx));
  return task ? trimmed(task.content && task.content.url) : "";
}

export function listArtifacts(task) {
  if (task.status !== "SUCCESS") return [];
  return artifactFileID(task) || h3ArtifactURL(task) ? [{ key: "video", type: "video", mimeType: "video/mp4" }] : [];
}

export function buildContentRequest(ctx) {
  if (ctx.artifactKey !== "video") throw new Error("artifact_not_found");
  const fileID = artifactFileID(ctx);
  if (!fileID) {
    const url = h3ArtifactURL(ctx);
    if (!url) throw new Error("artifact_not_found");
    return { url: url, method: ctx.clientRequest.method, credentialless: true };
  }
  return {
    url: ctx.baseUrl + "/v1/files/download?file_id=" + encodeURIComponent(fileID),
    method: ctx.clientRequest.method,
    headers: { Accept: "video/*", Authorization: "Bearer " + ctx.apiKey },
  };
}

export function extractUsageOnComplete(_task, _taskResult, body) {
  const h3Task = h3QueryTask(body);
  if (h3Task) {
    const resolution = trimmed(h3Task.resolution).toUpperCase();
    const facts = {};
    if (resolution === "2K" || resolution === "768P") facts.resolution = resolution;
    const usage = h3Task.usage && typeof h3Task.usage === "object" && !Array.isArray(h3Task.usage) ? h3Task.usage : {};
    const fields = [
      { key: "seconds", value: usage.output_seconds, minimum: H3_MIN_DURATION, maximum: H3_MAX_DURATION, integer: false },
      { key: "input_images", value: usage.input_image_count, minimum: 0, maximum: H3_MAX_REFERENCE_IMAGES, integer: true },
      { key: "input_video_seconds", value: usage.input_seconds, minimum: 0, maximum: H3_MAX_INPUT_VIDEO_SECONDS, integer: false },
    ];
    // Omit malformed or out-of-contract upstream values so settlement keeps
    // the bounded submission estimate instead of accepting a new multiplier.
    for (const field of fields) {
      if (field.value === undefined || field.value === null || field.value === "") continue;
      const value = Number(field.value);
      if (!Number.isFinite(value) || value < field.minimum || value > field.maximum || (field.integer && !Number.isInteger(value))) continue;
      facts[field.key] = value;
    }
    return Object.keys(facts).length ? facts : null;
  }
  const width = Number((body || {}).video_width || 0);
  const height = Number((body || {}).video_height || 0);
  if (!(width > 0) || !(height > 0)) return null;
  return { resolution: resolutionFor(width + "x" + height, "") };
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
      const metadata = Object.assign({}, req.metadata || {});
      if (images.length && !metadata.first_frame_image) metadata.first_frame_image = images[0];
      if (images.length > 1 && !metadata.last_frame_image) metadata.last_frame_image = images[1];
      const requestBody = { model: model, prompt: prompt, metadata: metadata };
      if (images.length) requestBody.images = images;
      if (Object.prototype.hasOwnProperty.call(req, "seconds")) requestBody.duration = req.seconds;
      else if (Object.prototype.hasOwnProperty.call(req, "duration")) requestBody.duration = req.duration;
      if (Object.prototype.hasOwnProperty.call(req, "size")) requestBody.size = req.size;
      else if (Object.prototype.hasOwnProperty.call(req, "resolution")) requestBody.size = req.resolution;
      return { kind: "submit", model: model, action: images.length ? "image_to_video" : "text_to_video", requestBody: requestBody };
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
        metadata: { vendor: "hailuo" },
      };
    },
  },
};

const legacyRenderers = {
  openai_video: function (task) {
    const statuses = { NOT_START: "queued", SUBMITTED: "queued", QUEUED: "queued", IN_PROGRESS: "in_progress", SUCCESS: "completed", FAILURE: "failed" };
    const output = {
      id: task.task_id,
      object: "video",
      model: task.properties && task.properties.origin_model_name ? task.properties.origin_model_name : "",
      status: statuses[task.status] || "unknown",
      progress: Number(String(task.progress || "0").replace("%", "")),
      created_at: task.created_at,
    };
    if (task.updated_at) output.completed_at = task.updated_at;
    if (task.data && task.data.base_resp && task.data.base_resp.status_code !== 0) {
      output.error = { message: task.data.base_resp.status_msg, code: String(task.data.base_resp.status_code) };
    }
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
    const seconds = req.seconds === undefined ? req.duration : req.seconds;
    if (seconds !== undefined) req.duration = Number(seconds);
    if (hasInputReferenceFile) {
      req.metadata = Object.assign({}, req.metadata || {}, {
        first_frame_image: { __fileRef: "request_file:input_reference", encoding: "dataUrl", maxBytes: 20971520 },
      });
    } else {
      const image = trimmed(req.input_reference || req.image);
      if (image) {
        req.metadata = Object.assign({}, req.metadata || {});
        if (!req.metadata.first_frame_image) req.metadata.first_frame_image = image;
      }
    }
    const hasImage = hasHailuoImage(req, hasInputReferenceFile);
    const duration = req.duration === undefined ? undefined : Number(req.duration);
    const comboModel = ctx.upstreamModel || ctx.model;
    validateHailuoCombo(comboModel, duration, outboundResolution(req, comboModel), hasImage);
    return {
      kind: "submit",
      model: ctx.model,
      action: hasImage ? "image_to_video" : "text_to_video",
      requestBody: Object.assign({}, req, { model: ctx.model }),
    };
  },
  render: function (ctx, task) {
    return legacyRenderers.openai_video(task);
  },
};

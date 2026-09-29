// Seedance capabilities from the Ark model list, not prices: the output
// resolutions each model offers, whether it accepts reference video input
// (Seedance 2.x prices video-to-video tokens separately) and whether audio
// output is priced separately (Seedance 1.5 pro; generate_audio defaults to
// true). Seedance 1.0 lite is no longer listed and keeps the 1.0 pro tiers.
const VIDEO_MODELS = {
  "doubao-seedance-1-0-pro-250528": { resolutions: ["480p", "720p", "1080p"] },
  "doubao-seedance-1-0-lite-t2v": { resolutions: ["480p", "720p", "1080p"] },
  "doubao-seedance-1-0-lite-i2v": { resolutions: ["480p", "720p", "1080p"] },
  "doubao-seedance-1-5-pro-251215": { resolutions: ["480p", "720p", "1080p"], audio: true },
  "doubao-seedance-2-0-260128": { resolutions: ["480p", "720p", "1080p", "4k"], videoInput: true },
  "doubao-seedance-2-0-fast-260128": { resolutions: ["480p", "720p"], videoInput: true },
  "doubao-seedance-2-0-mini-260615": { resolutions: ["480p", "720p"], videoInput: true },
  "doubao-seedance-2-5-260628": { resolutions: ["480p", "720p", "1080p"], videoInput: true },
};
// Every Ark resolution tier; an endpoint ID reached through channel mapping
// without a declared profile keeps them all.
const SEEDANCE_RESOLUTIONS = ["480p", "720p", "1080p", "4k"];
const DEFAULT_VIDEO_PROFILE = { resolutions: SEEDANCE_RESOLUTIONS, videoInput: true };

// Protocol capabilities from the Ark image generation API reference, not prices.
// presets: accepted `size` resolution tiers; minPixels/maxPixels bound `WxH` sizes.
// outputFormat/tools/fastPromptMode mirror the per-model support lists in the API
// reference and were confirmed by live 400s; background marks the models whose
// documented transparency combination rules are enforced locally.
const IMAGE_MODELS = {
  "doubao-seedream-5-0-pro-260628": {
    presets: ["1K", "1.5K", "2K"],
    minPixels: 921600,
    maxPixels: 4624220,
    maxReferenceImages: 10,
    sequential: false,
    layers: true,
    outputFormat: true,
    background: true,
    tools: false,
    fastPromptMode: true,
  },
  "doubao-seedream-5-0-lite-260128": {
    presets: ["2K", "3K", "4K"],
    minPixels: 3686400,
    maxPixels: 16777216,
    maxReferenceImages: 14,
    sequential: true,
    layers: false,
    outputFormat: true,
    background: false,
    tools: true,
    fastPromptMode: false,
  },
  "doubao-seedream-5-0-260128": {
    presets: ["2K", "3K", "4K"],
    minPixels: 3686400,
    maxPixels: 16777216,
    maxReferenceImages: 14,
    sequential: true,
    layers: false,
    outputFormat: true,
    background: false,
    tools: true,
    fastPromptMode: false,
  },
  "doubao-seedream-4-5-251128": {
    presets: ["2K", "4K"],
    minPixels: 3686400,
    maxPixels: 16777216,
    maxReferenceImages: 14,
    sequential: true,
    layers: false,
    outputFormat: false,
    background: false,
    tools: false,
    fastPromptMode: false,
  },
  "doubao-seedream-4-0-250828": {
    presets: ["1K", "2K", "4K"],
    minPixels: 921600,
    maxPixels: 16777216,
    maxReferenceImages: 14,
    sequential: true,
    layers: false,
    outputFormat: false,
    background: false,
    tools: false,
    fastPromptMode: true,
  },
};
const IMAGE_RESOLUTIONS = ["1K", "1.5K", "2K", "3K", "4K"];
// Square pixel area of each preset, used only for the submit-time tier estimate.
const IMAGE_PRESET_PIXELS = { "1K": 1048576, "1.5K": 2359296, "2K": 4194304, "3K": 9437184, "4K": 16777216 };
// Endpoint IDs and newer models reach the image route without a profile; accept every documented option.
const DEFAULT_IMAGE_PROFILE = {
  presets: IMAGE_RESOLUTIONS,
  minPixels: 921600,
  maxPixels: 16777216,
  maxReferenceImages: 14,
  sequential: true,
  layers: true,
  outputFormat: true,
  background: true,
  tools: true,
  fastPromptMode: true,
};
const LAYER_SIZE_PRESETS = ["1K", "1.5K", "2K"];
const IMAGE_ACTIONS = ["text_to_image", "image_to_image"];
// Official Seedream pricing boundary: every output image at or below 2.61
// megapixels (1.5K and below) is priced separately from images above it.
const IMAGE_TIER_MAX_PIXELS = 2610000;
// Documented output ceiling: 15 images per group request, or one base image plus 16 layers.
const MAX_IMAGE_OUTPUTS = 17;
// Documented reference-image ceiling across Seedream models; bounds usage.input_images.
const MAX_REFERENCE_IMAGES = 14;
// Base64 references must be `data:image/<lowercase format>;base64,<payload>`.
const IMAGE_DATA_URI = /^data:image\/[a-z0-9.+-]+;base64,[A-Za-z0-9+/]+={0,2}$/;
// Responses fields copied verbatim into the Ark body. `background` is excluded:
// on /v1/responses it is the host's boolean background-execution flag, not Ark's
// string transparency option. `tools` is filtered separately.
const IMAGE_REQUEST_KEYS = [
  "size",
  "seed",
  "guidance_scale",
  "response_format",
  "watermark",
  "optimize_prompt",
  "optimize_prompt_options",
  "sequential_image_generation",
  "sequential_image_generation_options",
  "output_format",
  "layer_decomposition",
];

const IMAGE_UNIT_LABEL = { en: "image", zh: "张", "zh-TW": "張", fr: "image", ja: "枚", ru: "изображение", vi: "ảnh" };
// Each output image is billed at its own pixel tier (official pricing bills
// layers individually). Estimated at submit from the requested size and count;
// settled from data[].size per successful image.
const IMAGE_USAGE_SCHEMA = {
  // Successful output images at or below IMAGE_TIER_MAX_PIXELS.
  images_up_to_1_5k: {
    type: "number",
    unit: "count",
    unitLabel: IMAGE_UNIT_LABEL,
    description: { en: "Image generation unit price (1.5K and below)", zh: "图片生成单价（1.5K 及以下）" },
  },
  // Successful output images above IMAGE_TIER_MAX_PIXELS.
  images_above_1_5k: {
    type: "number",
    unit: "count",
    unitLabel: IMAGE_UNIT_LABEL,
    description: { en: "Image generation unit price (above 1.5K)", zh: "图片生成单价（1.5K 以上）" },
  },
  // Reference images: request count at submit, usage.input_images on completion.
  // Seedream 5.0 pro charges from the second image; the expression carries that
  // rule, for example max(u("input_images") - 1, 0) * price.
  input_images: {
    type: "number",
    unit: "count",
    unitLabel: IMAGE_UNIT_LABEL,
    description: { en: "Input image unit price", zh: "输入图片单价" },
  },
  // Layer decomposition outputs (base image plus layers) are priced per layer at their own rate.
  layer_decomposition: {
    type: "boolean",
    description: { en: "Whether layer decomposition is enabled", zh: "是否开启图层拆分" },
  },
};

// Usage facts for one Seedance capability profile: the pricing table then
// lists only the resolutions and input kinds the model offers.
function seedanceUsageSchema(profile) {
  const resolutionLabels = {};
  for (const resolution of profile.resolutions) resolutionLabels[resolution] = { en: resolution, zh: resolution };
  const schema = {
    // Upstream billing tokens (estimated at submit, actual on completion).
    tokens: {
      type: "number",
      unit: "token",
      description: { en: "Billing token unit price", zh: "计费 Token 单价" },
    },
    // Output video resolution; Seedance token unit price varies by resolution tier.
    resolution: {
      enum: profile.resolutions,
      enumLabels: resolutionLabels,
      description: { en: "Output video resolution", zh: "输出视频分辨率" },
    },
  };
  // Whether the request includes reference video input; Seedance 2.x prices video-to-video tokens at a lower unit rate.
  if (profile.videoInput) {
    schema.video_input = {
      enum: ["none", "video"],
      enumLabels: { none: { en: "No reference video", zh: "无参考视频" }, video: { en: "With reference video", zh: "有参考视频" } },
      description: { en: "Reference video input", zh: "参考视频输入" },
    };
  }
  // Seedance 1.5 pro prices videos with and without audio differently.
  if (profile.audio) {
    schema.generate_audio = {
      type: "boolean",
      description: { en: "Whether audio is generated", zh: "是否生成音频" },
    };
  }
  return schema;
}

// Display examples for a token-priced profile: 5-second videos at each tier
// from the official Ark formula tokens = (input + output seconds) × W × H ×
// 24 / 1024 with 16:9 max-pixel sizes, cross-checked against Volcengine price
// examples, plus the reference-video and silent variants the profile prices.
function seedanceUsageExamples(profile) {
  const specs = profile.resolutions.map((resolution) => ({
    label: resolution + " · 5s" + (profile.audio ? " · 有声" : ""),
    resolution: resolution,
    seconds: 5,
  }));
  if (profile.videoInput) specs.push({ label: "720p · 5s (+4s 输入视频)", resolution: "720p", seconds: 9, video_input: "video" });
  if (profile.audio) specs.push({ label: "720p · 5s · 无声", resolution: "720p", seconds: 5, generate_audio: false });
  return specs.map((spec) => {
    const facts = { tokens: Math.round(estimateTokens(spec.seconds, spec.resolution)), resolution: spec.resolution };
    if (profile.videoInput) facts.video_input = spec.video_input || "none";
    if (profile.audio) facts.generate_audio = spec.generate_audio !== false;
    return { label: spec.label, facts: facts };
  });
}

// One usage profile per distinct capability shape.
function seedanceUsageProfiles() {
  const profiles = [];
  for (const model of Object.keys(VIDEO_MODELS)) {
    const capability = VIDEO_MODELS[model];
    const shape = capability.resolutions.join("/") + (capability.videoInput ? "+video" : "") + (capability.audio ? "+audio" : "");
    let profile = profiles.find((entry) => entry.shape === shape);
    if (!profile) {
      profile = { shape: shape, models: [], schema: seedanceUsageSchema(capability), examples: seedanceUsageExamples(capability) };
      profiles.push(profile);
    }
    profile.models.push(model);
  }
  return profiles.map((profile) => ({ models: profile.models, schema: profile.schema, examples: profile.examples }));
}

export const meta = {
  apiVersion: 1,
  key: "doubao",
  name: "Doubao",
  icon: "Doubao.Color",
  description: {
    en: "Volcengine Doubao Seedance video generation and Seedream image generation",
    zh: "火山引擎豆包 Seedance 视频生成与 Seedream 图片生成",
  },
  version: "1.1.0",
  author: { name: "QuantumNous" },
  channelTypes: [54, 45], // VolcEngine-type channels serve Ark video models with the same wire format
  models: Object.keys(VIDEO_MODELS).concat(Object.keys(IMAGE_MODELS)),
  fetchMode: "per_task",
  upstreams: ["vendor", "new_api"],
  usageSchema: seedanceUsageSchema(DEFAULT_VIDEO_PROFILE),
  usageExamples: seedanceUsageExamples(DEFAULT_VIDEO_PROFILE),
  usageProfiles: seedanceUsageProfiles().concat([
    {
      models: Object.keys(IMAGE_MODELS),
      schema: IMAGE_USAGE_SCHEMA,
      examples: [
        { label: "2K · 1 张", facts: { images_up_to_1_5k: 0, images_above_1_5k: 1, input_images: 0, layer_decomposition: false } },
        { label: "1K · 1 张 · 2 张参考图", facts: { images_up_to_1_5k: 1, images_above_1_5k: 0, input_images: 2, layer_decomposition: false } },
        { label: "2K · 4 张组图", facts: { images_up_to_1_5k: 0, images_above_1_5k: 4, input_images: 0, layer_decomposition: false } },
        { label: "图层拆分 · 2K 底图 + 4 层 1.5K", facts: { images_up_to_1_5k: 4, images_above_1_5k: 1, input_images: 1, layer_decomposition: true } },
      ],
    },
  ]),
  routes: [
    { method: "POST", path: "/doubao/api/v3/contents/generations/tasks", type: "submit", decode: "createTask", render: "taskCreated" },
    { method: "GET", path: "/doubao/api/v3/contents/generations/tasks/:task_id", type: "query", render: "taskStatus" },
    // The vendor image API answers synchronously and has no task to re-query;
    // the complete response is delivered once and never persisted.
    { method: "POST", path: "/doubao/api/v3/images/generations", type: "submit", decode: "createImage", render: "imageCreated", retainResult: false },
  ],
  protocols: [
    { name: "openai_responses", supports: ["stream", "sync", "background"] },
    { name: "openai_video", models: Object.keys(VIDEO_MODELS) },
  ],
};

function trimmed(value) {
  return String(value || "").trim();
}

// Another New API gateway serves the Ark wire format only on this plugin's
// prefixed native routes; Ark itself serves the unprefixed paths.
function apiRoot(ctx) {
  return ctx.baseUrl + (ctx.upstream && ctx.upstream.kind === "new_api" ? "/doubao" : "");
}

function objectValue(value, name) {
  if (value === undefined || value === null) return {};
  if (typeof value !== "object" || Array.isArray(value)) throw new Error(name + " must be an object");
  return value;
}

function draftTaskIds(content) {
  const ids = [];
  if (!Array.isArray(content)) return ids;
  for (const item of content) {
    if (!item || typeof item !== "object" || Array.isArray(item)) continue;
    if (item.type !== "draft_task") continue;
    const draft = item.draft_task;
    if (!draft || typeof draft !== "object" || Array.isArray(draft)) continue;
    const id = trimmed(draft.id);
    if (id) ids.push(id);
  }
  return ids;
}

function rewriteDraftTaskContent(content, originTasks) {
  if (!Array.isArray(content)) return content;
  return content.map(function (item) {
    if (!item || typeof item !== "object" || Array.isArray(item) || item.type !== "draft_task") return item;
    const draft = item.draft_task;
    if (!draft || typeof draft !== "object" || Array.isArray(draft) || !trimmed(draft.id)) return item;
    const publicId = trimmed(draft.id);
    let upstream = "";
    if (Array.isArray(originTasks)) {
      for (const task of originTasks) {
        if (task && task.taskId === publicId) {
          upstream = trimmed(task.upstreamTaskId);
          break;
        }
      }
    }
    if (!upstream) throw new Error("origin task is unavailable");
    return Object.assign({}, item, { draft_task: Object.assign({}, draft, { id: upstream }) });
  });
}

function normalizeResolution(value) {
  const raw = trimmed(value).toLowerCase();
  if (["480p", "720p", "1080p", "4k"].includes(raw)) return raw;
  const parts = raw.replace("*", "x").split("x");
  if (parts.length !== 2) return "720p";
  const max = Math.max(Number(parts[0]), Number(parts[1]));
  if (max >= 3840) return "4k";
  if (max >= 1920) return "1080p";
  if (max >= 1280) return "720p";
  return "480p";
}

function hasVideo(content) {
  return Array.isArray(content) && content.some((item) => item && (item.type === "video_url" || Object.prototype.hasOwnProperty.call(item, "video_url")));
}

// Capability profile of the executing Seedance model. Channel mapping may send
// a declared model to an Ark endpoint ID; the declared name still describes it.
function videoProfile(ctx) {
  for (const model of [ctx && ctx.upstreamModel, ctx && ctx.model].map(trimmed)) {
    if (Object.prototype.hasOwnProperty.call(VIDEO_MODELS, model)) return VIDEO_MODELS[model];
  }
  return DEFAULT_VIDEO_PROFILE;
}

// Resolution tier the reservation is estimated at: the requested tier or the
// tier of a WxH size, rejecting tiers the model does not offer. A request that
// leaves the resolution to Ark reserves the model's highest tier up to 1080p;
// completion overlays the delivered resolution.
function videoResolution(ctx) {
  const req = ctx.requestBody || {};
  const metadata = req.metadata || {};
  const profile = videoProfile(ctx);
  const raw = trimmed(metadata.resolution || req.size).toLowerCase();
  const recognized = SEEDANCE_RESOLUTIONS.includes(raw) || raw.replace("*", "x").split("x").length === 2;
  if (!recognized) return profile.resolutions.includes("1080p") ? "1080p" : profile.resolutions[profile.resolutions.length - 1];
  const resolution = normalizeResolution(raw);
  if (!profile.resolutions.includes(resolution))
    throw new Error(trimmed(ctx.upstreamModel || ctx.model) + " resolution must be one of " + profile.resolutions.join(", "));
  return resolution;
}

// Max-pixel 16:9 dimensions per resolution tier. Used when ratio is absent or
// adaptive so the submit-time estimate overestimates rather than underestimates.
// Official Ark formula: tokens = seconds × width × height × 24 / 1024.
// Video input duration is omitted; extractUsageOnComplete overlays the real bill.
function resolutionMaxPixels(resolution) {
  if (resolution === "480p") return [854, 480];
  if (resolution === "1080p") return [1920, 1080];
  if (resolution === "4k") return [3840, 2160];
  return [1280, 720];
}

function estimateTokens(seconds, resolution) {
  const dims = resolutionMaxPixels(resolution);
  return (seconds * dims[0] * dims[1] * 24) / 1024;
}

function videoInputRatio(model, resolution, content) {
  const video = hasVideo(content);
  const res = trimmed(resolution).toLowerCase();
  if (model === "doubao-seedance-2-5-260628") {
    if (res === "1080p") return video ? 7.0 / 10.7 : 11.7 / 10.7;
    return video ? 42 / 70 : 1;
  }
  if (model === "doubao-seedance-2-0-260128") {
    if (res === "1080p") return video ? 31 / 46 : 51 / 46;
    if (res === "4k") return video ? 16 / 46 : 26 / 46;
    return video ? 28 / 46 : 1;
  }
  if (model === "doubao-seedance-2-0-fast-260128") return video ? 22 / 37 : 1;
  if (model === "doubao-seedance-2-0-mini-260615") return video ? 14 / 23 : 1;
  return 1;
}

// Channel model mapping may send a declared Seedream model to an Ark endpoint
// ID; the declared name the client sent still describes that endpoint.
function imageModelCandidates(ctx) {
  const req = (ctx && ctx.requestBody) || {};
  return [ctx && ctx.upstreamModel, ctx && ctx.model, req.model].map(trimmed).filter(Boolean);
}

function imageProfile(ctx) {
  for (const model of imageModelCandidates(ctx)) {
    if (Object.prototype.hasOwnProperty.call(IMAGE_MODELS, model)) return IMAGE_MODELS[model];
  }
  return DEFAULT_IMAGE_PROFILE;
}

// Image work is identified by a declared model, or by the decode action for
// endpoint IDs that reach the image route without any declared name.
function imageTask(ctx) {
  return imageModelCandidates(ctx).some((model) => Object.prototype.hasOwnProperty.call(IMAGE_MODELS, model)) || IMAGE_ACTIONS.includes(ctx && ctx.action);
}

function imageSizePixels(value) {
  const match = /^(\d+)\s*[xX×*]\s*(\d+)$/.exec(trimmed(value));
  if (!match) return null;
  const width = Number(match[1]);
  const height = Number(match[2]);
  if (!Number.isSafeInteger(width) || !Number.isSafeInteger(height) || width <= 0 || height <= 0) return null;
  return { width: width, height: height, pixels: width * height };
}

// Validates the request `size` against the documented per-model presets and
// pixel ranges, normalizes accepted spellings (`2k` -> `2K`, `2048*2048` ->
// `2048x2048`) and returns the pixel area used for the submit-time tier
// estimate. `pixels` is null when the output size is unknown (`auto`), which
// the estimate treats as the higher tier; completion overlays the real sizes.
function imageSize(size, profile, layered) {
  // Documented defaults: 2K / 2048x2048 for image generation, `auto` for layer decomposition.
  if (size === undefined) return { size: undefined, pixels: layered ? null : IMAGE_PRESET_PIXELS["2K"] };
  if (typeof size !== "string") throw new Error("size must be a string");
  const preset = trimmed(size).toUpperCase();
  if (preset === "AUTO") {
    if (!layered) throw new Error("size auto is only supported with layer decomposition");
    return { size: "auto", pixels: null };
  }
  if (IMAGE_RESOLUTIONS.includes(preset)) {
    const allowed = layered ? LAYER_SIZE_PRESETS : profile.presets;
    if (!allowed.includes(preset)) throw new Error("size must be one of " + allowed.join(", ") + (layered ? ", auto" : " or width x height pixels"));
    return { size: preset, pixels: IMAGE_PRESET_PIXELS[preset] };
  }
  if (layered) throw new Error("layer decomposition sizes must be one of " + LAYER_SIZE_PRESETS.join(", ") + ", auto");
  const dims = imageSizePixels(size);
  if (!dims) throw new Error("size must be a resolution preset or width x height pixels");
  const ratio = dims.width / dims.height;
  if (dims.pixels < profile.minPixels || dims.pixels > profile.maxPixels || ratio < 1 / 16 || ratio > 16)
    throw new Error("size is outside the model's pixel and aspect-ratio limits");
  return { size: dims.width + "x" + dims.height, pixels: dims.pixels };
}

// Builds the upstream /api/v3/images/generations body. Unknown fields pass
// through so the native route mirrors the official API; documented fields are
// type-checked and model capabilities are enforced here. Live probes (2026-09)
// confirmed Ark rejects wrong-typed seed/guidance_scale, output_format on 4.0,
// tools on pro, sequential_image_generation on pro, and non-web_search tools,
// and accepts `optimize_prompt_options: null` as unset.
function convertImage(ctx) {
  const req = ctx.requestBody || {};
  const model = trimmed(ctx.upstreamModel || req.model);
  if (!model) throw new Error("model is required");
  const profile = imageProfile(ctx);
  const body = {};
  for (const key of Object.keys(req)) {
    if (key === "model" || key === "stream" || req[key] === undefined) continue;
    body[key] = req[key];
  }
  body.model = model;
  for (const key of ["watermark", "optimize_prompt", "layer_decomposition"]) {
    if (body[key] !== undefined && typeof body[key] !== "boolean") throw new Error(key + " must be a boolean");
  }
  const layered = body.layer_decomposition === true;
  if (layered && !profile.layers) throw new Error("layer_decomposition is not supported by this model");
  if (body.prompt !== undefined && typeof body.prompt !== "string") throw new Error("prompt must be a string");
  if (!layered && !trimmed(body.prompt)) throw new Error("prompt is required");
  const images = body.image === undefined ? [] : Array.isArray(body.image) ? body.image : [body.image];
  for (const image of images) {
    const value = typeof image === "string" ? trimmed(image) : "";
    if (!/^https?:\/\//i.test(value) && !IMAGE_DATA_URI.test(value))
      throw new Error("image must be an HTTP URL or a data:image/<format>;base64 URL with a lowercase format");
  }
  if (layered && images.length !== 1) throw new Error("layer decomposition requires exactly one input image");
  if (images.length > profile.maxReferenceImages) throw new Error("at most " + profile.maxReferenceImages + " reference images are supported");
  // The host caps the upstream JSON response and the persisted task data at
  // 1 MiB and serves images through artifact URLs, so it cannot reliably accept
  // or persist Base64 image payloads; this route is URL-only.
  if (body.response_format !== undefined && body.response_format !== "url") throw new Error("response_format must be url");
  if (body.seed !== undefined && !Number.isInteger(body.seed)) throw new Error("seed must be an integer");
  if (body.guidance_scale !== undefined && (typeof body.guidance_scale !== "number" || !Number.isFinite(body.guidance_scale)))
    throw new Error("guidance_scale must be a number");
  if (body.output_format !== undefined) {
    if (body.output_format !== "png" && body.output_format !== "jpeg") throw new Error("output_format must be png or jpeg");
    if (!profile.outputFormat) throw new Error("output_format is not supported by this model");
  }
  // The API reference lists `background` for 5.0 pro only, but a live probe
  // showed lite accepting and ignoring it, so other models forward the value
  // and only pro's documented combination rules are enforced locally.
  if (body.background !== undefined) {
    if (body.background !== "opaque" && body.background !== "transparent") throw new Error("background must be opaque or transparent");
    if (profile.background && body.background === "transparent" && images.length !== 1)
      throw new Error("background transparent requires exactly one input image");
    if (profile.background && body.background === "transparent" && body.output_format === "jpeg")
      throw new Error("background transparent cannot be combined with output_format jpeg");
  }
  if (body.tools !== undefined) {
    if (!Array.isArray(body.tools) || body.tools.some((tool) => !tool || typeof tool !== "object" || Array.isArray(tool) || !trimmed(tool.type)))
      throw new Error("tools must be an array of objects with a string type");
    if (!profile.tools) throw new Error("tools are not supported by this model");
  }
  // JSON null is forwarded as-is: Ark treats it as an unset object (confirmed live).
  const promptOptions = objectValue(body.optimize_prompt_options, "optimize_prompt_options");
  if (promptOptions.mode !== undefined) {
    if (promptOptions.mode !== "standard" && promptOptions.mode !== "fast") throw new Error("optimize_prompt_options.mode must be standard or fast");
    if (promptOptions.mode === "fast" && !profile.fastPromptMode) throw new Error("optimize_prompt_options.mode fast is not supported by this model");
  }
  const sequential = body.sequential_image_generation;
  if (sequential !== undefined && sequential !== "auto" && sequential !== "disabled") throw new Error("sequential_image_generation must be auto or disabled");
  if (sequential !== undefined && !profile.sequential) throw new Error("sequential_image_generation is not supported by this model");
  const options = objectValue(body.sequential_image_generation_options, "sequential_image_generation_options");
  let maxImages = 15;
  if (options.max_images !== undefined) {
    if (!Number.isInteger(options.max_images) || options.max_images < 1 || options.max_images > 15)
      throw new Error("max_images must be an integer between 1 and 15");
    maxImages = options.max_images;
  }
  const size = imageSize(body.size, profile, layered);
  if (size.size !== undefined) body.size = size.size;
  // Submit-time estimate; extractUsageOnComplete overlays the per-image tiers
  // from data[].size and usage.input_images. Unknown output sizes (`auto`)
  // reserve the higher tier.
  let imageCount = 1;
  if (layered) imageCount = MAX_IMAGE_OUTPUTS;
  else if (sequential === "auto") imageCount = Math.max(1, Math.min(maxImages, 15 - images.length)); // reference + generated images ≤ 15
  const higherTier = size.pixels === null || size.pixels > IMAGE_TIER_MAX_PIXELS;
  return {
    body: body,
    action: images.length ? "image_to_image" : "text_to_image",
    facts: {
      images_up_to_1_5k: higherTier ? 0 : imageCount,
      images_above_1_5k: higherTier ? imageCount : 0,
      input_images: images.length,
      layer_decomposition: layered,
    },
  };
}

// `data` is an array in the documented response; tolerate an object-shaped
// payload from compatible upstreams by treating it as one entry.
function imageEntries(body) {
  const data = body && body.data;
  if (Array.isArray(data)) return data.filter((item) => item && typeof item === "object" && !Array.isArray(item));
  if (data && typeof data === "object") return [data];
  return [];
}

function imageURLEntries(body) {
  return imageEntries(body).filter((item) => trimmed(item.url));
}

// Delivered image payloads of a synchronous result. Payloads are counted,
// never entries: failed group members carry only `error`. A
// usage.generated_images count that is fractional, negative, oversized or
// inconsistent with the delivered payloads means the response is not
// understood; the host then keeps the reservation instead of settling on a guess.
function imagePayloads(body) {
  const payloads = imageEntries(body).filter((item) => trimmed(item.url) || trimmed(item.b64_json));
  if (payloads.length > MAX_IMAGE_OUTPUTS) throw new Error("too many output images");
  const count = (body.usage || {}).generated_images;
  if (count !== undefined && (!Number.isInteger(count) || count < 0 || count > MAX_IMAGE_OUTPUTS || count !== payloads.length))
    throw new Error("invalid upstream generated_images");
  return payloads;
}

// Completion facts. Each successful image is counted at its own pixel tier from
// data[].size (official pricing bills layers individually). Payloads without a
// parseable size keep the submit-time tier estimate; usage.input_images
// replaces the estimated reference count only when it is a bounded integer.
function imageUsage(body) {
  const usage = body.usage || {};
  const payloads = imagePayloads(body);
  const facts = {};
  let lower = 0,
    higher = 0,
    sized = true;
  for (const item of payloads) {
    const dims = imageSizePixels(item.size);
    if (!dims) {
      sized = false;
      break;
    }
    if (dims.pixels > IMAGE_TIER_MAX_PIXELS) higher += 1;
    else lower += 1;
  }
  if (sized) {
    facts.images_up_to_1_5k = lower;
    facts.images_above_1_5k = higher;
  }
  if (Number.isInteger(usage.input_images) && usage.input_images >= 0 && usage.input_images <= MAX_REFERENCE_IMAGES) facts.input_images = usage.input_images;
  return facts;
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

function responsesOutputText(ctx, task) {
  const data = artifactData(task);
  if (!imageTask(ctx) && !Array.isArray(data.data)) return responsesVideoText(ctx);
  const parts = [];
  imageURLEntries(data).forEach(function (_, index) {
    const artifact = (ctx.artifacts || {})["image-" + (index + 1)];
    if (!artifact || !trimmed(artifact.url)) throw new Error("image artifact is unavailable");
    parts.push("![Image " + (index + 1) + "](<" + artifact.url + ">)");
  });
  if (!parts.length) throw new Error("image artifact is unavailable");
  return parts.join("\n\n");
}

export const native = {
  createTask: function (ctx) {
    if (!ctx.body || ctx.body.kind !== "json") throw new Error("JSON body required");
    const body = ctx.body.value;
    if (!body || typeof body !== "object" || Array.isArray(body)) throw new Error("request body must be an object");
    const model = trimmed(body.model);
    if (!model) throw new Error("model is required");
    if (body.content !== undefined && !Array.isArray(body.content)) throw new Error("content must be an array");
    const content = Array.isArray(body.content) ? body.content : [];
    const texts = [];
    let hasReference = false;
    for (const item of content) {
      if (!item || typeof item !== "object" || Array.isArray(item)) continue;
      if (item.type === "text" && typeof item.text === "string") texts.push(item.text);
      else hasReference = true;
    }
    if (!texts.length && !hasReference) throw new Error("content is required");
    const requestBody = {
      model: model,
      prompt: texts
        .filter(function (text) {
          return trimmed(text);
        })
        .join("\n"),
      metadata: body,
    };
    const seconds = Number(body.duration);
    if (Number.isFinite(seconds) && seconds > 0) requestBody.seconds = seconds;
    const intent = { kind: "submit", model: model, action: hasReference ? "image_to_video" : "text_to_video", requestBody: requestBody };
    const originTaskIds = draftTaskIds(content);
    if (originTaskIds.length) intent.originTaskIds = originTaskIds;
    return intent;
  },
  taskCreated: function (ctx, task) {
    const data = task.data && typeof task.data === "object" && !Array.isArray(task.data) ? task.data : {};
    return Object.assign({}, data, { id: task.task_id });
  },
  taskStatus: function (ctx, task) {
    if (task.data && typeof task.data === "object" && !Array.isArray(task.data)) return Object.assign({}, task.data, { id: task.task_id });
    const statusMap = { NOT_START: "queued", SUBMITTED: "queued", QUEUED: "queued", IN_PROGRESS: "running", SUCCESS: "succeeded", FAILURE: "failed" };
    const output = { id: task.task_id, status: statusMap[task.status] || "queued" };
    if (task.fail_reason) output.error = { message: task.fail_reason };
    return output;
  },
  createImage: function (ctx) {
    if (!ctx.body || ctx.body.kind !== "json") throw new Error("JSON body required");
    const body = ctx.body.value;
    if (!body || typeof body !== "object" || Array.isArray(body)) throw new Error("request body must be an object");
    const model = trimmed(body.model);
    if (!model) throw new Error("model is required");
    // The host presents one JSON response after the upstream call completes; upstream SSE is not forwarded.
    if (body.stream !== undefined && body.stream !== false)
      throw new Error("stream is not supported on this route; the complete JSON response is returned once all images are generated");
    const requestBody = Object.assign({}, body, { model: model });
    const converted = convertImage({ model: model, requestBody: requestBody });
    return { kind: "submit", model: model, action: converted.action, requestBody: requestBody };
  },
  imageCreated: function (ctx, task) {
    return task.data && typeof task.data === "object" && !Array.isArray(task.data) ? task.data : {};
  },
  error: function (ctx, error) {
    return { error: { code: error.code, message: error.message } };
  },
};

export function buildSubmitRequest(ctx) {
  if (imageTask(ctx)) {
    const converted = convertImage(ctx);
    return {
      url: apiRoot(ctx) + "/api/v3/images/generations",
      method: "POST",
      headers: { "Content-Type": "application/json", Accept: "application/json", Authorization: "Bearer " + ctx.apiKey },
      body: converted.body,
      action: converted.action,
      rewriteModel: converted.body.model,
    };
  }
  const req = ctx.requestBody;
  const metadata = req.metadata || {};
  // Reject output tiers the model does not offer before any quota is reserved.
  videoResolution(ctx);
  const body = Object.assign({ model: req.model || "", content: [] }, metadata);
  const imageContent = [];
  const images = Array.isArray(req.images) ? req.images : [];
  for (const url of images) imageContent.push({ type: "image_url", image_url: { url: url } });
  const metadataContent = Array.isArray(body.content) ? body.content : [];
  body.content = imageContent.concat(metadataContent).filter((item) => item && item.type !== "text");
  const hasReference = body.content.length > 0;
  if (trimmed(req.prompt) || !hasReference) body.content.push({ type: "text", text: req.prompt || "" });
  if (Array.isArray(body.content)) body.content = rewriteDraftTaskContent(body.content, ctx.originTasks);
  const seconds = Number.parseInt(req.seconds || "", 10);
  if (seconds > 0) body.duration = seconds;
  body.model = ctx.upstreamModel || body.model;
  return {
    url: apiRoot(ctx) + "/api/v3/contents/generations/tasks",
    method: "POST",
    headers: { "Content-Type": "application/json", Accept: "application/json", Authorization: "Bearer " + ctx.apiKey },
    body: body,
    action: hasReference ? "image_to_video" : "text_to_video",
    rewriteModel: body.model,
  };
}

export function parseSubmitResponse(ctx, resp) {
  if (imageTask(ctx)) {
    const body = resp.body;
    if (!body || typeof body !== "object" || Array.isArray(body)) throw new Error("invalid image generation response");
    if (body.error && typeof body.error === "object")
      throw new Error((trimmed(body.error.code) || "upstream_error") + ": " + (trimmed(body.error.message) || "image generation failed"));
    const urls = imageURLEntries(body);
    if (!urls.length) throw new Error("image generation returned no images");
    // The synchronous API has no vendor task; keep the host-issued public id.
    const taskId = trimmed(ctx.publicTaskId);
    if (!taskId) throw new Error("missing gateway task id");
    return { taskId: taskId, taskData: body, immediate: { status: "SUCCESS", progress: "100%", url: trimmed(urls[0].url) } };
  }
  if (!resp.body || !resp.body.id) throw new Error("task_id is empty");
  return { taskId: resp.body.id, taskData: resp.body };
}

export function extractUsage(ctx) {
  if (imageTask(ctx)) {
    const facts = convertImage(ctx).facts;
    // Legacy per-call pricing multiplies the price by every ratio returned
    // here, so the reservation ratio is the total requested output count
    // alone; task expressions read the tiered facts and the reference count.
    if (ctx.usagePurpose === "billing_ratios") return { image_count: facts.images_up_to_1_5k + facts.images_above_1_5k };
    return facts;
  }
  const req = ctx.requestBody || {};
  const metadata = req.metadata || {};
  if (ctx.usagePurpose === "billing_ratios") {
    const ratio = videoInputRatio(ctx.upstreamModel || ctx.model, metadata.resolution, metadata.content);
    return ratio === 1 ? null : { video_input_ratio: ratio };
  }
  let seconds = Number(req.seconds || req.duration || metadata.duration || 0);
  if (!Number.isFinite(seconds) || seconds <= 0) {
    const frames = Number(metadata.frames);
    seconds = Number.isFinite(frames) && frames > 0 ? Math.floor(frames / 24) : 15;
  }
  if (seconds <= 0) seconds = 5;
  seconds = Math.min(seconds, 3600);
  const profile = videoProfile(ctx);
  const resolution = videoResolution(ctx);
  const facts = { tokens: estimateTokens(seconds, resolution), resolution: resolution };
  if (profile.videoInput) facts.video_input = hasVideo(metadata.content) ? "video" : "none";
  if (profile.audio) facts.generate_audio = metadata.generate_audio !== false;
  return facts;
}

export function extractUsageOnSubmit(ctx, body) {
  // Only legacy per-call pricing calls this hook, and the host multiplies the
  // price by every ratio it returns, so a synchronous image result settles on
  // the delivered output count alone. Task expressions settle the tiered
  // facts through extractUsageOnComplete instead.
  if (!imageTask(ctx)) return null;
  const count = imagePayloads(body || {}).length;
  return count ? { image_count: count } : {};
}

export function buildQueryRequest(ctx) {
  return {
    url: apiRoot(ctx) + "/api/v3/contents/generations/tasks/" + ctx.taskId,
    method: "GET",
    headers: { Accept: "application/json", "Content-Type": "application/json", Authorization: "Bearer " + ctx.apiKey },
  };
}

export function parseTaskResult(ctx, body) {
  if (body.status === "pending" || body.status === "queued") return { status: "QUEUED", progress: "10%" };
  if (body.status === "processing" || body.status === "running") return { status: "IN_PROGRESS", progress: "50%" };
  if (body.status === "succeeded") {
    const result = { status: "SUCCESS", progress: "100%", url: body.content && body.content.video_url ? body.content.video_url : "" };
    const usage = body.usage || {};
    const completionTokens = Number(usage.completion_tokens || 0);
    const totalTokens = Number(usage.total_tokens || 0);
    if (Number.isFinite(completionTokens) && completionTokens > 0) result.completionTokens = completionTokens;
    if (Number.isFinite(totalTokens) && totalTokens > 0) result.totalTokens = totalTokens;
    return result;
  }
  if (body.status === "failed" || body.status === "expired" || body.status === "cancelled") {
    const reason = body.error && body.error.message ? body.error.message : body.status;
    return { status: "FAILURE", progress: "100%", reason: reason };
  }
  return { status: "UNKNOWN", reason: "unrecognized status: " + String(body.status || "") };
}

function artifactData(ctx) {
  const data = (ctx && ctx.data) || {};
  if (data.data && typeof data.data === "object" && data.data.task_id && Object.prototype.hasOwnProperty.call(data.data, "data")) return data.data.data || {};
  return data;
}

export function listArtifacts(task) {
  if (task.status !== "SUCCESS") return [];
  const data = artifactData(task);
  if (imageTask(task) || Array.isArray(data.data)) {
    return imageURLEntries(data).map(function (item, index) {
      const artifact = { key: "image-" + (index + 1), type: "image" };
      if (item.output_format === "png") artifact.mimeType = "image/png";
      else if (item.output_format === "jpeg") artifact.mimeType = "image/jpeg";
      return artifact;
    });
  }
  const content = data.content || {};
  const artifacts = [];
  if (trimmed(content.video_url)) artifacts.push({ key: "video", type: "video" });
  if (trimmed(content.last_frame_url)) artifacts.push({ key: "last_frame", type: "image", mimeType: "image/png" });
  return artifacts;
}

export function buildContentRequest(ctx) {
  const data = artifactData(ctx);
  let url = "";
  if (String(ctx.artifactKey || "").startsWith("image-")) {
    const entries = imageURLEntries(data);
    const index = entries.findIndex(function (_, position) {
      return ctx.artifactKey === "image-" + (position + 1);
    });
    url = index >= 0 ? trimmed(entries[index].url) : "";
  } else {
    const content = data.content || {};
    const urls = { video: content.video_url, last_frame: content.last_frame_url };
    url = trimmed(urls[ctx.artifactKey]);
  }
  if (!url) throw new Error("artifact_not_found");
  return { url: url, method: ctx.clientRequest.method, credentialless: true };
}

export function extractUsageOnComplete(task, taskResult, body) {
  if (imageTask(task)) return imageUsage(body || {});
  if (!body || body.status !== "succeeded") return {};
  const facts = {};
  const usage = body.usage || {};
  let tokens = Number(usage.completion_tokens);
  if (!Number.isFinite(tokens) || tokens <= 0) tokens = Number(usage.total_tokens);
  if (Number.isFinite(tokens) && tokens > 0) facts.tokens = tokens;
  const content = body.content || {};
  const resolution = trimmed(content.resolution || body.resolution).toLowerCase();
  if (videoProfile(task).resolutions.includes(resolution)) facts.resolution = resolution;
  return facts;
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
      const input = responsesInput(req);
      if (imageTask({ upstreamModel: ctx.upstreamModel, model: model })) {
        const prompt = input.prompt || trimmed(req.prompt);
        const listed = Array.isArray(req.image) ? req.image : req.image === undefined ? [] : [req.image];
        const images = [];
        for (const image of listed.concat(req.images || [], input.images)) {
          if (trimmed(image) && !images.includes(trimmed(image))) images.push(trimmed(image));
        }
        if (!prompt && !images.length) throw new Error("input is required");
        const requestBody = { model: model };
        if (prompt) requestBody.prompt = prompt;
        if (images.length) requestBody.image = images;
        for (const key of IMAGE_REQUEST_KEYS) {
          if (Object.prototype.hasOwnProperty.call(req, key)) requestBody[key] = req[key];
        }
        // Responses `background` (boolean background execution) is host-owned and never reaches Ark;
        // Ark's string `background` transparency option is available on the native route only.
        // Only Ark's documented web_search tool is forwarded; function and other Responses tools are dropped.
        if (Array.isArray(req.tools)) {
          const tools = req.tools.filter((tool) => tool && typeof tool === "object" && !Array.isArray(tool) && tool.type === "web_search");
          if (tools.length) requestBody.tools = tools.map(() => ({ type: "web_search" }));
        }
        // The model may be a mapped alias; final validation runs after channel selection.
        const converted = convertImage({ upstreamModel: ctx.upstreamModel, model: model, requestBody: requestBody });
        return { kind: "submit", model: model, action: converted.action, requestBody: requestBody };
      }
      if (req.metadata !== undefined && (!req.metadata || typeof req.metadata !== "object" || Array.isArray(req.metadata)))
        throw new Error("metadata must be an object");
      const prompt = input.prompt || trimmed(req.prompt);
      const images = [];
      for (const image of [req.image, req.input_reference].concat(req.images || [], input.images)) {
        if (trimmed(image) && !images.includes(trimmed(image))) images.push(trimmed(image));
      }
      if (!prompt && images.length === 0) throw new Error("input is required");
      const metadata = Object.assign({}, req.metadata || {});
      if (Object.prototype.hasOwnProperty.call(req, "resolution")) metadata.resolution = req.resolution;
      else if (req.size && !metadata.resolution) metadata.resolution = normalizeResolution(req.size);
      const requestBody = { model: model, prompt: prompt, metadata: metadata };
      if (images.length) requestBody.images = images;
      if (Object.prototype.hasOwnProperty.call(req, "seconds")) requestBody.seconds = req.seconds;
      else if (Object.prototype.hasOwnProperty.call(req, "duration")) requestBody.seconds = req.duration;
      if (Object.prototype.hasOwnProperty.call(req, "size")) requestBody.size = req.size;
      const intent = { kind: "submit", model: model, action: images.length ? "image_to_video" : "text_to_video", requestBody: requestBody };
      const originTaskIds = draftTaskIds(metadata.content);
      if (originTaskIds.length) intent.originTaskIds = originTaskIds;
      return intent;
    },
    renderEvents: function (ctx, task, previousState) {
      const status = String(task.status || "UNKNOWN").toUpperCase();
      const value = Number(String(task.progress || "").replace("%", ""));
      const progress = Number.isFinite(value) && value >= 0 && value <= 100 ? value : null;
      const state = { status: status, progress: progress };
      if (status === "SUCCESS") {
        const text = responsesOutputText(ctx, task);
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
      return {
        output: [
          {
            type: "message",
            status: "completed",
            role: "assistant",
            content: [{ type: "output_text", text: responsesOutputText(ctx, task), annotations: [], logprobs: [] }],
          },
        ],
        metadata: { vendor: "doubao" },
      };
    },
  },
};

const legacyRenderers = {
  openai_video: function (task) {
    const data = task.data || {};
    const statusMap = { NOT_START: "queued", SUBMITTED: "queued", QUEUED: "queued", IN_PROGRESS: "in_progress", SUCCESS: "completed", FAILURE: "failed" };
    const output = {
      id: task.task_id,
      object: "video",
      model: task.properties ? task.properties.origin_model_name || "" : "",
      status: statusMap[task.status] || "unknown",
      progress: Number(String(task.progress || "0").replace("%", "")),
      created_at: task.created_at,
      completed_at: task.updated_at,
    };
    if (data.status === "failed") output.error = { message: data.error ? data.error.message || "" : "", code: data.error ? data.error.code || "" : "" };
    return output;
  },
};

protocols.openai_video = {
  decodeRequest: function (ctx) {
    if (!ctx.body || (ctx.body.kind !== "json" && ctx.body.kind !== "multipart")) throw new Error("JSON or multipart body required");
    if (ctx.body.kind === "json") {
      if (!ctx.body.value || Array.isArray(ctx.body.value)) throw new Error("JSON object required");
      const req = ctx.body.value;
      const seconds = req.seconds === undefined ? req.duration : req.seconds;
      if (seconds !== undefined && (!Number.isFinite(Number(seconds)) || Number(seconds) <= 0 || Number(seconds) > 3600))
        throw new Error("seconds must be between 1 and 3600");
      return {
        kind: "submit",
        model: ctx.model,
        action: req.input_reference || req.image ? "image_to_video" : "text_to_video",
        requestBody: Object.assign({}, req, { model: ctx.model }),
      };
    }
    const first = function (name) {
      const values = (ctx.body.fields || {})[name] || [];
      if (values.length > 1) throw new Error(name + " must be provided once");
      return values[0];
    };
    const req = {};
    const fields = ctx.body.fields || {};
    for (const name of Object.keys(fields)) {
      req[name] = first(name);
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
    if ((ctx.body.files || []).length) throw new Error("Doubao requires image and video references to be URLs inside metadata.content");
    if (req.seconds !== undefined) req.seconds = Number(req.seconds);
    else if (req.duration !== undefined) req.seconds = Number(req.duration);
    const seconds = req.seconds === undefined ? req.duration : req.seconds;
    if (seconds !== undefined && (!Number.isFinite(Number(seconds)) || Number(seconds) <= 0 || Number(seconds) > 3600))
      throw new Error("seconds must be between 1 and 3600");
    return {
      kind: "submit",
      model: ctx.model,
      action: req.input_reference || req.image ? "image_to_video" : "text_to_video",
      requestBody: Object.assign({}, req, { model: ctx.model }),
    };
  },
  render: function (ctx, task) {
    return legacyRenderers.openai_video(task);
  },
};

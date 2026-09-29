// Protocol capabilities, not prices. Keep defaults and validation shared by
// native requests, compatibility protocols, and submission billing facts.
const WAN_MODELS = {
  "wan3.0-video": { kind: "all", resolutions: ["480P", "720P", "1080P"], defaultResolution: "1080P", maxDuration: 30 },
  "wan3.0-video-prime": { kind: "all", resolutions: ["480P", "720P", "1080P"], defaultResolution: "1080P", maxDuration: 30 },
  "wan2.7-t2v": { kind: "t2v", resolutions: ["720P", "1080P"], defaultResolution: "1080P", maxDuration: 15 },
  "wan2.7-i2v": { kind: "media", resolutions: ["720P", "1080P"], defaultResolution: "1080P", maxDuration: 15 },
  "wan2.6-t2v": { kind: "size", resolutions: ["720P", "1080P"], defaultResolution: "1080P", maxDuration: 15 },
  "wan2.6-t2v-us": { kind: "size", resolutions: ["720P", "1080P"], defaultResolution: "1080P", durations: [5, 10, 15] },
  "wan2.6-i2v": { kind: "image", resolutions: ["720P", "1080P"], defaultResolution: "1080P", maxDuration: 15 },
  "wan2.6-i2v-flash": { kind: "image", resolutions: ["720P", "1080P"], defaultResolution: "1080P", maxDuration: 15 },
  "wan2.6-i2v-us": { kind: "image", resolutions: ["720P", "1080P"], defaultResolution: "1080P", durations: [5, 10, 15] },
  "wan2.5-t2v-preview": { kind: "size", resolutions: ["480P", "720P", "1080P"], defaultResolution: "1080P", durations: [5, 10] },
  "wan2.5-i2v-preview": { kind: "image", resolutions: ["480P", "720P", "1080P"], defaultResolution: "1080P", durations: [5, 10] },
  "wan2.2-t2v-plus": { kind: "size", resolutions: ["480P", "1080P"], defaultResolution: "1080P", durations: [5] },
  "wan2.2-i2v-flash": { kind: "image", resolutions: ["480P", "720P", "1080P"], defaultResolution: "720P", durations: [5] },
  "wan2.2-i2v-plus": { kind: "image", resolutions: ["480P", "1080P"], defaultResolution: "1080P", durations: [5] },
  "wan2.2-kf2v-flash": { kind: "frames", resolutions: ["480P", "720P", "1080P"], defaultResolution: "720P", durations: [5] },
  "wan2.2-s2v": { kind: "speech", resolutions: ["480P", "720P"], defaultResolution: "480P" },
  "wanx2.1-t2v-plus": { kind: "size", resolutions: ["720P"], defaultResolution: "720P", durations: [5] },
  "wanx2.1-t2v-turbo": { kind: "size", resolutions: ["480P", "720P"], defaultResolution: "720P", durations: [5] },
  "wanx2.1-i2v-plus": { kind: "image", resolutions: ["720P"], defaultResolution: "720P", durations: [5] },
  "wanx2.1-i2v-turbo": { kind: "image", resolutions: ["480P", "720P"], defaultResolution: "720P", durations: [3, 4, 5] },
};

// HTTP contracts (SDK synchronous calls can also wrap polling):
// https://help.aliyun.com/zh/model-studio/text-to-image-v2-api-reference
// https://help.aliyun.com/zh/model-studio/wan-image-generation-api-reference
// https://help.aliyun.com/zh/model-studio/wan-image-generation-and-editing-api-reference
// https://help.aliyun.com/zh/model-studio/wan2-5-image-edit-api-reference
// https://help.aliyun.com/zh/model-studio/wanx-image-edit-api-reference
// https://help.aliyun.com/zh/model-studio/qwen-image-api
// https://help.aliyun.com/zh/model-studio/qwen-image-edit-api
// https://help.aliyun.com/zh/model-studio/qwen-image-generation-and-editing-api-reference
// https://help.aliyun.com/zh/model-studio/z-image-api-reference
// Kinds: image27/image26/t2i use multimodal-generation (sync) or
// image-generation/generation (async) with output.choices; legacy uses the
// async text2image/image-synthesis with output.results; legacy_edit uses the
// async image2image/image-synthesis with input.images and output.results;
// legacy_imageedit (wanx2.1-imageedit) uses the same async
// image2image/image-synthesis with input.function, input.base_image_url and an
// optional input.mask_image_url; qwen (Qwen-Image, Qwen-Image-Edit, Z-Image)
// uses multimodal-generation synchronously only, with output.choices.
const IMAGE_MODELS = {
  "wan2.7-image-pro": "image27",
  "wan2.7-image": "image27",
  "wan2.6-image": "image26",
  "wan2.6-t2i": "t2i",
  "wan2.5-t2i-preview": "legacy",
  "wan2.2-t2i-flash": "legacy",
  "wan2.2-t2i-plus": "legacy",
  "wanx2.1-t2i-turbo": "legacy",
  "wanx2.1-t2i-plus": "legacy",
  "wanx2.0-t2i-turbo": "legacy",
  "wan2.5-i2i-preview": "legacy_edit",
  "wanx2.1-imageedit": "legacy_imageedit",
  "qwen-image": "qwen",
  "qwen-image-plus": "qwen",
  "qwen-image-max": "qwen",
  "qwen-image-2.0": "qwen",
  "qwen-image-2.0-pro": "qwen",
  "qwen-image-3.0": "qwen",
  "qwen-image-3.0-pro": "qwen",
  "qwen-image-edit": "qwen",
  "qwen-image-edit-plus": "qwen",
  "qwen-image-edit-max": "qwen",
  "z-image-turbo": "qwen",
};
// wanx2.1-imageedit selects its editing operation with input.function. The
// parameters below belong to specific functions and are forwarded as
// documented; none of them changes the billed image count.
const IMAGE_EDIT_FUNCTIONS = [
  "stylization_all",
  "stylization_local",
  "description_edit",
  "description_edit_with_mask",
  "remove_watermark",
  "expand",
  "super_resolution",
  "colorization",
  "doodle",
  "control_cartoon_feature",
];
const IMAGE_EDIT_FUNCTION_PARAMETERS = ["strength", "top_scale", "bottom_scale", "left_scale", "right_scale", "upscale_factor", "is_sketch"];
// Documented limits of the multimodal-generation image models: the output
// count range and the number of input images (minInput 1 marks edit-only
// models). Fixed-count models reject n > 1 upstream ("n must be 1").
const QWEN_IMAGE_LIMITS = {
  "qwen-image": { maxN: 1, maxInput: 0 },
  "qwen-image-plus": { maxN: 1, maxInput: 0 },
  "qwen-image-max": { maxN: 1, maxInput: 0 },
  "qwen-image-2.0": { maxN: 6, maxInput: 3 },
  "qwen-image-2.0-pro": { maxN: 6, maxInput: 3 },
  "qwen-image-3.0": { maxN: 6, maxInput: 3 },
  "qwen-image-3.0-pro": { maxN: 6, maxInput: 3 },
  "qwen-image-edit": { maxN: 1, maxInput: 3, minInput: 1 },
  "qwen-image-edit-plus": { maxN: 6, maxInput: 3, minInput: 1 },
  "qwen-image-edit-max": { maxN: 6, maxInput: 3, minInput: 1 },
  "z-image-turbo": { maxN: 1, maxInput: 0 },
};
// Dated snapshots resolve to the undated profile through modelKey().
const IMAGE_MODEL_SNAPSHOTS = [
  "qwen-image-plus-2026-01-09",
  "qwen-image-max-2025-12-30",
  "qwen-image-2.0-2026-03-03",
  "qwen-image-2.0-pro-2026-03-03",
  "qwen-image-2.0-pro-2026-04-22",
  "qwen-image-2.0-pro-2026-06-22",
  "qwen-image-edit-plus-2025-10-30",
  "qwen-image-edit-plus-2025-12-15",
  "qwen-image-edit-max-2026-01-16",
];
const QWEN_IMAGE3_MODELS = ["qwen-image-3.0", "qwen-image-3.0-pro"];
const Z_IMAGE_MODELS = ["z-image-turbo"];
// Qwen-Image-3.0 usage: output_image_type / input_image_type switch to the
// 2K tier above this pixel area.
const QWEN_IMAGE3_TIER_MAX_PIXELS = 2250000;
// Largest documented per-request image output (wan2.7 group generation).
const MAX_IMAGE_OUTPUTS = 12;
// Legacy per-call pricing multiplied Z-Image requests with prompt rewriting
// by this ratio; task expressions read the prompt_extend fact instead.
const Z_IMAGE_PROMPT_EXTEND_RATIO = 2;
// Base64 uploads on the OpenAI edits endpoint are bounded by DashScope's 10 MB input image limit.
const MAX_INPUT_IMAGE_BYTES = 10485760;

const IMAGE_UNIT_LABEL = { en: "image", zh: "张", "zh-TW": "張", fr: "image", ja: "枚", ru: "изображение", vi: "ảnh" };
const IMAGE_COUNT_FIELD = {
  type: "number",
  unit: "count",
  unitLabel: IMAGE_UNIT_LABEL,
  description: { en: "Image generation unit price", zh: "图片生成单价" },
};

const WAN_IMAGE_USAGE_SCHEMA = { image_count: IMAGE_COUNT_FIELD };

// Z-Image is priced differently when prompt rewriting is enabled.
const Z_IMAGE_USAGE_SCHEMA = {
  image_count: IMAGE_COUNT_FIELD,
  prompt_extend: {
    type: "boolean",
    description: { en: "Whether prompt rewriting is enabled", zh: "是否开启提示词改写" },
  },
};

// Qwen-Image-3.0 meters output images by tier and input images separately.
// Estimated at submit from the requested size and input count; settled from
// usage.output_image_count, usage.output_image_type and usage.input_image_count.
const QWEN_IMAGE3_USAGE_SCHEMA = {
  image_count: IMAGE_COUNT_FIELD,
  output_image_type: {
    enum: ["qima_output_1k", "qima_output_2k"],
    enumLabels: { qima_output_1k: { en: "1K output", zh: "1K 输出" }, qima_output_2k: { en: "2K output", zh: "2K 输出" } },
    description: { en: "Output image tier", zh: "输出图片档位" },
  },
  input_image_count: {
    type: "number",
    unit: "count",
    unitLabel: IMAGE_UNIT_LABEL,
    description: { en: "Input image unit price", zh: "输入图片单价" },
  },
};

// Wan video usage facts. The resolution options follow the model's
// capability table so pricing lists only the tiers the model offers.
function wanVideoUsageSchema(resolutions, audio) {
  const resolutionLabels = {};
  for (const resolution of resolutions) resolutionLabels[resolution] = { en: resolution, zh: resolution };
  const schema = {
    // Billable seconds: output duration, or input + output for Wan3.
    seconds: {
      type: "number",
      unit: "second",
      description: { en: "Video generation unit price", zh: "视频生成单价" },
    },
    // Requested output video resolution.
    resolution: {
      enum: resolutions,
      enumLabels: resolutionLabels,
      description: { en: "Output video resolution", zh: "输出视频分辨率" },
    },
  };
  // wan2.6-i2v-flash prices audio and silent output differently. parameters.audio
  // defaults to true; the completion usage.audio flag replaces the estimate.
  if (audio) {
    schema.audio = {
      type: "boolean",
      description: { en: "Whether audio is generated", zh: "是否生成音频" },
    };
  }
  return schema;
}

const WAN_AUDIO_VIDEO_MODELS = ["wan2.6-i2v-flash"];
// Dated snapshots share the undated model's capabilities through modelKey.
const WAN_VIDEO_SNAPSHOTS = ["wan2.7-t2v-2026-04-25", "wan2.7-t2v-2026-06-12", "wan2.7-i2v-2026-04-25"];
const WAN_VIDEO_RESOLUTIONS = ["480P", "720P", "1080P"];

// One usage profile per distinct resolution set and audio option.
function wanVideoUsageProfiles() {
  const profiles = [];
  for (const model of Object.keys(WAN_MODELS).concat(WAN_VIDEO_SNAPSHOTS)) {
    const key = modelKey(model);
    const resolutions = WAN_MODELS[key].resolutions;
    const audio = WAN_AUDIO_VIDEO_MODELS.includes(key);
    const shape = resolutions.join("/") + (audio ? "+audio" : "");
    let profile = profiles.find((entry) => entry.shape === shape);
    if (!profile) {
      profile = { shape: shape, models: [], schema: wanVideoUsageSchema(resolutions, audio) };
      profiles.push(profile);
    }
    profile.models.push(model);
  }
  return profiles.map((profile) => ({ models: profile.models, schema: profile.schema }));
}

export const meta = {
  apiVersion: 1,
  key: "alibaba",
  name: "Alibaba Bailian",
  icon: "Bailian.Color",
  description: {
    en: "Alibaba Cloud Bailian image and video generation (Wan, Qwen-Image, Z-Image)",
    zh: "阿里云百炼图片与视频生成（万相、千问图像、Z-Image）",
  },
  version: "1.4.1",
  author: { name: "QuantumNous" },
  channelTypes: [17],
  // Literal metadata also supports the dashboard's static script preview.
  models: [
    "wan3.0-video",
    "wan3.0-video-prime",
    "wan2.7-t2v",
    "wan2.7-t2v-2026-04-25",
    "wan2.7-t2v-2026-06-12",
    "wan2.7-i2v",
    "wan2.7-i2v-2026-04-25",
    "wan2.6-t2v",
    "wan2.6-t2v-us",
    "wan2.6-i2v",
    "wan2.6-i2v-flash",
    "wan2.6-i2v-us",
    "wan2.5-t2v-preview",
    "wan2.5-i2v-preview",
    "wan2.2-t2v-plus",
    "wan2.2-i2v-flash",
    "wan2.2-i2v-plus",
    "wan2.2-kf2v-flash",
    "wan2.2-s2v",
    "wanx2.1-t2v-plus",
    "wanx2.1-t2v-turbo",
    "wanx2.1-i2v-plus",
    "wanx2.1-i2v-turbo",
    "wan2.7-image-pro",
    "wan2.7-image",
    "wan2.6-image",
    "wan2.6-t2i",
    "wan2.5-t2i-preview",
    "wan2.2-t2i-flash",
    "wan2.2-t2i-plus",
    "wanx2.1-t2i-turbo",
    "wanx2.1-t2i-plus",
    "wanx2.0-t2i-turbo",
    "wan2.5-i2i-preview",
    "wanx2.1-imageedit",
    "qwen-image",
    "qwen-image-plus",
    "qwen-image-max",
    "qwen-image-2.0",
    "qwen-image-2.0-pro",
    "qwen-image-3.0",
    "qwen-image-3.0-pro",
    "qwen-image-edit",
    "qwen-image-edit-plus",
    "qwen-image-edit-max",
    "z-image-turbo",
  ].concat(IMAGE_MODEL_SNAPSHOTS),
  fetchMode: "per_task",
  upstreams: ["vendor", "new_api"],
  submitResponseTypes: ["json", "sse"],
  requiredCapabilities: ["json-clone@1", "submit-sse-delta@1"],
  usageSchema: { ...WAN_IMAGE_USAGE_SCHEMA, ...wanVideoUsageSchema(WAN_VIDEO_RESOLUTIONS, false) },
  usageProfiles: [
    {
      models: Object.keys(IMAGE_MODELS)
        .concat(IMAGE_MODEL_SNAPSHOTS)
        .filter((name) => !QWEN_IMAGE3_MODELS.includes(name) && !Z_IMAGE_MODELS.includes(name)),
      schema: WAN_IMAGE_USAGE_SCHEMA,
    },
    { models: Z_IMAGE_MODELS, schema: Z_IMAGE_USAGE_SCHEMA },
    { models: QWEN_IMAGE3_MODELS, schema: QWEN_IMAGE3_USAGE_SCHEMA },
  ].concat(wanVideoUsageProfiles()),
  routes: [
    // Synchronous vendor call with no task to re-query; the response is
    // delivered once and never persisted.
    {
      method: "POST",
      path: "/ali/api/v1/services/aigc/multimodal-generation/generation",
      type: "submit",
      models: ["wan2.7-image-pro", "wan2.7-image", "wan2.6-image", "wan2.6-t2i"].concat(Object.keys(QWEN_IMAGE_LIMITS), IMAGE_MODEL_SNAPSHOTS),
      decode: "createImageTask",
      render: "imageCreated",
      retainResult: false,
    },
    {
      method: "POST",
      path: "/ali/api/v1/services/aigc/image-generation/generation",
      type: "submit",
      models: ["wan2.7-image-pro", "wan2.7-image", "wan2.6-image", "wan2.6-t2i"],
      decode: "createImageTask",
      render: "taskCreated",
    },
    {
      method: "POST",
      path: "/ali/api/v1/services/aigc/text2image/image-synthesis",
      type: "submit",
      models: ["wan2.5-t2i-preview", "wan2.2-t2i-flash", "wan2.2-t2i-plus", "wanx2.1-t2i-turbo", "wanx2.1-t2i-plus", "wanx2.0-t2i-turbo"],
      decode: "createImageTask",
      render: "taskCreated",
    },
    {
      method: "POST",
      path: "/ali/api/v1/services/aigc/image2image/image-synthesis",
      type: "submit",
      models: ["wan2.5-i2i-preview", "wanx2.1-imageedit"],
      decode: "createImageTask",
      render: "taskCreated",
    },
    { method: "POST", path: "/ali/api/v1/services/aigc/video-generation/video-synthesis", type: "submit", decode: "createVideoTask", render: "taskCreated" },
    { method: "POST", path: "/ali/api/v1/services/aigc/image2video/video-synthesis", type: "submit", decode: "createVideoTask", render: "taskCreated" },
    { method: "GET", path: "/ali/api/v1/tasks/:task_id", type: "query", render: "taskStatus" },
  ],
  protocols: [
    { name: "openai_responses", supports: ["stream", "sync", "background"] },
    { name: "openai_video", models: Object.keys(WAN_MODELS).concat(["wan2.7-t2v-2026-04-25", "wan2.7-t2v-2026-06-12", "wan2.7-i2v-2026-04-25"]) },
    { name: "openai_image", models: Object.keys(IMAGE_MODELS).concat(IMAGE_MODEL_SNAPSHOTS) },
  ],
};

function trimmed(value) {
  return String(value || "").trim();
}

function viaNewAPI(ctx) {
  return !!(ctx.upstream && ctx.upstream.kind === "new_api");
}

// Another New API gateway serves the DashScope wire format only on this
// plugin's /ali native routes; DashScope itself serves the unprefixed paths.
function apiRoot(ctx) {
  return ctx.baseUrl + (viaNewAPI(ctx) ? "/ali" : "");
}

function firstImage(req) {
  if (trimmed(req.image)) return trimmed(req.image);
  for (const image of req.images || []) if (trimmed(image)) return trimmed(image);
  return trimmed(req.input_reference);
}

function secondImage(req) {
  let count = 0;
  for (const image of req.images || []) {
    if (!trimmed(image)) continue;
    count++;
    if (count === 2) return trimmed(image);
  }
  return "";
}

const LEGACY_SIZES = {
  "480P": { "16:9": "832*480", "9:16": "480*832", "1:1": "624*624" },
  "720P": { "16:9": "1280*720", "9:16": "720*1280", "1:1": "960*960", "4:3": "1088*832", "3:4": "832*1088" },
  "1080P": { "16:9": "1920*1080", "9:16": "1080*1920", "1:1": "1440*1440", "4:3": "1632*1248", "3:4": "1248*1632" },
};

// Wan2.7 changed the pixel sizes for 4:3 and 3:4. Legacy sizes remain valid
// compatibility inputs, but never replace the target model's native sizes.
const MODERN_SIZES = {
  "720P": { "16:9": "1280*720", "9:16": "720*1280", "1:1": "960*960", "4:3": "1104*832", "3:4": "832*1104" },
  "1080P": { "16:9": "1920*1080", "9:16": "1080*1920", "1:1": "1440*1440", "4:3": "1648*1248", "3:4": "1248*1648" },
};

function modelKey(model) {
  return String(model || "")
    .replace(/-\d{4}-\d{2}-\d{2}$/, "")
    .replace(/^wan2\.1-/, "wanx2.1-");
}

function modelProfile(model) {
  const key = modelKey(model);
  if (!Object.prototype.hasOwnProperty.call(WAN_MODELS, key)) throw new Error("unsupported Wan model: " + model);
  return WAN_MODELS[key];
}

function imageModel(ctx) {
  const key = modelKey(ctx.upstreamModel || ctx.model || (ctx.requestBody || {}).model);
  return Object.prototype.hasOwnProperty.call(IMAGE_MODELS, key) ? IMAGE_MODELS[key] : undefined;
}

function qwenImage3(ctx) {
  return QWEN_IMAGE3_MODELS.includes(modelKey(ctx.upstreamModel || ctx.model || (ctx.requestBody || {}).model));
}

function zImage(ctx) {
  return Z_IMAGE_MODELS.includes(modelKey(ctx.upstreamModel || ctx.model || (ctx.requestBody || {}).model));
}

// Host file placeholders stand in for uploaded edit images; the host inlines
// them as data URLs before the request is sent.
function isFileRef(value) {
  return !!value && typeof value === "object" && !Array.isArray(value) && typeof value.__fileRef === "string";
}

function isImageInput(value) {
  return isFileRef(value) || (typeof value === "string" && /^(https?:\/\/|data:image\/)/i.test(value));
}

function sizePixels(size) {
  const match = /^(\d+)\*(\d+)$/.exec(size);
  if (!match) return null;
  const width = Number(match[1]),
    height = Number(match[2]);
  if (!Number.isSafeInteger(width) || !Number.isSafeInteger(height) || width <= 0 || height <= 0) return null;
  return { width: width, height: height, pixels: width * height };
}

function convertImage(ctx) {
  const req = ctx.requestBody || {};
  const model = ctx.upstreamModel || req.model;
  const profile = imageModel(ctx);
  if (!profile) throw new Error("unsupported image model: " + model);
  const limits = QWEN_IMAGE_LIMITS[modelKey(model)];
  const metadata = objectValue(req.metadata, "metadata");
  if (metadata.model !== undefined && metadata.model !== model) throw new Error("can't change model with metadata");
  const mode = metadata.upstream_mode ?? (profile === "qwen" ? "sync" : "async");
  if (mode !== "sync" && mode !== "async") throw new Error("upstream_mode must be sync or async");
  const legacyProfile = profile === "legacy" || profile === "legacy_edit" || profile === "legacy_imageedit";
  if (legacyProfile && mode === "sync") throw new Error("this image model only supports asynchronous HTTP calls");
  if (profile === "qwen" && mode === "async") throw new Error("this image model only supports synchronous HTTP calls");
  const parameters = {};
  for (const key of [
    "n",
    "size",
    "negative_prompt",
    "prompt_extend",
    "prompt_extend_mode",
    "enable_thinking",
    "watermark",
    "seed",
    "enable_interleave",
    "max_images",
    "enable_sequential",
    "thinking_mode",
    "bbox_list",
    "color_palette",
  ]) {
    if (req[key] !== undefined) parameters[key] = req[key];
  }
  if (profile === "legacy_imageedit") {
    for (const key of IMAGE_EDIT_FUNCTION_PARAMETERS) if (req[key] !== undefined) parameters[key] = req[key];
  }
  Object.assign(parameters, objectValue(metadata.parameters, "metadata.parameters"));
  for (const key of ["prompt_extend", "enable_thinking", "watermark", "enable_interleave", "enable_sequential", "thinking_mode", "stream"]) {
    if (parameters[key] !== undefined && typeof parameters[key] !== "boolean") throw new Error(key + " must be a boolean");
  }
  if (parameters.stream === true && (mode !== "sync" || profile !== "image26" || !parameters.enable_interleave))
    throw new Error("upstream streaming requires synchronous wan2.6-image interleaved output");
  if (parameters.enable_interleave && profile !== "image26") throw new Error("enable_interleave is only supported by wan2.6-image");
  if (parameters.enable_sequential && profile !== "image27") throw new Error("enable_sequential is only supported by wan2.7-image models");
  if (parameters.enable_interleave && mode === "sync") parameters.stream = true;
  const maxN = parameters.enable_interleave ? 1 : parameters.enable_sequential ? 12 : profile === "qwen" ? limits.maxN : 4;
  parameters.n =
    parameters.n ??
    (mode === "sync" || parameters.enable_interleave || profile === "qwen" || profile === "legacy_imageedit"
      ? 1
      : profile === "image27"
        ? parameters.enable_sequential
          ? 12
          : 1
        : 4);
  // Provider limits are stricter than the host's dto.MaxImageN count ceiling.
  // Fixed-count models are rejected here instead of letting DashScope answer
  // "n must be 1" after the quantity was reserved.
  if (!Number.isInteger(parameters.n) || parameters.n < 1 || parameters.n > maxN)
    throw new Error(maxN === 1 ? "n must be 1 for this model" : "n must be an integer between 1 and " + maxN);
  if (parameters.max_images !== undefined && (!Number.isInteger(parameters.max_images) || parameters.max_images < 1 || parameters.max_images > 5))
    throw new Error("max_images must be an integer between 1 and 5");
  if (parameters.enable_interleave) parameters.max_images = parameters.max_images ?? 5;
  if (parameters.seed !== undefined && (!Number.isInteger(parameters.seed) || parameters.seed < 0 || parameters.seed > 2147483647))
    throw new Error("seed must be an integer between 0 and 2147483647");
  if (parameters.prompt_extend_mode !== undefined && parameters.prompt_extend_mode !== "direct" && parameters.prompt_extend_mode !== "agent")
    throw new Error("prompt_extend_mode must be direct or agent");

  const input = Object.assign({}, objectValue(metadata.input, "metadata.input"));
  let listedImages = req.images;
  if (listedImages === undefined) {
    const first = isFileRef(req.image) ? req.image : firstImage(req);
    listedImages = first ? [first] : [];
  }
  if (!Array.isArray(listedImages)) throw new Error("images must be an array");
  let imageCount = 0;
  if (legacyProfile) {
    input.prompt = input.prompt ?? req.prompt;
    if (typeof input.prompt !== "string" || !input.prompt.trim()) throw new Error("prompt is required");
    if (input.messages !== undefined) throw new Error("this model does not accept input.messages");
    if (parameters.negative_prompt !== undefined) {
      if (profile === "legacy_imageedit") throw new Error("this model does not accept negative_prompt");
      input.negative_prompt = parameters.negative_prompt;
      delete parameters.negative_prompt;
    }
    if (profile === "legacy") {
      if (listedImages.length) throw new Error("this model only supports text-to-image input");
    } else if (profile === "legacy_edit") {
      input.images = input.images ?? listedImages;
      if (!Array.isArray(input.images) || input.images.length < 1 || input.images.length > 3 || !input.images.every(isImageInput))
        throw new Error("this model requires 1 to 3 input images as HTTP URLs or Base64 data URLs");
      imageCount = input.images.length;
    } else {
      // wanx2.1-imageedit takes one base image and selects the operation with
      // input.function; an OpenAI edit defaults to instruction editing, or to
      // local repainting when a mask is supplied.
      if (input.images !== undefined) throw new Error("this model takes input.base_image_url, not input.images");
      if (input.base_image_url === undefined && listedImages.length === 1) input.base_image_url = listedImages[0];
      if (listedImages.length > 1 || !isImageInput(input.base_image_url))
        throw new Error("this model requires exactly one input image as an HTTP URL or Base64 data URL");
      if (input.mask_image_url === undefined && req.mask !== undefined) input.mask_image_url = req.mask;
      input.function = input.function ?? req.function ?? (input.mask_image_url !== undefined ? "description_edit_with_mask" : "description_edit");
      if (!IMAGE_EDIT_FUNCTIONS.includes(input.function)) throw new Error("function must be one of " + IMAGE_EDIT_FUNCTIONS.join(", "));
      if (input.function === "description_edit_with_mask" && !isImageInput(input.mask_image_url))
        throw new Error("description_edit_with_mask requires a mask image as an HTTP URL or Base64 data URL");
      if (input.function !== "description_edit_with_mask" && input.mask_image_url !== undefined)
        throw new Error("mask images are only used by description_edit_with_mask");
      if (parameters.size !== undefined) throw new Error("this model does not accept size");
      imageCount = 1;
    }
  } else if (input.messages === undefined) {
    const content = [];
    for (const image of listedImages) content.push({ image: image });
    content.push({ text: req.prompt });
    input.messages = [{ role: "user", content: content }];
  }
  if (!legacyProfile) {
    if (
      !Array.isArray(input.messages) ||
      input.messages.length !== 1 ||
      !input.messages[0] ||
      input.messages[0].role !== "user" ||
      !Array.isArray(input.messages[0].content)
    )
      throw new Error("input.messages must contain one user message with a content array");
    let texts = 0;
    for (const part of input.messages[0].content) {
      if (!part || typeof part !== "object" || Array.isArray(part)) throw new Error("invalid image message content");
      if (part.text !== undefined) {
        if (typeof part.text !== "string" || !part.text.trim()) throw new Error("text must be a non-empty string");
        texts++;
      }
      if (part.image !== undefined) {
        if (!isImageInput(part.image)) throw new Error("image must be an HTTP URL or Base64 data URL");
        imageCount++;
      }
    }
    if (texts !== 1) throw new Error("input.messages must contain exactly one text prompt");
    const maxInput = profile === "image27" ? 9 : profile === "image26" ? (parameters.enable_interleave ? 1 : 4) : profile === "qwen" ? limits.maxInput : 0;
    if (imageCount > maxInput) throw new Error(maxInput === 0 ? "this model only supports text-to-image input" : "too many input images for this model");
    if (profile === "image26" && !parameters.enable_interleave && imageCount === 0)
      throw new Error("wan2.6-image editing requires a reference image; use wan2.6-t2i for text-to-image");
    if (profile === "qwen" && limits.minInput && imageCount < limits.minInput) throw new Error("this image editing model requires at least one input image");
  }
  if (parameters.size !== undefined) {
    if (typeof parameters.size !== "string") throw new Error("size must be a string");
    parameters.size = parameters.size.replace(/x/i, "*");
    const supports4K = modelKey(model) === "wan2.7-image-pro" && !imageCount && !parameters.enable_sequential;
    if (["1K", "2K", "4K"].includes(parameters.size)) {
      if ((profile !== "image26" && profile !== "image27") || parameters.enable_interleave) throw new Error("this model requires a width*height image size");
      if (parameters.size === "4K" && !supports4K) throw new Error("4K is only supported by wan2.7-image-pro text-to-image without sequential output");
    } else {
      const dims = sizePixels(parameters.size);
      if (!dims) throw new Error("size must be width*height or a supported resolution preset");
      const width = dims.width,
        height = dims.height;
      const legacySize = profile === "legacy" && modelKey(model) !== "wan2.5-t2i-preview";
      const maxPixels =
        profile === "image27"
          ? supports4K
            ? 4096 * 4096
            : 2048 * 2048
          : profile === "image26"
            ? parameters.enable_interleave
              ? 1280 * 1280
              : 2048 * 2048
            : profile === "qwen"
              ? 2048 * 2048
              : profile === "legacy_edit"
                ? 1280 * 1280
                : 1440 * 1440;
      const minPixels = profile === "qwen" ? 512 * 512 : 0;
      const ratio = profile === "image27" || profile === "qwen" ? 8 : 4;
      if (
        legacySize
          ? width < 512 || height < 512 || width > 1440 || height > 1440
          : dims.pixels > maxPixels || dims.pixels < minPixels || width / height > ratio || height / width > ratio
      )
        throw new Error("size is outside the model's pixel and aspect-ratio limits");
    }
  }
  const service =
    profile === "legacy"
      ? "text2image/image-synthesis"
      : profile === "legacy_edit" || profile === "legacy_imageedit"
        ? "image2image/image-synthesis"
        : mode === "sync"
          ? "multimodal-generation/generation"
          : "image-generation/generation";
  return {
    body: { model: model, input: input, parameters: parameters },
    service: service,
    synchronous: mode === "sync",
    action: imageCount ? "image_to_image" : "text_to_image",
    inputImages: imageCount,
  };
}

// Submit-time billing facts: the requested count (max_images for interleaved
// output), the Z-Image prompt rewriting flag, and the Qwen-Image-3.0 tier
// estimated from the requested size (an unspecified size reserves the higher
// tier). Completion facts replace these key by key.
function imageEstimate(ctx, converted) {
  const parameters = converted.body.parameters;
  const facts = { image_count: parameters.enable_interleave ? parameters.max_images : parameters.n };
  if (zImage(ctx)) facts.prompt_extend = parameters.prompt_extend === true;
  if (qwenImage3(ctx)) {
    const dims = typeof parameters.size === "string" ? sizePixels(parameters.size) : null;
    facts.output_image_type = dims && dims.pixels <= QWEN_IMAGE3_TIER_MAX_PIXELS ? "qima_output_1k" : "qima_output_2k";
    facts.input_image_count = converted.inputImages;
  }
  return facts;
}

// Legacy per-call pricing multiplies the model price by these ratios; the
// Z-Image prompt rewriting surcharge survives as its own ratio because the
// boolean fact cannot be a multiplier.
function imageRatios(ctx, converted, count) {
  const ratios = { image_count: count };
  if (zImage(ctx) && converted.body.parameters.prompt_extend === true) ratios.prompt_extend_ratio = Z_IMAGE_PROMPT_EXTEND_RATIO;
  return ratios;
}

function imageContent(body) {
  const output = (body && body.output) || {};
  const content = [];
  for (const result of Array.isArray(output.results) ? output.results : []) {
    if (result && typeof result.url === "string" && result.url) content.push({ image: result.url });
  }
  for (const choice of Array.isArray(output.choices) ? output.choices : []) {
    for (const part of choice && choice.message && Array.isArray(choice.message.content) ? choice.message.content : []) {
      if (part && typeof part.image === "string" && part.image) content.push({ image: part.image });
      else if (part && typeof part.text === "string") content.push({ text: part.text });
    }
  }
  return content;
}

// Completion facts. The count prefers usage.image_count, then the
// Qwen-Image-3.0 usage.output_image_count, and otherwise counts image payloads
// flattened across output.results[] and every choices[].message.content[]
// part; payload-less entries are never counted. An inconsistent upstream
// count is rejected so the host keeps the reservation instead of guessing.
function imageUsage(ctx, body) {
  const usage = (body && body.usage) || {};
  const content = imageContent(body);
  const images = content.filter(function (part) {
    return part.image;
  });
  if (images.length > MAX_IMAGE_OUTPUTS) throw new Error("too many output images");
  const reported = usage.image_count !== undefined ? usage.image_count : usage.output_image_count;
  let count;
  if (reported !== undefined) {
    // Do not let fractional, negative or oversized upstream counts change billing.
    if (!Number.isInteger(reported) || reported < 0 || reported > MAX_IMAGE_OUTPUTS || (images.length && reported === 0))
      throw new Error("invalid upstream image count");
    count = reported;
  } else if (content.length) count = images.length;
  else return {};
  const facts = { image_count: count };
  if (qwenImage3(ctx)) {
    if (QWEN_IMAGE3_USAGE_SCHEMA.output_image_type.enum.includes(usage.output_image_type)) facts.output_image_type = usage.output_image_type;
    if (Number.isInteger(usage.input_image_count) && usage.input_image_count >= 0 && usage.input_image_count <= 3)
      facts.input_image_count = usage.input_image_count;
  }
  return facts;
}

// One OpenAI ImageResponse entry from a DashScope image payload.
function imageDatum(image) {
  if (/^https?:\/\//i.test(image)) return { url: image };
  const match = /^data:[^;,]*;base64,(.*)$/s.exec(image);
  return { b64_json: match ? match[1] : image };
}

function objectValue(value, name) {
  if (value === undefined || value === null) return {};
  if (typeof value !== "object" || Array.isArray(value)) throw new Error(name + " must be an object");
  return value;
}

// The host rejects negative canonical duration/seconds facts before any hook
// runs, so wan3.0's "-1 = smart duration" sentinel travels as a boolean marker.
function normalizeRequest(value) {
  const req = Object.assign({}, value);
  const metadata = objectValue(req.metadata, "metadata");
  const parameters = Object.assign({}, objectValue(metadata.parameters, "metadata.parameters"));
  if (req.metadata !== undefined) {
    req.metadata = Object.assign({}, metadata, { parameters: parameters });
    objectValue(metadata.input, "metadata.input");
  }
  const duration = parameters.duration ?? req.duration ?? req.seconds ?? (req.auto_duration === true ? -1 : undefined);
  for (const key of ["duration", "seconds"]) {
    if (Number(req[key]) === -1) delete req[key];
  }
  if (Number(parameters.duration) === -1) delete parameters.duration;
  if (Number(duration) === -1) {
    req.auto_duration = true;
    delete req.duration;
    delete req.seconds;
  } else delete req.auto_duration;
  // Normalize enum facts for host validation against the model's usage schema.
  if (req.resolution != null) req.resolution = normalizeResolution(req.resolution);
  if (parameters.resolution != null) parameters.resolution = normalizeResolution(parameters.resolution);
  return req;
}

function normalizeResolution(value) {
  let resolution = trimmed(value).toUpperCase();
  if (!resolution) return "";
  if (!resolution.endsWith("P")) resolution += "P";
  return resolution;
}

function videoSize(value) {
  const size = trimmed(value).replace(/x/i, "*");
  if (!size.includes("*")) return { resolution: normalizeResolution(size) };
  for (const sizes of [LEGACY_SIZES, MODERN_SIZES]) {
    for (const resolution of Object.keys(sizes)) {
      for (const ratio of Object.keys(sizes[resolution])) {
        if (sizes[resolution][ratio] === size) return { resolution: resolution, ratio: ratio };
      }
    }
  }
  throw new Error("invalid size: " + size);
}

function videoAction(req) {
  const input = objectValue((req.metadata || {}).input, "metadata.input");
  for (const source of [req, input]) {
    if (
      firstImage(source) ||
      trimmed(source.img_url) ||
      trimmed(source.image_url) ||
      trimmed(source.first_frame_url) ||
      (Array.isArray(source.media) && source.media.length)
    )
      return "image_to_video";
  }
  return "text_to_video";
}

function convert(ctx) {
  const req = ctx.requestBody;
  const upstreamModel = ctx.upstreamModel || req.model;
  const profile = modelProfile(upstreamModel);
  const metadata = objectValue(req.metadata, "metadata");
  if (metadata.model !== undefined && metadata.model !== upstreamModel) throw new Error("can't change model with metadata");
  const input = {};
  for (const key of ["prompt", "negative_prompt", "img_url", "image_url", "first_frame_url", "last_frame_url", "audio_url", "template", "media"]) {
    if (req[key] !== undefined) input[key] = req[key];
  }
  const image = firstImage(req);
  if (image && input.img_url === undefined) input.img_url = image;
  Object.assign(input, objectValue(metadata.input, "metadata.input"));
  const nativeParameters = objectValue(metadata.parameters, "metadata.parameters");
  const parameters = profile.kind === "speech" ? {} : { prompt_extend: true };
  for (const key of ["resolution", "ratio", "prompt_extend", "watermark", "audio", "seed", "shot_type"]) {
    if (req[key] !== undefined) parameters[key] = req[key];
  }
  Object.assign(parameters, nativeParameters);
  for (const key of ["prompt_extend", "watermark", "audio"]) {
    if (parameters[key] != null && typeof parameters[key] !== "boolean") throw new Error(key + " must be a boolean");
  }
  if (parameters.seed != null && (!Number.isInteger(parameters.seed) || parameters.seed < (profile.kind === "all" ? -1 : 0) || parameters.seed > 2147483647))
    throw new Error("seed must be an integer between " + (profile.kind === "all" ? -1 : 0) + " and 2147483647");

  const sizeValue = nativeParameters.size ?? req.size;
  const size = sizeValue == null || sizeValue === "" ? {} : videoSize(sizeValue);
  let resolution = normalizeResolution(parameters.resolution ?? size.resolution ?? profile.defaultResolution);
  let ratio = parameters.ratio ?? size.ratio ?? (profile.kind === "all" ? "adaptive" : "16:9");
  // In the legacy T2V protocol the explicit native size is authoritative.
  if (profile.kind === "size" && nativeParameters.size != null && nativeParameters.size !== "") {
    resolution = size.resolution;
    ratio = size.ratio ?? parameters.ratio ?? "16:9";
  }
  if (!profile.resolutions.includes(resolution)) throw new Error(upstreamModel + " resolution must be one of " + profile.resolutions.join(", "));
  if (profile.kind === "size" || profile.kind === "t2v" || profile.kind === "all") {
    const ratios = ["16:9", "9:16", "1:1", "4:3", "3:4"];
    if (profile.kind === "all") ratios.push("adaptive");
    if (!ratios.includes(ratio)) throw new Error(upstreamModel + " ratio must be one of " + ratios.join(", "));
  }
  if (profile.kind === "size") {
    const pixelSize = LEGACY_SIZES[resolution][ratio];
    if (!pixelSize) throw new Error("unsupported ratio for " + resolution + ": " + ratio);
    parameters.size = pixelSize;
    delete parameters.resolution;
    delete parameters.ratio;
  } else {
    parameters.resolution = resolution;
    delete parameters.size;
    if (profile.kind === "t2v" || profile.kind === "all") parameters.ratio = ratio;
    else delete parameters.ratio; // Image-derived aspect ratios are not configurable.
  }

  const rawDuration = nativeParameters.duration ?? (req.auto_duration === true ? -1 : (req.duration ?? req.seconds));
  if (rawDuration != null && ((typeof rawDuration !== "number" && typeof rawDuration !== "string") || String(rawDuration).trim() === ""))
    throw new Error("duration must be a number");
  const duration = rawDuration == null ? 5 : Number(rawDuration);
  if (duration === -1 && profile.kind !== "all") throw new Error("duration -1 (smart duration) is only supported by wan3.0 models");
  if (profile.kind === "speech") {
    if (rawDuration != null && (!Number.isFinite(duration) || duration <= 0 || duration >= 20))
      throw new Error("wan2.2-s2v duration must be positive and less than 20 seconds; output follows the audio");
    delete parameters.duration;
  } else {
    if (profile.durations) {
      if (!profile.durations.includes(duration)) throw new Error(upstreamModel + " duration must be one of " + profile.durations.join(", "));
    } else if (!(profile.kind === "all" && duration === -1) && (!Number.isInteger(duration) || duration < 2 || duration > profile.maxDuration)) {
      throw new Error(upstreamModel + " duration must be " + (profile.kind === "all" ? "-1 or " : "") + "an integer between 2 and " + profile.maxDuration);
    }
    parameters.duration = duration;
  }

  if (profile.kind === "media" || profile.kind === "all") {
    if (input.media !== undefined && !Array.isArray(input.media)) throw new Error("input.media must be an array");
    if (!input.media || input.media.length === 0) {
      input.media = [];
      const first = trimmed(input.first_frame_url) || trimmed(input.img_url) || firstImage(req);
      const last = trimmed(input.last_frame_url) || secondImage(req);
      if (first) input.media.push({ type: "first_frame", url: first });
      if (last) input.media.push({ type: "last_frame", url: last });
      if (trimmed(input.audio_url)) input.media.push({ type: profile.kind === "media" ? "driving_audio" : "reference_audio", url: input.audio_url });
    }
    const counts = {};
    const limits =
      profile.kind === "media"
        ? { first_frame: 1, last_frame: 1, driving_audio: 1, first_clip: 1 }
        : { first_frame: 1, last_frame: 1, reference_image: 10, reference_video: 5, reference_audio: 5, file: 1, link: 1 };
    for (const media of input.media) {
      if (!media || !Object.prototype.hasOwnProperty.call(limits, media.type) || !trimmed(media.url)) throw new Error("invalid input.media type or url");
      counts[media.type] = (counts[media.type] || 0) + 1;
      if (counts[media.type] > limits[media.type]) throw new Error("too many input.media entries for " + media.type);
    }
    if (profile.kind === "media") {
      if ((!counts.first_frame && !counts.first_clip) || (counts.first_clip && (counts.first_frame || counts.driving_audio)))
        throw new Error("wan2.7-i2v requires first_frame or first_clip with a supported media combination");
    } else {
      const frames = counts.first_frame || counts.last_frame;
      const references = counts.reference_image || counts.reference_video || counts.reference_audio || counts.file || counts.link;
      if ((frames && references) || (counts.file && counts.link) || (counts.last_frame && !counts.first_frame))
        throw new Error("unsupported wan3.0 input.media combination");
      if (!trimmed(input.prompt) && !input.media.length) throw new Error("wan3.0-video requires prompt or input.media");
    }
    if (!input.media.length) delete input.media;
    delete input.img_url;
    delete input.first_frame_url;
    delete input.last_frame_url;
    delete input.audio_url;
  } else if (profile.kind === "frames") {
    input.first_frame_url = trimmed(input.first_frame_url) || trimmed(input.img_url);
    if (!input.first_frame_url) throw new Error(upstreamModel + " requires first_frame_url or image");
    if (!input.last_frame_url && secondImage(req)) input.last_frame_url = secondImage(req);
    delete input.img_url;
  } else if (profile.kind === "speech") {
    input.image_url = trimmed(input.image_url) || trimmed(input.img_url);
    if (!input.image_url || !trimmed(input.audio_url)) throw new Error("wan2.2-s2v requires image_url and audio_url");
    delete input.img_url;
    if (!trimmed(input.prompt)) delete input.prompt;
  } else if (profile.kind === "image") {
    if (!trimmed(input.img_url)) throw new Error(upstreamModel + " requires img_url or image");
  } else if (!trimmed(input.prompt)) {
    throw new Error("input is required");
  }
  return { model: upstreamModel, input: input, parameters: parameters };
}

function resolutionRatio(body) {
  const resolution = body.parameters.size ? videoSize(body.parameters.size).resolution : body.parameters.resolution;
  const ratios = {
    "wan3.0-video": { "480P": 1, "720P": 2, "1080P": 4 },
    "wan3.0-video-prime": { "480P": 1, "720P": 2, "1080P": 4 },
    "wan2.6-i2v": { "720P": 1, "1080P": 1 / 0.6 },
    "wan2.5-t2v-preview": { "480P": 1, "720P": 2, "1080P": 1 / 0.3 },
    "wan2.2-t2v-plus": { "480P": 1, "1080P": 5 },
    "wan2.5-i2v-preview": { "480P": 1, "720P": 2, "1080P": 1 / 0.3 },
    "wan2.2-i2v-plus": { "480P": 1, "1080P": 5 },
    "wan2.2-kf2v-flash": { "480P": 1, "720P": 2, "1080P": 4.8 },
    "wan2.2-i2v-flash": { "480P": 1, "720P": 2 },
    "wan2.2-s2v": { "480P": 1, "720P": 1.8 },
  };
  const model = modelKey(body.model);
  return ratios[model] ? { key: "resolution-" + resolution, value: ratios[model][resolution] } : null;
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
  const escaped = url
    .replace(/&/g, "&amp;")
    .replace(/\u0022/g, "&quot;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;");
  return '<video controls src="' + escaped + '"></video>';
}

function responsesOutputText(ctx, task) {
  const content = imageContent(artifactData(task));
  if (!content.length) return responsesVideoText(ctx);
  const parts = [];
  let index = 0;
  for (const part of content) {
    if (part.image) {
      const key = "image-" + ++index;
      const artifact = (ctx.artifacts || {})[key];
      if (!artifact || !artifact.url) throw new Error("image artifact is unavailable");
      parts.push("![Image " + index + "](<" + artifact.url + ">)");
    } else parts.push(part.text);
  }
  return parts.join("\n\n");
}

export function buildSubmitRequest(ctx) {
  if (imageModel(ctx)) {
    const converted = convertImage(ctx);
    const headers = { Authorization: "Bearer " + ctx.apiKey, "Content-Type": "application/json" };
    if (!converted.synchronous) headers["X-DashScope-Async"] = "enable";
    // A gateway's native route aggregates the vendor stream itself and answers
    // with one JSON body, so only DashScope is asked for SSE.
    const streaming = converted.synchronous && converted.body.parameters.enable_interleave === true && !viaNewAPI(ctx);
    if (streaming) headers["X-DashScope-Sse"] = "enable";
    return {
      url: apiRoot(ctx) + "/api/v1/services/aigc/" + converted.service,
      method: "POST",
      headers: headers,
      body: converted.body,
      action: converted.action,
      responseType: streaming ? "sse" : "json",
    };
  }
  const body = convert(ctx);
  const kind = modelProfile(body.model).kind;
  const service = kind === "frames" || kind === "speech" ? "image2video" : "video-generation";
  return {
    url: apiRoot(ctx) + "/api/v1/services/aigc/" + service + "/video-synthesis",
    method: "POST",
    headers: { Authorization: "Bearer " + ctx.apiKey, "Content-Type": "application/json", "X-DashScope-Async": "enable" },
    body: body,
    action: videoAction(body.input),
  };
}

// DashScope sends text/image deltas but cumulative usage. The host applies
// generic JSON changes; vendor completion and usage semantics remain here.
export function parseSubmitEventDelta(ctx, event, previousState) {
  const body = JSON.parse(event.data);
  if (!body || typeof body !== "object" || Array.isArray(body)) throw new Error("invalid DashScope stream event");
  if (body.code || event.event === "error") throw new Error((body.code || "upstream_error") + ": " + (body.message || "stream failed"));
  const state = utils.json.clone(previousState || { choices: [], hasUsage: false });
  const changes = [];
  if (!previousState) changes.push({ op: "set", path: [], value: { output: { choices: [], finished: false } } });
  if (body.request_id) changes.push({ op: "set", path: ["request_id"], value: body.request_id });
  if (body.usage) {
    if (!state.hasUsage) {
      changes.push({ op: "set", path: ["usage"], value: {} });
      state.hasUsage = true;
    }
    for (const key of Object.keys(body.usage)) changes.push({ op: "set", path: ["usage", key], value: body.usage[key] });
  }
  const choices = (body.output || {}).choices || [];
  for (let index = 0; index < choices.length; index++) {
    const choice = choices[index];
    if (!choice || !choice.message || !Array.isArray(choice.message.content)) throw new Error("invalid DashScope stream choice");
    if (!state.choices[index]) {
      state.choices.push({ count: 0, lastText: false, finishReason: "" });
      changes.push({ op: "append", path: ["output", "choices"], value: { message: { role: "assistant", content: [] } } });
    }
    const control = state.choices[index];
    for (const part of choice.message.content) {
      if (!part || typeof part !== "object") throw new Error("invalid DashScope stream content");
      if (typeof part.text === "string" && control.lastText) {
        changes.push({ op: "appendText", path: ["output", "choices", index, "message", "content", control.count - 1, "text"], value: part.text });
      } else {
        changes.push({ op: "append", path: ["output", "choices", index, "message", "content"], value: part });
        control.count++;
        control.lastText = typeof part.text === "string";
      }
    }
    if (choice.finish_reason && choice.finish_reason !== "null") {
      control.finishReason = choice.finish_reason;
      changes.push({ op: "set", path: ["output", "choices", index, "finish_reason"], value: choice.finish_reason });
    }
  }
  // Some intermediate frames carry finished:true; only terminal finish reasons
  // make the complete output ready for parsing and billing.
  const done =
    state.choices.length > 0 &&
    state.choices.every(function (choice) {
      return choice.finishReason === "stop";
    });
  changes.push({ op: "set", path: ["output", "finished"], value: done });
  return { changes: changes, state: state, done: done };
}

export function parseSubmitResponse(ctx, resp) {
  const body = resp.body || {};
  if (body.code) throw new Error(body.code + ": " + (body.message || ""));
  if (imageModel(ctx) && convertImage(ctx).synchronous) {
    const content = imageContent(body);
    const images = content.filter(function (part) {
      return part.image;
    });
    const interleaved = convertImage(ctx).body.parameters.enable_interleave === true;
    if ((!images.length && !(interleaved && content.length)) || (body.output || {}).finished === false)
      throw new Error("synchronous image response has no completed output");
    // No vendor task exists for a synchronous HTTP response. The host already
    // allocated a public ID; retain that identity without inventing a pollable ID.
    const taskId = ctx.publicTaskId || body.request_id;
    if (!taskId) throw new Error("missing synchronous image request id");
    return { taskId: taskId, taskData: body, immediate: { status: "SUCCESS", progress: "100%", url: images.length ? images[0].image : "" } };
  }
  if (!body.output || !body.output.task_id) throw new Error("task_id is empty");
  return { taskId: body.output.task_id, taskData: body };
}

export function extractUsage(ctx) {
  if (imageModel(ctx)) {
    const converted = convertImage(ctx);
    const estimate = imageEstimate(ctx, converted);
    if (ctx.usagePurpose === "billing_ratios") return imageRatios(ctx, converted, estimate.image_count);
    return estimate;
  }
  const body = convert(ctx);
  const kind = modelProfile(body.model).kind;
  const hasVideo = (body.input.media || []).some(function (media) {
    return media.type === "reference_video";
  });
  let seconds = body.parameters.duration;
  if (kind === "speech") seconds = 20;
  else if (kind === "all" && (hasVideo || seconds === -1)) seconds = 30;
  if (ctx.usagePurpose === "billing_ratios") {
    const ratios = { seconds: seconds };
    const resolution = resolutionRatio(body);
    if (resolution && resolution.value !== undefined) ratios[resolution.key] = resolution.value;
    return ratios;
  }
  const resolution = body.parameters.size ? videoSize(body.parameters.size).resolution : body.parameters.resolution;
  const facts = { seconds: seconds, resolution: resolution };
  if (WAN_AUDIO_VIDEO_MODELS.includes(modelKey(body.model))) facts.audio = body.parameters.audio !== false;
  return facts;
}

export function extractUsageOnSubmit(ctx, body) {
  // Legacy ratio pricing uses this hook; task expressions use the same actual
  // facts through extractUsageOnComplete for both immediate and polled results.
  if (!imageModel(ctx)) return {};
  const converted = convertImage(ctx);
  if (!converted.synchronous) return {};
  const actual = imageUsage(ctx, body || {});
  return actual.image_count === undefined ? {} : imageRatios(ctx, converted, actual.image_count);
}

export function extractUsageOnComplete(task, taskResult, body) {
  if (imageModel(task)) return imageUsage(task, body || {});
  const output = (body && body.output) || {};
  const usage = (body && body.usage) || {};
  const facts = {};
  const model = modelKey(task && (task.upstreamModel || task.model));
  if (model === "wan3.0-video" || model === "wan3.0-video-prime" || (!model && usage.input_video_duration != null)) {
    const inputSeconds = usage.input_video_duration;
    const outputSeconds = usage.output_video_duration ?? usage.duration;
    // Partial Wan3 statistics cannot establish the total billable duration.
    // Keep the reservation when either side is missing. Invalid supplied facts
    // reach the host validator unchanged, which logs and rejects them.
    if (inputSeconds != null && outputSeconds != null) {
      if (typeof inputSeconds !== "number" || !Number.isFinite(inputSeconds) || inputSeconds < 0) facts.seconds = inputSeconds;
      else if (typeof outputSeconds !== "number" || !Number.isFinite(outputSeconds) || outputSeconds < 0) facts.seconds = outputSeconds;
      else facts.seconds = inputSeconds + outputSeconds;
    }
  } else {
    const seconds = usage.duration ?? usage.output_video_duration ?? output.duration ?? output.duration_seconds;
    if (seconds != null) facts.seconds = seconds;
  }
  const resolution = usage.SR ?? output.resolution;
  if (resolution != null) facts.resolution = normalizeResolution(resolution);
  if (WAN_AUDIO_VIDEO_MODELS.includes(model) && typeof usage.audio === "boolean") facts.audio = usage.audio;
  return facts;
}

export function buildQueryRequest(ctx) {
  return { url: apiRoot(ctx) + "/api/v1/tasks/" + ctx.taskId, method: "GET", headers: { Authorization: "Bearer " + ctx.apiKey } };
}

export function parseTaskResult(ctx, body) {
  const output = body.output || {};
  if (output.task_status === "PENDING") return { status: "QUEUED" };
  if (output.task_status === "RUNNING") return { status: "IN_PROGRESS" };
  if (output.task_status === "SUCCEEDED") {
    if (imageModel(ctx)) {
      const content = imageContent(body);
      const images = content.filter(function (part) {
        return part.image;
      });
      if (!images.length) {
        if (
          imageModel(ctx) === "image26" &&
          content.some(function (part) {
            return trimmed(part.text);
          })
        )
          return { status: "SUCCESS" };
        return { status: "FAILURE", reason: "image task succeeded without any images" };
      }
      return { status: "SUCCESS", url: images[0].image };
    }
    return { status: "SUCCESS", url: videoURL(body) };
  }
  if (["FAILED", "CANCELED", "UNKNOWN"].includes(output.task_status)) {
    let reason = body.message || "";
    if (!reason && output.message) reason = "task failed, code: " + (output.code || "") + " , message: " + output.message;
    if (!reason) reason = "task failed";
    return { status: "FAILURE", reason: reason };
  }
  return { status: "UNKNOWN", reason: "unrecognized status: " + String(output.task_status || "") };
}

function artifactData(ctx) {
  const data = (ctx && ctx.data) || {};
  if (data.data && typeof data.data === "object" && data.data.task_id && Object.prototype.hasOwnProperty.call(data.data, "data")) return data.data.data || {};
  return data;
}

function videoURL(body) {
  const output = (body && body.output) || {};
  return trimmed(output.video_url) || trimmed((output.results || {}).video_url);
}

export function listArtifacts(task) {
  if (task.status !== "SUCCESS") return [];
  const body = artifactData(task);
  const images = imageContent(body).filter(function (part) {
    return part.image;
  });
  if (images.length)
    return images.map(function (_, index) {
      return { key: "image-" + (index + 1), type: "image" };
    });
  return videoURL(body) ? [{ key: "video", type: "video" }] : [];
}

export function buildContentRequest(ctx) {
  let url;
  if (ctx.artifactKey === "video") url = videoURL(artifactData(ctx));
  else {
    const images = imageContent(artifactData(ctx)).filter(function (part) {
      return part.image;
    });
    const index = images.findIndex(function (_, index) {
      return ctx.artifactKey === "image-" + (index + 1);
    });
    url = index >= 0 ? images[index].image : "";
  }
  if (!url) throw new Error("artifact_not_found");
  return { url: url, method: ctx.clientRequest.method, credentialless: true };
}

export const native = {
  createImageTask: function (ctx) {
    if (!ctx.body || ctx.body.kind !== "json" || !ctx.body.value || typeof ctx.body.value !== "object" || Array.isArray(ctx.body.value))
      throw new Error("JSON object required");
    const req = ctx.body.value;
    if (req.stream !== undefined && req.stream !== false)
      throw new Error("native task responses are aggregated JSON; configure upstream streaming in parameters");
    const requestBody = {
      model: req.model,
      metadata: {
        input: objectValue(req.input, "input"),
        parameters: objectValue(req.parameters, "parameters"),
        upstream_mode: String(ctx.path || "").includes("/multimodal-generation/") ? "sync" : "async",
      },
    };
    return { kind: "submit", model: req.model, requestBody: requestBody };
  },
  imageCreated: function (ctx, task) {
    return task.data || {};
  },
  createVideoTask: function (ctx) {
    if (!ctx.body || ctx.body.kind !== "json" || !ctx.body.value || typeof ctx.body.value !== "object" || Array.isArray(ctx.body.value))
      throw new Error("JSON object required");
    const req = ctx.body.value,
      input = objectValue(req.input, "input"),
      parameters = objectValue(req.parameters, "parameters");
    const requestBody = {
      model: req.model,
      metadata: { input: input, parameters: parameters },
    };
    if (input.prompt !== undefined) requestBody.prompt = input.prompt;
    const image = input.img_url || input.image_url || input.first_frame_url;
    if (image !== undefined) requestBody.image = image;
    return {
      kind: "submit",
      model: req.model,
      action: videoAction(requestBody),
      requestBody: normalizeRequest(requestBody),
    };
  },
  taskCreated: function (ctx, task) {
    const data = task.data || {};
    return { request_id: data.request_id || "", output: { task_id: task.task_id, task_status: "PENDING" } };
  },
  taskStatus: function (ctx, task) {
    const data = task.data || {},
      output = Object.assign({}, data.output || {}, { task_id: task.task_id });
    return Object.assign({}, data, { output: output });
  },
  error: function (ctx, error) {
    return { code: error.code, message: error.message, request_id: "" };
  },
};

export const protocols = {
  openai_responses: {
    decodeRequest: function (ctx) {
      if (!ctx.body || ctx.body.kind !== "json") throw new Error("JSON body required");
      const req = ctx.body.value;
      if (!req || typeof req !== "object" || Array.isArray(req)) throw new Error("request body must be an object");
      const model = trimmed(ctx.model);
      if (!model) throw new Error("model is required");
      if (req.input !== undefined && typeof req.input !== "string" && !Array.isArray(req.input)) throw new Error("input must be a string or array");
      const input = responsesInput(req);
      const prompt = input.prompt || trimmed(req.prompt);
      let requestBody = { model: model, prompt: prompt };
      if (trimmed(req.image)) requestBody.image = trimmed(req.image);
      if (req.images !== undefined && !Array.isArray(req.images)) throw new Error("images must be an array");
      const images = [];
      for (const image of req.images || []) if (trimmed(image) && !images.includes(trimmed(image))) images.push(trimmed(image));
      for (const image of input.images) if (!images.includes(image)) images.push(image);
      if (images.length) requestBody.images = images;
      if (trimmed(req.input_reference)) requestBody.input_reference = trimmed(req.input_reference);
      for (const key of [
        "size",
        "resolution",
        "ratio",
        "duration",
        "seconds",
        "auto_duration",
        "prompt_extend",
        "watermark",
        "audio",
        "seed",
        "shot_type",
        "negative_prompt",
        "img_url",
        "image_url",
        "first_frame_url",
        "last_frame_url",
        "audio_url",
        "template",
        "media",
      ]) {
        if (Object.prototype.hasOwnProperty.call(req, key)) requestBody[key] = req[key];
      }
      if (Object.prototype.hasOwnProperty.call(req, "metadata")) requestBody.metadata = req.metadata;
      if (imageModel({ upstreamModel: ctx.upstreamModel, model: model })) {
        for (const key of ["n", "enable_interleave", "max_images", "enable_sequential", "thinking_mode", "bbox_list", "color_palette"]) {
          if (Object.prototype.hasOwnProperty.call(req, key)) requestBody[key] = req[key];
        }
        // The model may be a mapped alias; final validation runs after channel selection.
        return { kind: "submit", model: model, action: firstImage(requestBody) ? "image_to_image" : "text_to_image", requestBody: requestBody };
      }
      requestBody = normalizeRequest(requestBody);
      const action = videoAction(requestBody);
      const profile = WAN_MODELS[modelKey(ctx.upstreamModel || model)];
      const nativeInput = objectValue((requestBody.metadata || {}).input, "metadata.input");
      if (!trimmed(nativeInput.prompt ?? prompt) && (action === "text_to_video" || (profile && (profile.kind === "size" || profile.kind === "t2v"))))
        throw new Error("input is required");
      return { kind: "submit", model: model, action: action, requestBody: requestBody };
    },
    renderEvents: function (ctx, task, previousState) {
      const status = String(task.status || "UNKNOWN").toUpperCase();
      const value = Number(String(task.progress || "").replace("%", ""));
      const progress = Number.isFinite(value) && value >= 0 && value <= 100 ? value : null;
      const state = { status: status, progress: progress };
      if (status === "SUCCESS") {
        const text = responsesOutputText(ctx, task);
        const events = previousState && previousState.status === status ? [] : text ? [{ type: "output", data: text }] : [];
        return { events: events, state: state, done: true };
      }
      if (status === "FAILURE") {
        return { events: [{ type: "error", code: "task_failed", message: "task failed" }], state: state, done: true };
      }
      if (previousState && previousState.status === status && previousState.progress === progress) {
        return { events: [], state: state, done: false };
      }
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
            content: [
              {
                type: "output_text",
                text: responsesOutputText(ctx, task),
                annotations: [],
                logprobs: [],
              },
            ],
          },
        ],
        metadata: { vendor: "ali" },
      };
    },
  },
  // OpenAI Images API. The host pins ctx.model, waits for the task to become
  // terminal and honors response_format itself; the plugin maps the request
  // onto the DashScope image services and renders data[] from the result.
  openai_image: {
    decodeRequest: function (ctx) {
      const model = trimmed(ctx.model);
      if (!model) throw new Error("model is required");
      let req = {};
      const uploads = [];
      if (ctx.body && ctx.body.kind === "json") {
        req = ctx.body.value;
        if (!req || typeof req !== "object" || Array.isArray(req)) throw new Error("request body must be an object");
      } else if (ctx.body && ctx.body.kind === "multipart") {
        const fields = ctx.body.fields || {};
        for (const name of Object.keys(fields)) {
          if (fields[name].length > 1) throw new Error(name + " must be provided once");
          req[name] = fields[name][0];
        }
        for (const key of ["n", "seed", "max_images", "upscale_factor", "strength", "top_scale", "bottom_scale", "left_scale", "right_scale"]) {
          if (req[key] !== undefined) req[key] = Number(req[key]);
        }
        for (const key of ["prompt_extend", "watermark", "enable_thinking", "enable_interleave", "enable_sequential", "thinking_mode", "is_sketch"]) {
          if (req[key] === undefined) continue;
          if (req[key] !== "true" && req[key] !== "false") throw new Error(key + " must be true or false");
          req[key] = req[key] === "true";
        }
        for (const key of ["parameters", "input"]) {
          if (req[key] === undefined) continue;
          try {
            req[key] = JSON.parse(req[key]);
          } catch (e) {
            throw new Error(key + " must be a JSON object string");
          }
        }
        for (const file of ctx.body.files || []) {
          const upload = { __fileRef: file.ref, encoding: "dataUrl", mimeType: trimmed(file.mimeType) || "image/png", maxBytes: MAX_INPUT_IMAGE_BYTES };
          // A mask upload is not a reference image; only wanx2.1-imageedit reads it.
          if (file.field === "mask") req.mask = upload;
          else if (/^image(\[\d*\])?$/.test(file.field)) uploads.push(upload);
        }
      } else throw new Error("JSON or multipart body required");
      if (req.stream !== undefined && req.stream !== false && req.stream !== "false")
        throw new Error("stream is not supported; the complete image response is returned once all images are generated");
      if (req.response_format !== undefined && req.response_format !== "url" && req.response_format !== "b64_json")
        throw new Error("response_format must be url or b64_json");
      const requestBody = { model: model, prompt: typeof req.prompt === "string" ? req.prompt : "" };
      if (!trimmed(requestBody.prompt) && !objectValue(req.input, "input").messages && !objectValue(req.input, "input").prompt)
        throw new Error("prompt is required");
      requestBody.n = req.n === undefined || req.n === null ? 1 : req.n;
      if (!Number.isInteger(requestBody.n) || requestBody.n < 1) throw new Error("n must be a positive integer");
      const images = [];
      for (const image of [].concat(req.image === undefined ? [] : req.image, req.images === undefined ? [] : req.images)) {
        if (isImageInput(image) || (typeof image === "string" && trimmed(image))) images.push(typeof image === "string" ? trimmed(image) : image);
        else throw new Error("image must be an HTTP URL or Base64 data URL");
      }
      for (const upload of uploads) images.push(upload);
      if (ctx.operation === "edit" && !images.length) throw new Error("image is required");
      if (images.length) requestBody.images = images;
      for (const key of [
        "size",
        "negative_prompt",
        "prompt_extend",
        "prompt_extend_mode",
        "enable_thinking",
        "watermark",
        "seed",
        "enable_interleave",
        "max_images",
        "enable_sequential",
        "thinking_mode",
        "bbox_list",
        "color_palette",
        "function",
        "mask",
        "strength",
        "top_scale",
        "bottom_scale",
        "left_scale",
        "right_scale",
        "upscale_factor",
        "is_sketch",
      ]) {
        if (Object.prototype.hasOwnProperty.call(req, key)) requestBody[key] = req[key];
      }
      // Provider passthrough: the same parameters/input objects the DashScope API accepts.
      const metadata = {};
      if (req.parameters !== undefined) metadata.parameters = objectValue(req.parameters, "parameters");
      if (req.input !== undefined) metadata.input = objectValue(req.input, "input");
      if (Object.keys(metadata).length) requestBody.metadata = metadata;
      // The model may be a mapped alias; final validation runs after channel selection.
      return { kind: "submit", model: model, action: images.length ? "image_to_image" : "text_to_image", requestBody: requestBody };
    },
    render: function (ctx, task) {
      const data = [];
      for (const part of imageContent(artifactData(task))) {
        if (part.image) data.push(imageDatum(part.image));
      }
      return { created: task.created_at, data: data, metadata: artifactData(task) };
    },
  },
  openai_video: {
    decodeRequest: function (ctx) {
      let req;
      if (ctx.body && ctx.body.kind === "json") req = ctx.body.value;
      else if (ctx.body && ctx.body.kind === "multipart") {
        if ((ctx.body.files || []).length) throw new Error("Alibaba requires image references to be URLs");
        const first = function (name) {
          const values = (ctx.body.fields || {})[name] || [];
          if (values.length > 1) throw new Error(name + " must be provided once");
          return values[0];
        };
        req = {};
        const fields = ctx.body.fields || {};
        for (const name of Object.keys(fields)) {
          if (name === "images") req.images = fields[name] || [];
          else req[name] = first(name);
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
        if (req.duration !== undefined) req.duration = Number(req.duration);
        if (req.seed !== undefined) req.seed = Number(req.seed);
        for (const key of ["prompt_extend", "watermark", "audio", "auto_duration"]) {
          if (req[key] === undefined) continue;
          if (req[key] !== "true" && req[key] !== "false") throw new Error(key + " must be true or false");
          req[key] = req[key] === "true";
        }
      } else throw new Error("JSON or multipart body required");
      if (!req || typeof req !== "object" || Array.isArray(req)) throw new Error("request body must be an object");
      if (req.images !== undefined && !Array.isArray(req.images)) throw new Error("images must be an array");
      const requestBody = normalizeRequest(Object.assign({}, req, { model: ctx.model }));
      return {
        kind: "submit",
        model: ctx.model,
        action: videoAction(requestBody),
        requestBody: requestBody,
      };
    },
    render: function (ctx, task) {
      const data = task.data || {},
        outputData = data.output || {};
      const statuses = {
        PENDING: "queued",
        RUNNING: "in_progress",
        SUCCEEDED: "completed",
        FAILED: "failed",
        CANCELED: "failed",
        UNKNOWN: "failed",
      };
      const output = {
        id: task.task_id,
        object: "video",
        model: task.properties ? task.properties.origin_model_name || "" : "",
        status: statuses[outputData.task_status] || "unknown",
        progress: Number(String(task.progress || "0").replace("%", "")),
        created_at: task.created_at,
        completed_at: task.updated_at,
      };
      if (data.code) output.error = { code: data.code, message: data.message || "" };
      else if (outputData.code) output.error = { code: outputData.code, message: outputData.message || "" };
      return output;
    },
  },
};

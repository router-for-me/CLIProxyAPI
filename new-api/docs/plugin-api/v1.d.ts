export type JSONValue = null | boolean | number | string | readonly JSONValue[] | {readonly [key: string]: JSONValue};
export type HostCapability = "json-clone@1" | "submit-sse-delta@1";
/** Kinds of upstream a driver can address: the vendor API itself, or another New API gateway with the same plugin installed. */
export type UpstreamKind = "vendor" | "new_api";
/** Host-injected on every driver hook context. With "new_api" the driver uses its own native-route prefix and the host already set Bearer credentials. */
export interface UpstreamContext {kind: UpstreamKind}
export type MutableJSON<T> = T extends readonly (infer Item)[] ? MutableJSON<Item>[] : T extends object ? {-readonly [Key in keyof T]: MutableJSON<T[Key]>} : T;
export interface HostUtils {
  hasCapability(name: string): boolean;
  /** Independent native JS containers; at most 1 MiB encoded JSON, depth 32 and 32,768 nodes. */
  json: {clone<T extends JSONValue>(value: T): MutableJSON<T>};
  unixNow(): number;
  uuid(): string;
  hmacSHA256(message: string, secret: string): string;
  jwtSignHS256(claims: Record<string, JSONValue>, secret: string): string;
  base64(value: string): string;
  base64URL(value: string): string;
  base64URLDecode(value: string): string;
  volcSignV4(request: {method: string; url: string; headers?: Record<string, string>; body?: string; accessKey: string; secretKey: string; region?: string; service?: string; timestamp?: number}): Record<string, string>;
}
declare global {const utils: HostUtils;}
export type FileReference = Readonly<{ref: string; field: string; filename: string; mimeType: string; size: number}>;
export type FilePlaceholder = Readonly<{__fileRef: string; encoding: "base64" | "dataUrl"; mimeType?: string; maxBytes?: number}>;
export type DecodedBody =
  | Readonly<{kind: "json"; value: JSONValue}>
  | Readonly<{kind: "form"; fields: Readonly<Record<string, readonly string[]>>}>
  | Readonly<{kind: "multipart"; fields: Readonly<Record<string, readonly string[]>>; files: readonly FileReference[]}>
  | Readonly<{kind: "none"}>;

export interface NativeDecodeContext {method: string; path: string; params: Readonly<Record<string, string>>; query: Readonly<Record<string, readonly string[]>>; body: DecodedBody}
export interface ProtocolDecodeContext extends NativeDecodeContext {protocol: ProtocolName; operation: string; model: string; upstreamModel?: string; stream: boolean}
export type SubmitIntent = {kind: "submit"; model: string; action?: string; requestBody?: unknown; originTaskIds?: readonly string[]};
export type QueryIntent = {kind: "query"; taskIds: readonly string[]};
export type TaskIntent = SubmitIntent | QueryIntent;
export interface NativeRoute {method: "GET" | "POST" | "PUT" | "PATCH" | "DELETE"; path: string; type: "submit" | "query" | "dynamic"; action?: string; taskIdParam?: string; decode?: string; render: string; models?: readonly string[]; retainResult?: boolean}
export type ProtocolName = "openai_responses" | "openai_video" | "openai_image";
export type ResponsesMode = "stream" | "sync" | "background";
export type ProtocolClaim =
  | "openai_video"
  | "openai_image"
  | {name: "openai_responses"; supports: readonly ResponsesMode[]; models?: readonly string[]}
  | {name: "openai_video"; models?: readonly string[]}
  | {name: "openai_image"; models?: readonly string[]};
/** One entry of the OpenAI ImageResponse `data` array rendered by protocols.openai_image.render. */
export type ImageResponseEntry = {url?: string; b64_json?: string; revised_prompt?: string};
export type LocalizedText = string | ({ en: string } & Record<string, string>);
export type UsageFieldSchema =
  | {type: "number"; unit: "count"; unitLabel?: LocalizedText; description?: LocalizedText}
  | {type: "number"; unit: "second" | "token" | "credit"; unitLabel?: never; description?: LocalizedText}
  | {type: "boolean"; unitLabel?: never; description?: LocalizedText}
  | {enum: readonly string[]; unitLabel?: never; description?: LocalizedText; enumLabels?: Readonly<Record<string, LocalizedText>>};
export type UsageExample = {label: string; facts: Readonly<Record<string, string | number | boolean>>};
export type UsageProfile = {models: readonly string[]; schema: Readonly<Record<string, UsageFieldSchema>>; examples?: readonly UsageExample[]};
export interface Meta {requiredCapabilities?: readonly HostCapability[]; submitResponseTypes?: readonly ("json" | "sse")[]; sortPriority?: number; website?: string; apiVersion: 1; key: string; name: string; icon?: string; description?: LocalizedText; version: string; author: {name: string; url?: string}; baseUrl?: string; channelTypes?: readonly number[]; models: readonly string[]; fetchMode: "per_task" | "batch"; allowedHosts?: readonly string[]; upstreams?: readonly UpstreamKind[]; routes?: readonly NativeRoute[]; protocols?: readonly ProtocolClaim[]; usageSchema?: Readonly<Record<string, UsageFieldSchema>>; usageExamples?: readonly UsageExample[]; usageProfiles?: readonly UsageProfile[]; auth?: "none" | "api_key" | "vertex_oauth" | {type: "none" | "api_key" | "oauth2_jwt"}}
export interface TaskView {task_id: string; status: string; progress?: string; fail_reason?: string; created_at?: number; updated_at?: number; data?: unknown; properties?: Record<string, unknown>}
export interface DriverContext {requestBody: unknown; requestHeaders: Readonly<Record<string, string>>; action: string; model: string; upstreamModel: string; baseUrl: string; apiKey?: string; authHeader: string; upstream: UpstreamContext; files: readonly FileReference[]; publicTaskId: string; originTasks?: readonly {taskId: string; upstreamTaskId: string; action: string; status: string; data: unknown}[]}
export interface TaskQueryContext {taskId: string; publicTaskId: string; action: string; model: string; upstreamModel: string; baseUrl: string; apiKey?: string; authHeader: string; auth?: unknown; upstream: UpstreamContext; data: unknown; state: unknown}
export interface BatchQueryContext {baseUrl: string; apiKey?: string; authHeader: string; auth?: unknown; upstream: UpstreamContext; tasks: readonly TaskQueryContext[]}
export type HookHTTPResponse = {readonly status: number; readonly headers: Readonly<Record<string, string>>}
export interface RequestDescriptor {responseType?: "json" | "sse"; url: string; method?: string; headers?: Record<string, string>; /** JSON body may contain FilePlaceholder objects at any depth; the host replaces each with a Base64 or data-URL string. */ body?: unknown; credentialless?: boolean; action?: string; model?: string; rewriteModel?: string; bodyType?: "json" | "multipart"; parts?: readonly {name: string; value?: unknown; fileRef?: string; filename?: string}[]}
export interface UpstreamResponse {statusCode: number; headers: Readonly<Record<string, readonly string[]>>; body: unknown}
export interface NormalizedTaskResult {taskId?: string; status: "NOT_START" | "SUBMITTED" | "QUEUED" | "IN_PROGRESS" | "SUCCESS" | "FAILURE" | "UNKNOWN"; progress?: string; reason?: string; url?: string; remoteUrl?: string; completionTokens?: number; totalTokens?: number}
export interface TaskArtifact {key: string; type: "video" | "audio" | "image" | "file"; mimeType?: string}
export declare const meta: Meta;
export declare const native: Record<string, ((ctx: NativeDecodeContext) => TaskIntent) | ((ctx: NativeDecodeContext, task: TaskView | readonly TaskView[]) => unknown)> & {error?: (ctx: NativeDecodeContext, error: {code: string; message: string; httpStatus: number; retryable: boolean}) => unknown};
export declare const protocols: {
  openai_responses?: {decodeRequest(ctx: ProtocolDecodeContext): SubmitIntent; renderEvents?(ctx: unknown, task: TaskView, previousState: unknown): unknown; renderFinal?(ctx: unknown, task: TaskView): unknown};
  openai_video?: {decodeRequest(ctx: ProtocolDecodeContext): SubmitIntent; render(ctx: unknown, task: TaskView): unknown};
  /** render returns the OpenAI ImageResponse; the host adds `created` when absent and resolves response_format b64_json. */
  openai_image?: {decodeRequest(ctx: ProtocolDecodeContext): SubmitIntent; render(ctx: unknown, task: TaskView): {created?: number; data: readonly ImageResponseEntry[]} & Record<string, unknown>};
};
export declare function buildSubmitRequest(ctx: DriverContext): RequestDescriptor;
export interface SubmitEvent {event: string; id: string; data: string}
export type JSONPath = readonly (string | number)[];
export type JSONChange =
  | {op: "set" | "append"; path: JSONPath; value: JSONValue}
  | {op: "appendText"; path: JSONPath; value: string};
export interface SubmitEventDeltaResult {changes: readonly JSONChange[]; state: JSONValue; done: boolean}
/** With submit-sse-delta@1, state is small control data; changes build the separate response body. */
export declare function parseSubmitEventDelta(ctx: DriverContext, event: SubmitEvent, previousState: JSONValue | null): SubmitEventDeltaResult;
export declare function parseSubmitEvent(ctx: DriverContext, event: SubmitEvent, previousState: JSONValue | null): {state: JSONValue; done: boolean};
export declare function parseSubmitResponse(ctx: DriverContext, response: UpstreamResponse): {taskId: string; taskData?: unknown; immediate?: NormalizedTaskResult; state?: unknown};
export declare function buildQueryRequest(ctx: TaskQueryContext): RequestDescriptor;
export declare function buildBatchQueryRequest(ctx: BatchQueryContext, tasks: readonly TaskQueryContext[]): RequestDescriptor;
export declare function parseTaskResult(ctx: TaskQueryContext, body: unknown, response: HookHTTPResponse): NormalizedTaskResult;
export declare function parseBatchResult(ctx: BatchQueryContext, body: unknown, response: HookHTTPResponse): readonly (NormalizedTaskResult & {taskId: string; data?: unknown; state?: unknown})[];
export declare function extractUsage(ctx: DriverContext & {usagePurpose?: "facts" | "billing_ratios"}): Readonly<Record<string, string | number | boolean>> | null;
export declare function extractUsageOnSubmit(ctx: DriverContext, taskData: unknown): Readonly<Record<string, string | number | boolean>> | null;
export declare function extractUsageOnComplete(task: TaskQueryContext, result: NormalizedTaskResult, data: unknown): Readonly<Record<string, string | number | boolean>> | null;
export declare function listArtifacts(task: {taskId: string; status: string; action: string; data: unknown; producerVersion: string}): readonly TaskArtifact[];
export declare function buildContentRequest(ctx: DriverContext & {artifactKey: string; data: unknown; state?: unknown; upstreamTaskId: string; clientRequest: {method: "GET" | "HEAD"; headers: Readonly<Record<string, string>>}}): RequestDescriptor;

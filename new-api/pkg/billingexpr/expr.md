# Billing Expression System (billingexpr)

## Design Philosophy

**One expression, one truth.** A single expression string completely defines a model's billing logic — pricing, tier conditions, cache/image/audio differentiation, time-based discounts, request-aware multipliers — all in one line. No scattered configuration, no implicit rules, no magic numbers.

The expression is the billing contract between the administrator and the system. What you write is what gets executed. The system's job is to evaluate it faithfully, not to interpret it.

### Core Principles

1. **Expression is self-contained** — The expression string alone determines billing. No external ratio tables, no implicit completion multipliers, no hidden conversion factors. Given the same token counts and request context, the same expression always produces the same cost.

2. **Variables are opt-in** — `p` (prompt) and `c` (completion) are the base. Cache (`cr`, `cc`, `cc1h`), image (`img`), and audio (`ai`, `ao`) variables are optional. If omitted, those tokens are included in `p`/`c` and priced at their rate. The system automatically detects which variables the expression uses (via AST introspection) and adjusts token normalization accordingly.

3. **Prices are real prices** — Token coefficients are actual $/1M tokens prices as published by providers. `p * 2.5` means $2.50 per 1M prompt tokens; `fixed(0.01)` means $0.01 per request. No ratio conversion or `/2` convention is required.

4. **Upstream-agnostic** — The expression doesn't need to know whether the upstream API is OpenAI-format (prompt_tokens includes cache) or Claude-format (input_tokens excludes cache). The system normalizes token counts before evaluation based on the upstream response format.

5. **Version-aware** — Expressions carry a version tag (`v1:`, default when omitted). The version controls the compile environment, token normalization, and quota conversion formula, enabling future evolution without breaking existing expressions.

---

## Expression Language

Powered by [expr-lang/expr](https://github.com/expr-lang/expr). Expressions are compiled, cached, and evaluated against a runtime environment.

### Token Variables

**输入侧变量：**

| 变量 | 含义 |
|------|------|
| `p` | 输入 token 数（**计价用**）。**自动排除**表达式中单独计价的子类别（见下方说明） |
| `len` | 输入上下文总长度（**条件判断用**）。不受自动排除影响，始终反映完整输入长度。非 Claude：等于原始 `prompt_tokens`；Claude：等于文本输入 + 缓存读取 + 缓存创建 |
| `cr` | 缓存命中（读取）token 数 |
| `cc` | 缓存创建 token 数（Claude 5分钟 TTL / 通用） |
| `cc1h` | 缓存创建 token 数 — 1小时 TTL（Claude 专用） |
| `img` | 图片输入 token 数 |
| `img_cr` | 图片缓存读取 token 数；只有表达式显式引用且上游提供有效缓存模态明细时才单独拆分 |
| `ai` | 音频输入 token 数 |

**输出侧变量：**

| 变量 | 含义 |
|------|------|
| `c` | 输出 token 数。**自动排除**表达式中单独计价的子类别（见下方说明） |
| `img_o` | 图片输出 token 数 |
| `ao` | 音频输出 token 数 |

#### `p` 和 `c` 的自动排除机制

`p` 和 `c` 是"兜底变量"——它们代表**所有没有被表达式单独定价的 token**。系统会根据表达式实际使用了哪些变量，自动从 `p` / `c` 中减去对应的子类别 token，避免重复计费。

**规则：如果表达式使用了某个子类别变量，对应的 token 就从 `p` 或 `c` 中扣除；如果没使用，那些 token 就留在 `p` 或 `c` 里按基础价格计费。**

> **重要：`len` 不受自动排除影响。** `len` 始终代表完整的输入上下文长度，不管表达式是否单独对缓存/图片/音频定价。因此**阶梯条件应使用 `len` 而非 `p`**，以避免缓存命中导致 `p` 降低而误判档位。

举例说明（假设上游返回的原始数据：prompt_tokens=1000，其中包含 200 cache read、100 image）：

| 表达式 | `p` 的值 | 说明 |
|--------|---------|------|
| `p * 3 + c * 15` | 1000 | 没用 `cr`/`img`，所以缓存和图片都包含在 `p` 里，全按 $3 计费 |
| `p * 3 + c * 15 + cr * 0.3` | 800 | 用了 `cr`，缓存 200 从 `p` 中扣除，按 $0.3 单独计费；图片仍在 `p` 里按 $3 计费 |
| `p * 3 + c * 15 + cr * 0.3 + img * 2` | 700 | 用了 `cr` 和 `img`，都从 `p` 中扣除，各自按自己的价格计费 |

输出侧同理（假设 completion_tokens=500，其中包含 100 audio output）：

| 表达式 | `c` 的值 | 说明 |
|--------|---------|------|
| `p * 3 + c * 15` | 500 | 没用 `ao`，音频输出包含在 `c` 里按 $15 计费 |
| `p * 3 + c * 15 + ao * 50` | 400 | 用了 `ao`，音频 100 从 `c` 中扣除按 $50 计费 |

#### 图片缓存与兼容性

OpenAI 兼容图片接口可报告
`usage.input_tokens_details.cached_tokens_details.{text_tokens,image_tokens,audio_tokens}`。
统一 DTO 保留各字段的缺失与显式零值；复制、用量合并及计费转换不得丢失这些信息。
当前公开 Images API 文档未明确缓存模态字段，兼容结构的合成测试不能替代真实上游缓存回包验证。

当表达式使用 `img_cr` 且明细有效时，`img_cr` 从 `cr` 和 `img` 中同时移出，
再按表达式引用的变量从 `p` 中扣除各互不重叠的类别。`len` 保持原始输入总长度。
例如总输入 1000、图片输入 600、缓存总量 300、图片缓存 200：
`p * 5 + cr * 1.25 + img * 8 + img_cr * 2 + c * 30` 对应
`p=300, cr=100, img=400, img_cr=200`；输出 100 时费用为 $0.008225。

缺少图片缓存明细时，`img_cr=0`，缓存总量继续进入 `cr`，沿用原有通用缓存计费，
不推测图片缓存比例。不引用 `img_cr` 的既有表达式保持原有归一化行为。
负数、超过总量或无法形成有效输入集合的明细会记录诊断并回退原有逻辑。
Anthropic 的独立缓存计数语义不采用这一 OpenAI 图片拆分。

图片缓存表达式的消费日志保存 `image_cache_tokens` 与 `billing_tokens`，后者由结算结果携带，
通过公共日志注入记录实际参与结算的计价用量，覆盖文本、audio 和 realtime 入口。
求值失败回退预扣时不伪造实际用量；固定价格和未引用 `img_cr` 的表达式不新增这些字段。
文本/图片日志的 `cache_tokens` 继续保留上游缓存总量。Images 的 `output_tokens` 是图片输出，
内置图片价格使用 `c`，无需上游额外报告 `output_tokens_details.image_tokens`。

### 旧定价转换

旧倍率和旧按次模式仍可运行，管理界面标记为弃用，新建定价默认使用表达式。
`POST /api/option/model_pricing/convert` 接收 `{model_name, pricing}`，其中 `pricing`
为完整的旧定价草稿，缺少的键继承运行时默认值。接口返回表达式及本次转换使用的生效定价快照，或具体不支持原因，不写数据库。
管理界面先用该快照显示左右对照预览，确认后才更新草稿；取消不改变原草稿。
保存仍使用现有 `PATCH /api/option/model_pricing` 及 `expected_version` 冲突检查。

倍率转换以 `ModelRatio * 1000000 / QuotaPerUnit` 得到 USD/百万 tokens 的基础单价，
保留输出、缓存读取、适用的缓存创建和图片输入的生效倍率及显式零值。
常规按次价格转换为 `tier("request", fixed(price))`。旧价格字段继续保存，供切回旧模式使用。
转换后的预扣及舍入采用表达式规则，不保证旧模式每笔舍入结果完全一致。
迁移、已保存价格展示与草稿价格预览共用生效价格解析：输出使用模型族的硬编码兜底/强制倍率；
缺少配置的缓存读取、缓存创建、图片输入分别使用运行时的 1、1.25、1 倍。
图片倍率为 1 时，转换不生成独立 `img` 项，图片输入继续包含在 `p` 中按相同价格计费；
图片倍率为其他值（包括显式 0）时才生成 `img` 项。
缓存读取倍率为 1 时在价格预览中合并为输入价格；没有独立图片输入或缓存写入项时，转换同时省略 `cr`。
存在这些独立项目时，缓存计数可能交叠，输入余量的零下限使直接合并不再等价，因此保留原 `cr` 计算项而不单列同价价格。
基础输入价为 0 时可直接省略倍率为 1 的 `cr`；其他缓存读取倍率（包括显式 0）保留独立项。
省略 `cr` 时，Anthropic 用量中单独报告的缓存读取数会加回 `p`，而 `len` 保持完整上下文长度。
缓存创建按对外计费名和原始配置处理：名称包含 `claude`（不区分大小写）时保留 `cc` 和 `cc1h`，
其中 1h 价格沿用旧引擎的 `CreateCacheRatio * 6 / 3.75`；其他名称仅在草稿已配置 `CreateCacheRatio`
时生成通用 `cc`（包括显式 0），不生成 `cc1h`。没有配置的通用 1.25 倍兜底不产生新计费项。
不按渠道映射或模型白名单扩展缓存类型，也不要求管理员另选类型。
定价快照条目、草稿预览响应和转换结果附带只读 `cache_write_mode`（`none` / `standard` / `claude_ttl`），
用于统一展示。`POST /api/option/model_pricing/preview` 的响应统一为
`{success, message, data: {effective, cache_write_mode, billing_details}}`，调用者从 `data.effective`
读取生效价格；转换接口和快照条目的 JSON 结构不变。元信息不保存、不参与表达式执行。
未设置与显式 0 必须区分。草稿关闭某项覆盖后按保存后的回退规则解析，不能继续读取运行进程中尚未被保存替换的旧值。
图片和普通音频规则均可自动迁移，具体规则如下；任务插件、视频、Realtime，以及依赖上游 cost
反推缓存写入用量的 OpenRouter Claude 分支仍返回具体原因，不按渠道类型或未知端点一概拦截。

Gemini 音频输入使用旧结算实际采用的美元单价生成 `ai`，不乘普通输入倍率，因此普通输入价为零时
仍保留独立音频费用。普通音频的 `ai` 单价为基础输入价乘 AudioRatio，`ao` 再乘 AudioCompletionRatio，
缺省倍率按 1 处理，显式零保留。普通音频结算不使用缓存/图片倍率；模型同时配置这些价格时，
转换生成音频请求与纯文本请求两个分支。音频分支使用 `max(len - ai, 0)` 计量普通输入，避免纯文本
分支中引用的缓存变量改变音频费用。Gemini 文本结算仍保留缓存/音频交叠后的输入余量零下限。
只读 `billing_details` 为预览提供实际音频单价、图片数量和请求倍率规则，不成为新的持久化价格来源。
同一计费名在 Gemini/兼容音频入口存在相互冲突的单价时，
转换返回该具体冲突，不能静默选择其中一条规则覆盖其他入口。

图片按次价格转换为 `tier("image", fixed(price)) * image_count`，再追加旧尺寸、质量条件倍率。
DALL·E 尺寸/质量规则按原始请求模型名生成，模型映射不会额外引入这些规则。
OpenAI 已于 2026-05-12 下线 DALL·E 2/3；其校验、默认值和倍率保留在独立 legacy 文件，
仅用于历史配置及兼容上游。转换从 DTO 的旧计费逻辑获取倍率，不维护第二份价格表。
图片 token 定价不额外乘数量。所有金额和倍率固化在表达式中，不再叠加旧 OtherRatios。

### Image Quantity

`image_count` 是独立于 token 的计费数量。省略上下文时默认 1；提供的数量必须是 1 到
`dto.MaxImageN`（128）的整数。固定价格语法允许它作为乘数，仍禁止用 token 乘固定价格。
图片入口只解析一次 provider 计费标量并随请求携带。数量来自顶层 `n`（`n=0` 兼容为 1）；
`parameters.n` 仍在入口校验范围，但不再覆盖计费数量。每次渠道重试或参数覆盖之后，再校验最终上游数量并在发送前补足预留，
图片钱包预留使用原子余额检查，不能通过增加数量形成欠费后继续提交。
`estimated_image_count` 保存本次发送前的数量；JSON/multipart 图片参数上下文只保留计费需要的标量，不保留图片或提示词内容。
结算使用独立的实际数量，不修改被冻结的 `param("n")`。正常结束
的 SSE 可以减少数量，客户端提前断开不能减少应收数量。最终消费日志记录 `image_count`。
阿里百炼图片模型（万相、Qwen-Image、Z-Image）由 alibaba 任务插件通过 `openai_image` 宿主协议承接：
其数量是任务用量事实 `u("image_count")`，提交时按请求数量预留，完成时按上游 `usage.image_count`
（Qwen-Image-3.0 为 `usage.output_image_count`）或按各 `choices[].message.content[]` / `results[]`
中的图片载荷数结算，任务消费日志同样记录 `image_count`。

> **注意：** 自动扣除针对 GPT/OpenAI 格式的 API（prompt_tokens 包含子类别）。Claude 格式的 API 不重复扣除缓存；未独立计价的缓存读取加回输入。系统根据上游返回格式自动处理。

### Built-in Functions

| Function | Signature | Purpose |
|----------|-----------|---------|
| `tier` | `tier(name, value) → float64` | Records which pricing tier matched; must wrap the cost expression |
| `fixed` | `fixed(amount) → float64` | A USD price per request, used only as the complete price in `tier(name, fixed(amount))` |
| `param` | `param(path) → any` | Reads a JSON path from the request body (uses gjson) |
| `header` | `header(key) → string` | Reads a request header value |
| `has` | `has(source, substr) → bool` | Substring check |
| `hour` | `hour(tz) → int` | Current hour in timezone (0-23) |
| `minute` | `minute(tz) → int` | Current minute (0-59) |
| `weekday` | `weekday(tz) → int` | Day of week (0=Sunday, 6=Saturday) |
| `month` | `month(tz) → int` | Month (1-12) |
| `day` | `day(tz) → int` | Day of month (1-31) |
| `max` | `max(a, b) → float64` | Math max |
| `min` | `min(a, b) → float64` | Math min |
| `abs` | `abs(x) → float64` | Absolute value |
| `ceil` | `ceil(x) → float64` | Ceiling |
| `floor` | `floor(x) → float64` | Floor |

### Expression Examples

```
# Simple flat pricing
tier("base", p * 2.5 + c * 15 + cr * 0.25)

# Conditional per-request pricing, with token pricing for the fallback
len <= 32000
  ? tier("short", fixed(0.01))
  : tier("long", p * 2 + c * 8)

# Multi-tier (Claude Sonnet style) — use len for tier conditions
len <= 200000
  ? tier("standard", p * 3 + c * 15 + cr * 0.3 + cc * 3.75 + cc1h * 6)
  : tier("long_context", p * 6 + c * 22.5 + cr * 0.6 + cc * 7.5 + cc1h * 12)

# Image model (no separate cache/audio pricing — those tokens stay in p/c)
tier("base", p * 2 + c * 8 + img * 2.5)

# Multimodal with audio
tier("base", p * 0.43 + c * 3.06 + img * 0.78 + ai * 3.81 + ao * 15.11)
```

### Fixed Request Prices

`tier("request", fixed(0.01))` replaces all token charges in the selected leaf
with a $0.01 base price for one successful HTTP/SSE request. Other leaves may
still use token pricing. Group ratios and request multipliers continue to apply;
existing tool surcharges are calculated separately and added as before. A stream
does not incur a fixed fee per chunk. Realtime and task usage expressions reject
`fixed()` before upstream submission or reservation.

The amount must be a finite, non-negative numeric literal whose v1 scaled value
is finite. Explicit `fixed(0)` is valid and stays free. Expressions using `fixed`
must consist of a conditional pricing tree and the standard request multiplier
factors described below, with each leaf wrapped in `tier()`. Adding fixed and
token charges in one leaf, adding multiple tiers together, or multiplying a
fixed price by tokens is rejected. Validation examines the original AST,
including branches that optimization or short-circuiting would skip. Existing
expressions without `fixed()` retain their original grammar and behavior.

`fixed(amount)` returns `amount * 1,000,000` internally, preserving v1's existing
quota conversion and rounding. Pre-consume matches conditions using estimates;
settlement reevaluates against actual or existing locally estimated usage and
reconciles any change of branch through the normal billing session. Missing
upstream usage retains the existing estimation path; an actual request-priced
branch can charge even with zero tokens. Failures retain the normal refund
policy. Settlement evaluation errors retain the reservation and its estimated
billing unit.

Evaluation results expose `billing_unit` (`token` or `request`) and, for a
request-priced leaf, `fixed_price` in USD before multipliers. Pre-consume snapshots
carry `estimated_billing_unit` and `estimated_fixed_price`; consume logs append
the actual values to `other`. An explicit zero price is present in these fields.
Older snapshots and logs need no migration. The expression remains the sole
pricing configuration.

### Request Rules (appended after `|||`)

Request-conditional multipliers are appended to the expression after a `|||` separator:

```
tier("base", p * 5 + c * 25)|||when(header("anthropic-beta") has "fast-mode") * 6
```

These factors are stored as ordinary multiplication in the final expression (for example, `(tier(...)) * (condition ? 6 : 1)`) and run in the same billing program.

### Request Rule Tracing

At compile time, the engine instruments ternary factors with this exact shape:

```
<request-probe condition> ? <numeric literal> : 1
```

The condition must reference at least one request probe (`param`, `header`, `hour`, `minute`, `weekday`, `month`, or `day`). Both branches must be numeric literals and the fallback must equal `1`. Other conditionals, including `(condition ? 2 : 1.5)`, are evaluated normally but are not traced. Integer-only factors use an integer-preserving trace callback, so instrumentation does not change expressions that require an integer operand (for example, `%`). The internal trace callback names are reserved and cannot be used in stored expressions.

The compiled cache stores the canonical condition and multiplier for every instrumented node. Each run starts with the full detected rule list marked as unmatched; callbacks mark rules that actually evaluate true. Rules skipped by normal expression short-circuiting remain unmatched. This keeps the expression's numeric result unchanged and avoids reparsing it on each request.

Settlement copies the actual run's traces into the consume log as:

```json
{
  "request_rules": [
    { "cond": "param(\"service_tier\") == \"fast\"", "multiplier": 2, "matched": true }
  ]
}
```

The usage-log UI treats `request_rules` as the authoritative rule list and renders directly from it. It parses `cond` only to produce a friendly label and falls back to the canonical condition text when that parser does not recognize the condition. Pricing pages without log context continue to parse the stored expression for display.

---

## Task Usage Expressions

Task plugins can expose validated, provider-specific billing facts through
`meta.usageSchema`. Expressions read those facts with `u("key")`. A literal key
must be declared by the plugin schema before the expression can be saved.

Multiple plugins may serve the same model with different usage schemas. The
executing channel identifies the plugin; clients do not select a billing
provider. Task expression resolution uses this order:

1. `billing_setting.plugin_billing_expr["<pluginKey>::<clientModel>"]`
2. The same plugin's override for the final mapped model, when present
3. The client model's expression when its billing mode is `tiered_expr`
4. The final mapped model's expression when the client model has no expression
   billing mode

Plugin overrides always use task-usage semantics, independently of the model's
billing mode. The flat override map is a separate option and never contributes
composite keys to model names or model-price synchronization exports. Existing
prices are not rewritten. The billing snapshot freezes the selected expression;
completion uses that snapshot even if provider pricing later changes.

A model-level expression save smoke-tests each candidate's selected schema,
skipping candidates with their own override in the same complete draft.
Provider expression saves validate the plugin/model binding and that plugin's
schema; errors identify both model and plugin. For a shared model, an effective
expression referencing an undeclared literal `u()` key is unconfigured for that
provider, including references in branches skipped by the current request.
Submission rejects it with 400 `model_price_error`; administration and public
pricing expose the unconfigured state. Public model-level metadata remains the
first plugin's view in ascending key order, with provider variants added
separately.

Saved provider overrides remain visible when only one provider remains.
Administration exposes unavailable plugin/model bindings as `stale` and permits
preserving or removing their existing expressions. Changes to those expressions
still require a valid binding; writes compare against the locked database
snapshot rather than the process cache. No overrides are silently pruned.
Public provider variants include their effective `billing_mode` so inherited
ratio/per-call pricing is distinct from an unconfigured usage expression.

Plugins serving different model families can optionally declare
`meta.usageProfiles: [{models: ["image-model"], schema: {...}, examples: [...]}]`.
Each profile provides the complete usage schema and optional display examples
for its declared models. It replaces, rather than merges with, the plugin's
default `usageSchema` and `usageExamples`. A model may belong to only one
profile, and profile model names must use the spelling declared in `meta.models`.
An empty profile schema is allowed; a missing or null schema is not. Token-unit
profiles require their own examples, using the same validation as the defaults.

The host resolves model aliases before selecting usage metadata. Model-level
pricing and expression-save APIs use the declared target; an ambiguous alias
with multiple targets in the same plugin retains the plugin defaults. Request
and usage validation use the executing plugin and final upstream model. Polling
selects metadata from each saved task's model, including in mixed-model batches.
An upstream name that no profile declares, such as a channel mapping to a vendor
endpoint ID, falls back to the client-facing model's profile. Unmatched models
and plugins without profiles retain the plugin defaults.

Profiles do not introduce a request-body whitelist or remove legacy multiplier
extensions. Numeric hook facts retain a consistent floating-point representation
for expression evaluation, including fields outside the selected profile.
Saving an expression checks literal usage keys against the selected schema;
already stored expressions are not migrated. Single-plugin submissions retain
the existing key-check behavior; shared models enforce the compatibility rule
above. Existing runtime error and quota safety checks still apply.
Completion continues to evaluate the frozen expression after overlaying measured
facts; no schema snapshot or database migration is introduced.

This is an optional addition to plugin API version 1. New hosts accept existing
plugins unchanged. Older hosts reject the new metadata field, so upgrade the
host before installing a plugin that declares profiles.

Numeric facts are finite, non-negative values in the declared canonical unit
(`second`, `count`, `token`, or `credit`); enum facts are exact strings from the
declared value list. The schema description is display-only metadata and never
affects evaluation. `token` is the host unit for upstream billing tokens (for
example doubao `usage.completion_tokens`). `credit` is the host unit for vendor
resource-pack units (for example kling `final_unit_deduction`). Both share the
int32 quota bound, not the 3600-second / 128-count limits.

Task usage billing has a deliberately different conversion rule from token
billing:

```
task quota = expression output in USD * QuotaPerUnit * groupRatio
token quota = expression output in $/1M tokens / 1,000,000 * QuotaPerUnit * groupRatio
```

In other words, a task expression already returns the request's dollar cost.
For example, `u("seconds") * 0.4` means $0.40 per second. Engine semantics do
not divide task output by one million.

The visual editor generates, and public pricing displays recognize, exactly
these canonical task shapes. Expressions outside these shapes remain valid in
raw mode but fall back to the special-expression display:

```
# Flat unit pricing
tier("base", u("seconds") * 0.4)

# Enum tiers (conditions may combine enum comparisons with &&)
u("mode") == "pro"
  ? tier("pro", u("seconds") * 0.8)
  : tier("std", u("seconds") * 0.4)

# Optional constant plus multiple numeric usage terms
tier("base", 0.1 + u("seconds") * 0.4 + u("clips") * 0.05)

# Upstream token overlay (doubao Seedance tokens)
# The editor takes a $/1M token input and emits the / 1000000 literal.
# Engine semantics are unchanged: the expression still returns USD.
tier("base", u("tokens") * 9.8 / 1000000)

# Vendor credit overlay (kling resource-pack units)
# The coefficient is the real $/credit price; no /1M scale.
tier("base", u("units") * 0.14)
```

The tier body is an optional non-negative constant plus one or more
`u("<number field>") * <unit price>` terms. Token-unit fields use the
canonical scaled shape `u("<field>") * <dollars per 1M tokens> / 1000000`.
Credit, second, and count fields keep the bare `u("<field>") * <unit price>`
shape. Tier conditions are equality checks
between declared enum fields and values, optionally joined by `&&`, with
chained ternaries following the same ordering rules as token tiers. Numeric
range tiers are not part of the current canonical shape. Request rules after
`|||` remain orthogonal and use the same syntax as token expressions.

Before saving, the host compiles every expression, rejects literal `u()` keys
that the selected task plugin did not declare, and smoke-tests usage vectors.
Every numeric field is exercised at 0, 1, and its host-owned unit ceiling
(`second` 3600, `count` 128, `token`/`credit` int32 max). Enum values are exercised as a
Cartesian product. Smoke vectors are capped at
64, reducing oversized enum dimensions to their first and last values. Every
evaluated result must be finite and non-negative.

Submission evaluates the expression with request-derived usage facts and
freezes both the expression and those facts in the billing snapshot. On
completion, the host overlays completion facts on the frozen submission facts
key by key, so measured values replace estimates while facts omitted by the
completion hook retain their submission values. The same expression is then
evaluated again; a changed fact can therefore produce a settlement delta and a
different matched tier. Evaluation failure keeps the pre-consumed charge.

---

## Architecture

### Data Flow

```
Frontend Editor → Storage → Pre-consume → Settlement → Log Display
```

### 1. Frontend Editor

**File**: `web/src/pages/Setting/Ratio/components/TieredPricingEditor.jsx`

Two editing modes:
- **Visual mode**: Fill in prices per variable, conditions per tier. Generates expression via `generateExprFromVisualConfig()`.
- **Raw mode**: Edit the expression string directly. Includes preset templates for common models.

The editor outputs a billing expression string and an optional request rule expression string. These are combined via `combineBillingExpr(billingExpr, requestRuleExpr)` before storage.

### 2. Storage

**File**: `setting/billing_setting/tiered_billing.go`

Two option maps stored in the `options` DB table:
- `ModelBillingMode`: `{ "model-name": "tiered_expr" }` — activates tiered billing for a model
- `ModelBillingExpr`: `{ "model-name": "tier(\"base\", p * 2.5 + c * 15)" }` — the expression

On save, the expression is validated:
1. Compiled via `billingexpr.CompileFromCache()` — syntax check
2. Smoke-tested with sample token vectors — ensures non-negative results

### 3. Pre-consume (Quota Estimation)

**File**: `relay/helper/price.go` → `modelPriceHelperTiered()`

When a request arrives and the model uses `tiered_expr` billing:
1. Loads expression from `billing_setting.GetBillingExpr()`
2. Builds `RequestInput` (headers + body) for `param()` / `header()` functions
3. Runs the expression with locally estimated input tokens (`P` and `Len`) and zero output tokens (`C=0`). Client output limits do not affect the reservation. If request token estimation is disabled, the estimated input is zero.
4. Converts output to quota: `rawCost / 1,000,000 * QuotaPerUnit`. For token-priced leaves, multiplies this reservation by `quota_setting.pre_consume_multiplier` (default 1; any finite positive decimal). The multiplier does not change context-length tier selection, actual settlement, or request-priced leaves. The snapshot freezes the multiplier for image quantity retries; group retries reuse the multiplied reservation. Older snapshots with no multiplier retain a factor of 1.
5. Creates `BillingSnapshot` and stores it on `RelayInfo`. Expression and request state stay frozen for settlement. An auto-group retry refreshes group-dependent fields from the selected group before the next upstream attempt. If a free initial group skipped pre-consume and the retry selects a paid group, the billing session is created before that attempt. If an existing session moves to a more expensive group, its reservation is raised to that group's estimate before sending; cheaper groups are refunded only after actual usage is settled.

Wallet trust bypass uses `quota_setting.trust_quota_usd` (default 10 USD; 0 disables bypass). Both the wallet and any limited API token must exceed the threshold. The wallet must still cover the estimated reservation before bypass is considered. Subscriptions and asynchronous tasks do not use this bypass. Legacy token-ratio pricing also reserves estimated input cost times the multiplier, without the former `PreConsumedQuota` floor or output-token estimate. Task usage pricing is unchanged.

### 4. Settlement (Actual Billing)

**Files**: `service/tiered_settle.go`, `pkg/billingexpr/settle.go`

After the upstream response returns with actual token usage:

1. `BuildTieredTokenParams(usage, isClaudeUsageSemantic, usedVars)`:
   - Reads actual token counts from `dto.Usage`
   - For GPT-format APIs (prompt_tokens includes everything): subtracts sub-categories from P/C **only when** the expression uses their variables (detected via AST introspection of the compiled expression)
   - For Claude-format APIs: cache reads omitted from the expression are added to input; separately priced cache remains separate

2. `TryTieredSettle(relayInfo, params)`:
   - Uses the captured `BillingSnapshot`, whose group-dependent fields have been refreshed from the final selected group
   - Re-runs the expression with actual token counts
   - Converts via `quotaConversion()` (version-dispatched)
   - Returns actual quota

### 5. Log Display

**Files**: `service/log_info_generate.go`, `web/src/helpers/render.jsx`

Backend: `InjectTieredBillingInfo()` adds `billing_mode`, `expr_b64` (base64 expression), `matched_tier`, and the structured `request_rules` trace list to the log's `other` JSON.

Frontend: Detects `billing_mode === "tiered_expr"`, decodes `expr_b64`, parses tiers via shared `parseTiersFromExpr()`, and renders request multipliers from `request_rules` when present. Without log traces, it falls back to parsing the stored expression.

---

## Key Design Decisions

### Token Normalization via AST Introspection

Different upstream APIs report `prompt_tokens` differently:
- **OpenAI/GPT**: `prompt_tokens` = total (text + cache + image + audio)
- **Claude**: `input_tokens` = text only (cache reported separately)

The system normalizes `p` to mean "tokens not separately priced" by subtracting sub-categories **only when the expression references them**. This is determined by walking the compiled AST to find `IdentifierNode` references — zero runtime cost after first compilation (cached).

Example: `p * 2.5 + c * 15 + cr * 0.25`
- Expression uses `cr` → cache read tokens subtracted from `p`
- Expression doesn't use `img` → image tokens stay in `p`, priced at $2.50

### `len` — Context Length Variable

`len` represents the total input context length, designed for **tier condition evaluation** (e.g. `len <= 200000 ? ...`). Unlike `p`, `len` is never reduced by sub-category exclusion.

**Computation rules:**
- **Non-Claude (GPT/OpenAI format)**: `len = prompt_tokens` (the raw total from the upstream response)
- **Claude format**: `len = input_tokens + cache_read_tokens + cache_creation_tokens` (since Claude's `input_tokens` is text-only, cache must be added back to reflect full context length)

This ensures that heavy cache usage doesn't cause the tier condition to incorrectly evaluate to a lower tier. For example, if a request has 300K total context but 250K is cached, `p` with cache subtracted would be only 50K (standard tier), while `len` correctly reports 300K (long-context tier).

### Quota Conversion

Expression coefficients are $/1M tokens. Conversion to internal quota:

```
quota = exprOutput / 1,000,000 * QuotaPerUnit * groupRatio
```

This matches the per-call billing pattern: `quota = modelPrice * QuotaPerUnit * groupRatio`.

### Expression Versioning

Expressions can carry a version prefix: `v1:tier(...)`. No prefix = v1.

Version controls:
- Compile environment (available variables and functions)
- Token normalization logic
- Quota conversion formula

This enables future evolution without breaking existing expressions.

---

## File Map

| Layer | Files |
|-------|-------|
| Expression engine | `pkg/billingexpr/compile.go`, `run.go`, `settle.go`, `round.go`, `types.go` |
| Storage | `setting/billing_setting/tiered_billing.go` |
| Pre-consume | `relay/helper/price.go`, `relay/helper/billing_expr_request.go` |
| Settlement | `service/tiered_settle.go`, `service/quota.go` |
| Log injection | `service/log_info_generate.go` |
| Frontend editor | `web/src/pages/Setting/Ratio/components/TieredPricingEditor.jsx` |
| Frontend display | `web/src/helpers/render.jsx`, `web/src/helpers/utils.jsx` |
| Model detail | `web/src/components/table/model-pricing/modal/components/DynamicPricingBreakdown.jsx` |
| Log display | `web/src/hooks/usage-logs/useUsageLogsData.jsx`, `web/src/components/table/usage-logs/UsageLogsColumnDefs.jsx` |

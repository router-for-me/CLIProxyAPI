/**
 * @file copilot_handler.js
 * @description 这是一个通用的 JavaScript 拦截处理器脚本，用于在请求被发送至上游前进行请求体清洗以及在响应返回客户端前进行日志记录和修改。
 * 
 * 使用方式：
 * 在配置文件 (如 config.yaml) 的 payload 属性中进行注册：
 * ```yaml
 * payload:
 *   js-handler:
 *     - models:
 *         - name: "gemini-*" # 匹配所有模型请求
 *       params:
 *         - "./examples/js-handler/copilot_handler.js" # 脚本文件相对路径
 * ```
 */

/**
 * 递归清理上游 AI 不支持的非标属性（如 $comment 和 enumDescriptions），防止接口报错 400 失败
 * @param {Object} obj - 待处理的 JSON 对象或数组
 */
function remove_unsupported_fields(obj) {
    if (obj === null || typeof obj !== 'object') {
        return;
    }
    if (Array.isArray(obj)) {
        for (let i = 0; i < obj.length; i++) {
            remove_unsupported_fields(obj[i]);
        }
        return;
    }
    // 自动删除上游不支持的非标字段
    if (obj.hasOwnProperty('$comment')) {
        delete obj['$comment'];
    }
    if (obj.hasOwnProperty('enumDescriptions')) {
        delete obj['enumDescriptions'];
    }
    // 递归处理所有下层属性
    for (let key in obj) {
        if (obj.hasOwnProperty(key)) {
            remove_unsupported_fields(obj[key]);
        }
    }
}

/**
 * 请求前置拦截钩子：在请求被转发给上游服务前触发。可以修改请求体 body 和请求头 headers。
 * 
 * ctx (请求上下文) 的结构说明：
 * {
 *   "id": "uuid-or-client-request-id", // 请求的唯一标识 Request ID
 *   "body": "...",                      // 请求体载荷（Payload）的原始 JSON/字符串
 *   "headers": {                       // 请求头对象（包含原始和全小写双重映射）
 *     "Authorization": "Bearer ...",
 *     "authorization": "Bearer ...",
 *     "content-type": "application/json"
 *   },
 *   "url": "",                         // 请求的 URL（占位）
 *   "model": "gemini-3-flash-preview", // 当前解析的底层模型名称
 *   "protocol": "gemini"               // 转换所使用的协议
 * }
 * 
 * @param {Object} ctx - 请求上下文对象
 * @returns {Object} 修改后的 ctx 对象
 */
function on_before_request(ctx) {
    console.log("[" + ctx.id + "] 正在拦截模型 [" + ctx.model + "] 的请求前载荷，协议: " + ctx.protocol);

    try {
        let req = JSON.parse(ctx.body);

        // 自动清洗上游不支持的字段以规避 400 报错
        remove_unsupported_fields(req);

        // 示例：动态调整 temperature
        if (req.temperature !== undefined) {
            req.temperature = 0.7;
            console.log("[" + ctx.id + "] 请求参数 temperature 已动态调整为 0.7");
        }

        // 示例：遍历消息内容，对特定敏感词进行替换过滤
        if (req.messages && req.messages.length > 0) {
            for (let i = 0; i < req.messages.length; i++) {
                if (req.messages[i].content && typeof req.messages[i].content === 'string') {
                    // 替换示例：把 "敏感词" 替换为 "安全词"
                    req.messages[i].content = req.messages[i].content.replace("敏感词", "安全词");
                }
            }
        }

        ctx.body = JSON.stringify(req);
    } catch (e) {
        console.log("[" + ctx.id + "] 解析请求 JSON 失败，跳过载荷修改: " + e.message);
    }

    return ctx;
}

/**
 * 响应后置拦截钩子：在上游返回数据、客户端接收响应前触发。支持非流式和流式分块响应。
 * 
 * ctx (响应上下文) 的结构说明：
 * {
 *   "id": "uuid-or-client-request-id", // 请求的唯一标识 Request ID
 *   "body": "..." | null,              // 响应体。非流式下为完整响应体字符串，流式响应下固定为 null
 *   "chunk": "..." | null,             // 当前分块。非流式下为 null，流式下为当前分块的可读写字符串（JS 可直接对其修改）
 *   "history_chunks": string[] | null, // 历史分块数组。非流式下为 null，流式响应下为之前所有已修改后的历史分块字符串数组（只读，且被 VM 引擎冻结无法篡改）
 *   "protocol": "openai",              // 所使用的协议
 *   "req": {                           // 原请求上下文
 *     "body": "...",                    // 原请求体
 *     "headers": {                      // 原请求头（包含双重大小写映射）
 *       "content-type": "application/json"
 *     },
 *     "url": ""
 *   },
 *   "headers": {                      // 上游服务返回的响应头对象（直接挂载在根上，包含双重大小写映射）
 *     "content-type": "application/json"
 *   }
 * }
 * 
 * @param {Object} ctx - 响应上下文对象
 * @returns {Object} 修改后的 ctx 对象
 */
function on_after_response(ctx) {
    // 自动注入 GitHub Copilot 所需的响应头，兼容新旧版本的 headers 字段挂载方式
/*     let headers = ctx.headers || (ctx.resp && ctx.resp.headers);
    if (headers) {
        if (!headers["x-request-id"]) {
            let requestId = (ctx.req && ctx.req.headers && ctx.req.headers["x-request-id"]) || ctx.id;
            headers["x-request-id"] = requestId;
            headers["x-github-request-id"] = requestId;
            console.log("[" + ctx.id + "] 成功在响应头中注入 x-request-id: " + requestId);
        }
    } */
    
    // 1. 流式响应分块处理 (当 ctx.chunk 不为 null/undefined 且不为 "" 时)
    if (ctx.chunk !== undefined && ctx.chunk !== null && ctx.chunk !== "") {
        let json_str = ctx.chunk.trim();

        try {
            let obj = JSON.parse(json_str);
            if (obj.choices && obj.choices.length > 0) {
                let choice = obj.choices[0];
                // 检查 delta 中是否包含工具调用列表
                let has_tool_calls = choice.delta && choice.delta.tool_calls && choice.delta.tool_calls.length > 0;

                if (has_tool_calls) {
                    // 如果当前分块包含工具调用，为了防止客户端在第一个工具块到达时就提前终止状态机，强制把 finish_reason 改为 null
                    if (choice.finish_reason !== null) {
                        console.log("[" + ctx.id + "] 工具调用分块发现有 finish_reason = [" + choice.finish_reason + "]，强制重置为 null，当前工具索引: " + choice.delta.tool_calls[0].index);
                        choice.finish_reason = null;
                        
                        // 重新还原组装回原始格式
                        ctx.chunk = JSON.stringify(obj);
                    }
                } else {
                    // 如果当前分块不包含工具调用，检查已发生的历史流分块中是否有过工具调用
                    let history_had_tool_calls = false;
                    if (ctx.history_chunks && ctx.history_chunks.length > 0) {
                        for (let i = 0; i < ctx.history_chunks.length; i++) {
                            let h_json = ctx.history_chunks[i].trim();
                            try {
                                let hist_obj = JSON.parse(h_json);
                                if (hist_obj.choices && hist_obj.choices.length > 0) {
                                    let h_choice = hist_obj.choices[0];
                                    if (h_choice.delta && h_choice.delta.tool_calls && h_choice.delta.tool_calls.length > 0) {
                                        history_had_tool_calls = true;
                                        break;
                                    }
                                }
                            } catch (err) {
                                // 忽略历史脏数据的解析错误
                            }
                        }
                    }

                    // 如果历史分块包含过工具调用，说明当前是不含工具调用的结束帧/统计帧，必须注入 finish_reason = "tool_calls" 唤醒客户端
                    if (history_had_tool_calls) {
                        console.log("[" + ctx.id + "] 监测到历史包含工具调用，且当前帧为结束收尾帧，修改 finish_reason 从 [" + choice.finish_reason + "] 为 [tool_calls]");
                        choice.finish_reason = "tool_calls";
                        
                        // 重新还原组装回原始格式
                        ctx.chunk = JSON.stringify(obj);
                    }
                }
            }
        } catch (e) {
            console.log("[" + ctx.id + "] 尝试解析流式响应 JSON 分块失败: " + e.message + " | 分块内容: " + ctx.chunk);
        }

        console.log("[" + ctx.id + "] 收到流式响应分块。当前已发流长度: " + (ctx.body ? ctx.body.length : 0) + " | 分块内容: " + ctx.chunk);
        return ctx;
    }
    
    // 2. 常规响应处理 (非流式响应)
    console.log("[" + ctx.id + "] 收到非流式响应。响应内容: " + ctx.body);
    return ctx;
}

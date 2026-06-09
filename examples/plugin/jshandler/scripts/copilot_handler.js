function on_before_request(ctx) {
    try {
        var req = JSON.parse(ctx.body);
        console.log("[" + ctx.id + "] message: " + ctx.body);
        if (req.messages) {
            for (var i = 0; i < req.messages.length; i++) {
                if (typeof req.messages[i].content === "string") {
                    req.messages[i].content = req.messages[i].content.replace("sensitive_word", "safe_word");
                }
            }
        }
        ctx.body = JSON.stringify(req);
        console.log("[" + ctx.id + "] message: " + ctx.body);
    } catch (e) {
        console.log("[" + ctx.id + "] Failed to parse request JSON, skipping payload modification: " + e.message);
    }
    return ctx;
}

function parse_stream_chunk(chunk) {
    var leading = "";
    var payload = chunk.trim();
    var trailing = "";

    var dataIndex = chunk.indexOf("data:");
    if (dataIndex >= 0) {
        leading = chunk.substring(0, dataIndex) + "data:";
        var afterData = chunk.substring(dataIndex + 5);
        var newlineIndex = afterData.indexOf("\n");
        if (newlineIndex >= 0) {
            payload = afterData.substring(0, newlineIndex).trim();
            trailing = afterData.substring(newlineIndex);
        } else {
            payload = afterData.trim();
        }
    }

    if (payload === "" || payload === "[DONE]") {
        return null;
    }

    return {
        obj: JSON.parse(payload),
        leading: leading,
        trailing: trailing
    };
}

function stringify_stream_chunk(parsed) {
    if (parsed.leading !== "") {
        return parsed.leading + " " + JSON.stringify(parsed.obj) + parsed.trailing;
    }
    return JSON.stringify(parsed.obj);
}

function on_after_stream_response(ctx) {
    console.log("[" + ctx.id + "] Received response with status: " + ctx.status);
    if (ctx.chunk === undefined || ctx.chunk === null || ctx.chunk === "") {
        return ctx;
    }

    try {
        var parsed = parse_stream_chunk(ctx.chunk);
        if (parsed === null) {
            return ctx;
        }
        var obj = parsed.obj;
        if (obj.choices && obj.choices.length > 0) {
            var choice = obj.choices[0];
            var has_tool_calls = choice.delta && choice.delta.tool_calls && choice.delta.tool_calls.length > 0;

            if (has_tool_calls) {
                if (choice.finish_reason !== null) {
                    console.log("[" + ctx.id + "] Tool call chunk has finish_reason = [" + choice.finish_reason + "], forcing reset to null, tool index: " + choice.delta.tool_calls[0].index);
                    choice.finish_reason = null;
                    ctx.chunk = stringify_stream_chunk(parsed);
                }
            } else {
                var history_had_tool_calls = false;
                if (ctx.history_chunks && ctx.history_chunks.length > 0) {
                    for (var i = 0; i < ctx.history_chunks.length; i++) {
                        try {
                            var h_parsed = parse_stream_chunk(ctx.history_chunks[i]);
                            if (h_parsed === null) {
                                continue;
                            }
                            var hist_obj = h_parsed.obj;
                            if (hist_obj.choices && hist_obj.choices.length > 0) {
                                var h_choice = hist_obj.choices[0];
                                if (h_choice.delta && h_choice.delta.tool_calls && h_choice.delta.tool_calls.length > 0) {
                                    history_had_tool_calls = true;
                                    break;
                                }
                            }
                        } catch (err) {
                        }
                    }
                }

                if (history_had_tool_calls && choice.finish_reason !== null && choice.finish_reason !== "tool_calls") {
                    console.log("[" + ctx.id + "] Detected history contains tool calls, modifying finish_reason from [" + choice.finish_reason + "] to [tool_calls]");
                    choice.finish_reason = "tool_calls";
                    ctx.chunk = stringify_stream_chunk(parsed);
                }
            }
        }
    } catch (e) {
        console.log("[" + ctx.id + "] Failed to parse streaming response JSON chunk: " + e.message + " | chunk content: " + ctx.chunk);
    }
    return ctx;
}

function on_after_nonstream_response(ctx) {
    console.log("[" + ctx.id + "] Received non-streaming response. Response content: " + ctx.body);
    return ctx;
}

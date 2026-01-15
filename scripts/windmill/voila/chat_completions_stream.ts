// Windmill Script: voila_chat_completions_stream
// OpenAI-compatible /v1/chat/completions endpoint (streaming SSE)
// Path in Windmill: f/voila/chat_completions_stream
//
// Usage:
//   POST https://js.chip.com.vn/api/w/chipvn/jobs/run_wait_result/p/f/voila/chat_completions_stream
//   Body: { "voila": "$res:f/voila/chatgpt", "request": { ..., "stream": true } }
//
// Returns: Server-Sent Events (SSE) stream

// ============================================================================
// TYPES
// ============================================================================

type VoilaResource = {
  auth_token: string;
  email: string;
  user_uuid: string;
  base_url: string;
  language: string;
  version: string;
  model?: string;
  conversation?: string;
};

interface OpenAIMessage {
  role: "system" | "user" | "assistant";
  content: string | ContentPart[];
}

interface ContentPart {
  type: "text" | "image_url";
  text?: string;
}

interface OpenAIChatRequest {
  model: string;
  messages: OpenAIMessage[];
  stream?: boolean;
  max_tokens?: number;
  temperature?: number;
}

interface VoilaChatMessage {
  role: string;
  content: string;
  attachments: any[];
  attachmentIds: any[];
}

// ============================================================================
// MAIN FUNCTION - Returns AsyncGenerator for streaming
// ============================================================================

export async function* main(
  voila: VoilaResource,
  request: OpenAIChatRequest
): AsyncGenerator<string, void, unknown> {
  // Validate
  if (!request.messages || request.messages.length === 0) {
    yield formatSSEError("Messages array is required");
    return;
  }

  const responseId = `chatcmpl-${generateId()}`;
  const model = request.model || voila.model || "gpt-4";
  const created = Math.floor(Date.now() / 1000);

  // Convert messages
  const voilaChat = convertMessagesToVoilaChat(request.messages);

  // Build Voila request
  const voilaRequest = {
    chat: voilaChat,
    email: voila.email,
    model: model,
    auth_token: voila.auth_token,
    client_date: new Date().toISOString(),
    language: voila.language || "vi",
    context: "",
    attachments: [],
    persona: null,
    conversation: voila.conversation || generateConversationId(),
    user_uuid: voila.user_uuid,
    version: voila.version || "1.7.6",
  };

  // Call Voila API
  let response: Response;
  try {
    response = await fetch(voila.base_url, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Accept: "text/event-stream, application/json",
        "User-Agent":
          "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
        "Cache-Control": "no-cache",
        Connection: "keep-alive",
      },
      body: JSON.stringify(voilaRequest),
    });
  } catch (error) {
    yield formatSSEError(`Network error: ${error}`);
    return;
  }

  if (!response.ok) {
    const errorBody = await response.text();
    yield formatSSEError(`Voila API error ${response.status}: ${errorBody}`);
    return;
  }

  // Check if response is SSE stream
  const contentType = response.headers.get("content-type") || "";

  if (contentType.includes("text/event-stream")) {
    // Handle SSE stream from Voila
    yield* handleVoilaSSEStream(response, responseId, model, created);
  } else {
    // Handle non-streaming response (convert to single SSE chunk)
    yield* handleVoilaNonStreamResponse(response, responseId, model, created);
  }

  // Send [DONE] signal
  yield "data: [DONE]\n\n";
}

// ============================================================================
// STREAM HANDLERS
// ============================================================================

/**
 * Handle SSE stream from Voila API
 */
async function* handleVoilaSSEStream(
  response: Response,
  responseId: string,
  model: string,
  created: number
): AsyncGenerator<string, void, unknown> {
  const reader = response.body?.getReader();
  if (!reader) {
    yield formatSSEChunk(
      responseId,
      model,
      created,
      "Error: No response body",
      null
    );
    return;
  }

  const decoder = new TextDecoder();
  let buffer = "";

  try {
    while (true) {
      const { done, value } = await reader.read();

      if (done) break;

      buffer += decoder.decode(value, { stream: true });

      // Process complete lines
      const lines = buffer.split("\n");
      buffer = lines.pop() || ""; // Keep incomplete line in buffer

      for (const line of lines) {
        if (!line.startsWith("data: ")) continue;

        const jsonStr = line.slice(6).trim();
        if (!jsonStr || jsonStr === "[DONE]") continue;

        try {
          const voilaChunk = JSON.parse(jsonStr);

          // Extract text from Voila chunk
          let text = "";
          if (voilaChunk.text) {
            text = voilaChunk.text;
          } else if (voilaChunk.delta?.content) {
            text = voilaChunk.delta.content;
          }

          if (text) {
            yield formatSSEChunk(responseId, model, created, text, null);
          }
        } catch {
          // Skip invalid JSON
        }
      }
    }

    // Process remaining buffer
    if (buffer.startsWith("data: ")) {
      const jsonStr = buffer.slice(6).trim();
      if (jsonStr && jsonStr !== "[DONE]") {
        try {
          const voilaChunk = JSON.parse(jsonStr);
          if (voilaChunk.text) {
            yield formatSSEChunk(
              responseId,
              model,
              created,
              voilaChunk.text,
              null
            );
          }
        } catch {}
      }
    }

    // Send final chunk with finish_reason
    yield formatSSEChunk(responseId, model, created, "", "stop");
  } finally {
    reader.releaseLock();
  }
}

/**
 * Handle non-streaming response from Voila (convert to SSE chunks)
 */
async function* handleVoilaNonStreamResponse(
  response: Response,
  responseId: string,
  model: string,
  created: number
): AsyncGenerator<string, void, unknown> {
  const responseText = await response.text();
  const extractedText = extractTextFromVoilaResponse(responseText);

  // Simulate streaming by chunking the response
  const chunkSize = 20; // characters per chunk

  for (let i = 0; i < extractedText.length; i += chunkSize) {
    const chunk = extractedText.slice(i, i + chunkSize);
    yield formatSSEChunk(responseId, model, created, chunk, null);

    // Small delay to simulate streaming (optional)
    // await new Promise(resolve => setTimeout(resolve, 10));
  }

  // Final chunk with finish_reason
  yield formatSSEChunk(responseId, model, created, "", "stop");
}

// ============================================================================
// FORMATTING FUNCTIONS
// ============================================================================

/**
 * Format OpenAI-compatible SSE chunk
 */
function formatSSEChunk(
  id: string,
  model: string,
  created: number,
  content: string,
  finishReason: "stop" | "length" | null
): string {
  const chunk = {
    id: id,
    object: "chat.completion.chunk",
    created: created,
    model: model,
    choices: [
      {
        index: 0,
        delta: content ? { content: content } : {},
        finish_reason: finishReason,
      },
    ],
  };

  return `data: ${JSON.stringify(chunk)}\n\n`;
}

/**
 * Format error as SSE
 */
function formatSSEError(message: string): string {
  const errorResponse = {
    error: {
      message: message,
      type: "api_error",
      code: "voila_error",
    },
  };
  return `data: ${JSON.stringify(errorResponse)}\n\ndata: [DONE]\n\n`;
}

// ============================================================================
// HELPER FUNCTIONS
// ============================================================================

function convertMessagesToVoilaChat(
  messages: OpenAIMessage[]
): VoilaChatMessage[] {
  return messages.map((msg) => {
    let content = "";
    if (typeof msg.content === "string") {
      content = msg.content;
    } else if (Array.isArray(msg.content)) {
      content = msg.content
        .filter(
          (part): part is ContentPart & { type: "text" } => part.type === "text"
        )
        .map((part) => part.text || "")
        .join("\n");
    }
    return {
      role: msg.role,
      content: content,
      attachments: [],
      attachmentIds: [],
    };
  });
}

function extractTextFromVoilaResponse(responseText: string): string {
  // Try JSON array
  try {
    const jsonArray = JSON.parse(responseText);
    if (
      Array.isArray(jsonArray) &&
      jsonArray.length > 0 &&
      jsonArray[0].text
    ) {
      return jsonArray[0].text;
    }
  } catch {}

  // Try JSON object
  try {
    const jsonObj = JSON.parse(responseText);
    if (jsonObj.text) return jsonObj.text;
  } catch {}

  return responseText.trim();
}

function generateId(): string {
  return (
    Math.random().toString(36).substring(2, 15) +
    Math.random().toString(36).substring(2, 15)
  );
}

function generateConversationId(): string {
  return Date.now().toString(36) + Math.random().toString(36).substring(2, 8);
}

// Windmill Script: voila_chat_completions
// OpenAI-compatible /v1/chat/completions endpoint (non-streaming)
// Path in Windmill: f/voila/chat_completions
//
// Usage:
//   POST https://js.chip.com.vn/api/w/chipvn/jobs/run_wait_result/p/f/voila/chat_completions
//   Body: { "voila": "$res:u/chipvn/chatgpt", "request": { ... } }

// ============================================================================
// TYPES
// ============================================================================

// Voila Resource Type (from Windmill Resource: u/chipvn/chatgpt)
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

// OpenAI Request Types
interface OpenAIMessage {
  role: "system" | "user" | "assistant";
  content: string | ContentPart[];
}

interface ContentPart {
  type: "text" | "image_url";
  text?: string;
  image_url?: { url: string };
}

interface OpenAIChatRequest {
  model: string;
  messages: OpenAIMessage[];
  stream?: boolean;
  max_tokens?: number;
  temperature?: number;
  top_p?: number;
  n?: number;
  stop?: string | string[];
  presence_penalty?: number;
  frequency_penalty?: number;
  user?: string;
}

// Voila Types
interface VoilaChatMessage {
  role: string;
  content: string;
  attachments: any[];
  attachmentIds: any[];
}

interface VoilaRequest {
  chat: VoilaChatMessage[];
  email: string;
  model: string;
  auth_token: string;
  client_date: string;
  language: string;
  context: string;
  attachments: any[];
  persona: null;
  conversation: string;
  user_uuid: string;
  version: string;
}

// OpenAI Response Types
interface OpenAIChatResponse {
  id: string;
  object: "chat.completion";
  created: number;
  model: string;
  choices: {
    index: number;
    message: {
      role: "assistant";
      content: string;
    };
    finish_reason: "stop" | "length" | "content_filter" | null;
  }[];
  usage: {
    prompt_tokens: number;
    completion_tokens: number;
    total_tokens: number;
  };
}

// ============================================================================
// MAIN FUNCTION
// ============================================================================

export async function main(
  voila: VoilaResource, // Resource: u/chipvn/chatgpt
  request: OpenAIChatRequest
): Promise<OpenAIChatResponse> {
  // Validate request
  if (!request.messages || request.messages.length === 0) {
    throw new Error("Messages array is required and cannot be empty");
  }

  // Convert OpenAI messages to Voila chat format
  const voilaChat = convertMessagesToVoilaChat(request.messages);

  // Build Voila request payload
  const voilaRequest: VoilaRequest = {
    chat: voilaChat,
    email: voila.email,
    model: request.model || voila.model || "gpt-4",
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
  const startTime = Date.now();
  const response = await fetch(voila.base_url, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Accept: "application/json, text/event-stream",
      "User-Agent":
        "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
      Connection: "keep-alive",
    },
    body: JSON.stringify(voilaRequest),
  });

  if (!response.ok) {
    const errorBody = await response.text();
    throw new Error(`Voila API error ${response.status}: ${errorBody}`);
  }

  // Parse Voila response
  const responseText = await response.text();
  const extractedText = extractTextFromVoilaResponse(responseText);
  const latency = Date.now() - startTime;

  // Calculate token usage (estimation)
  const promptTokens = estimateTokenCount(voilaChat);
  const completionTokens = estimateTokenCount([{ content: extractedText }]);

  // Build OpenAI-compatible response
  const openAIResponse: OpenAIChatResponse = {
    id: `chatcmpl-${generateId()}`,
    object: "chat.completion",
    created: Math.floor(Date.now() / 1000),
    model: request.model || voila.model || "gpt-4",
    choices: [
      {
        index: 0,
        message: {
          role: "assistant",
          content: extractedText,
        },
        finish_reason: "stop",
      },
    ],
    usage: {
      prompt_tokens: promptTokens,
      completion_tokens: completionTokens,
      total_tokens: promptTokens + completionTokens,
    },
  };

  console.log(
    `Voila API latency: ${latency}ms, tokens: ${openAIResponse.usage.total_tokens}`
  );

  return openAIResponse;
}

// ============================================================================
// HELPER FUNCTIONS
// ============================================================================

/**
 * Convert OpenAI messages array to Voila chat format
 */
function convertMessagesToVoilaChat(
  messages: OpenAIMessage[]
): VoilaChatMessage[] {
  return messages.map((msg) => {
    let content = "";

    if (typeof msg.content === "string") {
      content = msg.content;
    } else if (Array.isArray(msg.content)) {
      // Handle content array (multimodal)
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

/**
 * Extract text from various Voila response formats
 */
function extractTextFromVoilaResponse(responseText: string): string {
  // Try 1: JSON array format [{"text": "...", "success": true}]
  try {
    const jsonArray = JSON.parse(responseText);
    if (Array.isArray(jsonArray) && jsonArray.length > 0) {
      // Check for SSE data field
      if (jsonArray[0].data && typeof jsonArray[0].data === "string") {
        const sseText = parseSSEData(jsonArray[0].data);
        if (sseText) return sseText;
      }
      // Check for direct text field
      if (jsonArray[0].text) {
        return jsonArray[0].text;
      }
    }
  } catch {}

  // Try 2: SSE stream format (data: {...}\n)
  if (responseText.includes("data: ")) {
    const sseText = parseSSEData(responseText);
    if (sseText) return sseText;
  }

  // Try 3: Single JSON object {"text": "...", "success": true}
  try {
    const jsonObj = JSON.parse(responseText);
    if (jsonObj.text) return jsonObj.text;
  } catch {}

  // Fallback: return as plain text
  return responseText.trim();
}

/**
 * Parse Server-Sent Events data and extract text
 */
function parseSSEData(data: string): string {
  if (!data) return "";

  const textParts: string[] = [];
  const lines = data.split("\n");

  for (const line of lines) {
    if (!line.startsWith("data: ")) continue;

    const jsonStr = line.slice(6).trim();
    if (!jsonStr || jsonStr === "[DONE]") continue;

    try {
      const chunk = JSON.parse(jsonStr);
      if (chunk.text) {
        textParts.push(chunk.text);
      }
    } catch {}
  }

  return textParts.join("");
}

/**
 * Estimate token count (rough approximation: ~4 chars per token for English/Vietnamese)
 */
function estimateTokenCount(messages: { content?: string }[]): number {
  const totalChars = messages.reduce(
    (sum, msg) => sum + (msg.content?.length || 0),
    0
  );
  return Math.ceil(totalChars / 4);
}

/**
 * Generate random ID for response
 */
function generateId(): string {
  return (
    Math.random().toString(36).substring(2, 15) +
    Math.random().toString(36).substring(2, 15)
  );
}

/**
 * Generate conversation ID
 */
function generateConversationId(): string {
  return Date.now().toString(36) + Math.random().toString(36).substring(2, 8);
}

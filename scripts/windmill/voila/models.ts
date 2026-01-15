// Windmill Script: voila_models
// OpenAI-compatible /v1/models endpoint
// Path in Windmill: f/voila/models
//
// Usage:
//   GET https://js.chip.com.vn/api/w/chipvn/jobs/run_wait_result/p/f/voila/models
//
// Returns list of available OpenAI models through Voila

// ============================================================================
// TYPES
// ============================================================================

interface OpenAIModel {
  id: string;
  object: "model";
  created: number;
  owned_by: string;
}

interface OpenAIModelsResponse {
  object: "list";
  data: OpenAIModel[];
}

// ============================================================================
// AVAILABLE MODELS
// ============================================================================

// OpenAI models available through Voila
// Update this list based on your Voila subscription
const VOILA_AVAILABLE_MODELS: OpenAIModel[] = [
  // GPT-4 Series
  {
    id: "gpt-4",
    object: "model",
    created: 1687882411,
    owned_by: "voila-openai",
  },
  {
    id: "gpt-4-turbo",
    object: "model",
    created: 1712361441,
    owned_by: "voila-openai",
  },
  {
    id: "gpt-4-turbo-preview",
    object: "model",
    created: 1706037777,
    owned_by: "voila-openai",
  },
  {
    id: "gpt-4o",
    object: "model",
    created: 1715367049,
    owned_by: "voila-openai",
  },
  {
    id: "gpt-4o-mini",
    object: "model",
    created: 1721172741,
    owned_by: "voila-openai",
  },
  {
    id: "gpt-4.1",
    object: "model",
    created: 1730000000,
    owned_by: "voila-openai",
  },
  {
    id: "gpt-4.1-mini",
    object: "model",
    created: 1730000000,
    owned_by: "voila-openai",
  },
  {
    id: "gpt-4.1-nano",
    object: "model",
    created: 1730000000,
    owned_by: "voila-openai",
  },

  // GPT-3.5 Series
  {
    id: "gpt-3.5-turbo",
    object: "model",
    created: 1677610602,
    owned_by: "voila-openai",
  },
  {
    id: "gpt-3.5-turbo-16k",
    object: "model",
    created: 1683758102,
    owned_by: "voila-openai",
  },

  // O1 Series (Reasoning models)
  {
    id: "o1",
    object: "model",
    created: 1734566400,
    owned_by: "voila-openai",
  },
  {
    id: "o1-mini",
    object: "model",
    created: 1725926400,
    owned_by: "voila-openai",
  },
  {
    id: "o1-preview",
    object: "model",
    created: 1725926400,
    owned_by: "voila-openai",
  },

  // O3 Series
  {
    id: "o3-mini",
    object: "model",
    created: 1737763200,
    owned_by: "voila-openai",
  },

  // O4-mini (if available)
  {
    id: "o4-mini",
    object: "model",
    created: 1740000000,
    owned_by: "voila-openai",
  },
];

// ============================================================================
// MAIN FUNCTION
// ============================================================================

export async function main(): Promise<OpenAIModelsResponse> {
  return {
    object: "list",
    data: VOILA_AVAILABLE_MODELS,
  };
}

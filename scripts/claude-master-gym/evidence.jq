def blocks:
  if type == "array" then . else [] end;
def text:
  if type == "string" then .
  else blocks | map(select(.type == "text") | .text // "") | join("\n") end;

map(select(.sessionId == $session and (.isSidechain // false) == false))
| to_entries
| . as $records
| "This is Claude Master gym lane \($lane)." as $prompt
| "GYM_LANE_\($lane)_OK" as $marker
| ([$records[] | select(.value.type == "user" and .value.message.role == "user")
    | select((.value.message.content | text) | startswith($prompt)) | .key]
   | last // -1) as $start
| ([$records[] | select(.key > $start)
    | select(.value.type == "user" and .value.message.role == "user" and (.value.isMeta // false) == false)
    | select((.value.message.content | blocks | any(.type == "tool_result")) | not)
    | .key] | first // 2147483647) as $end
| [$records[] | select(.key > $start and .key < $end)] as $turn
| ([$turn[] | select(.value.type == "assistant" and .value.message.role == "assistant")
    | select((.value.message.content | text) | test("^\\s*" + $marker + "(\\s|[.,:;!]|$)"))]
   | last) as $completion
| [$turn[] | select(.value.type == "assistant" and .value.message.role == "assistant")
   | .key as $key
   | .value.message.content | blocks | .[]
   | select(.type == "tool_use" and (.name == "Agent" or .name == "Task"))
   | {key: $key, call: .}] as $callEntries
| ($callEntries | map(select($completion == null or .key < $completion.key) | .call)) as $calls
| [$turn[] | select(.value.type == "user" and .value.message.role == "user")
   | select($completion == null or .key < $completion.key)
   | .value.message.content | blocks | .[]
   | select(.type == "tool_result" and (.is_error // false) == false)
   | .tool_use_id | select(type == "string" and length > 0)] as $results
| ($calls | map(.id) | unique) as $ids
| ([$ids[] | select(. as $id | $results | index($id))] | length) as $matched
| ($completion != null and $start >= 0 and ($calls | length) == 2
   and ($callEntries | length) == 2
   and ($ids | length) == 2 and $matched == 2
   and ($calls | all(.id | type == "string" and length > 0))
   and ($calls | all(.input.run_in_background != true))) as $verified
| {
    completion: (if $verified then "verified" else "unverified" end),
    reason: (if $start < 0 then "gym-prompt-not-found"
             elif $completion == null then "assistant-result-not-found"
             elif ($calls | length) != 2 or ($callEntries | length) != 2 or ($ids | length) != 2 then "expected-exactly-two-agents"
             elif $matched != 2 then "agent-results-incomplete"
             elif ($calls | any(.input.run_in_background == true)) then "background-agent-completion-unverified"
             else "assistant-and-agent-results-matched" end),
    agent_calls: ($callEntries | length),
    agent_results: $matched,
    model_verified: (($completion.value.message.model // "") | test("^claude-sonnet-5-5(-[0-9]{8})?$")),
    effort_verified: false
  }

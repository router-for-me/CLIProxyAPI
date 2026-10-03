package helps

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertResponsesCompactionTerminalEvent validates upstream JSON and appends capsule events to the prepared completion.
func ConvertResponsesCompactionTerminalEvent(event, upstreamEvent []byte, model, scope string, secrets []string) ([]byte, [][]byte, error) {
	if !gjson.ValidBytes(upstreamEvent) || !gjson.ParseBytes(upstreamEvent).IsObject() || !gjson.ValidBytes(event) || !gjson.ParseBytes(event).IsObject() {
		return nil, nil, ResponsesCompactionError{Message: "compaction upstream returned invalid terminal JSON"}
	}
	if err := validateResponsesCompactionTerminalOutput(upstreamEvent); err != nil {
		return nil, nil, err
	}
	if gjson.GetBytes(event, "type").String() != "response.completed" {
		return nil, nil, ResponsesCompactionError{Message: "compaction upstream did not complete"}
	}
	response := gjson.GetBytes(event, "response")
	converted, errConvert := ConvertResponsesCompactionResponse([]byte(response.Raw), model, scope, secrets)
	if errConvert != nil {
		return nil, nil, errConvert
	}
	terminal, errTerminal := sjson.SetRawBytes(event, "response", converted)
	if errTerminal != nil {
		return nil, nil, ResponsesCompactionError{Message: "compaction terminal event could not be encoded"}
	}
	index := len(response.Get("output").Array())
	output := gjson.GetBytes(converted, "output").Array()
	var events [][]byte
	if len(output) > index {
		item := output[index]
		events = make([][]byte, 0, 2)
		sequence := gjson.GetBytes(event, "sequence_number")
		for offset, eventType := range []string{"response.output_item.added", "response.output_item.done"} {
			itemEvent, _ := sjson.SetBytes([]byte(`{}`), "type", eventType)
			itemEvent, _ = sjson.SetBytes(itemEvent, "output_index", index)
			itemEvent, _ = sjson.SetBytes(itemEvent, "response_id", gjson.GetBytes(converted, "id").String())
			itemEvent, _ = sjson.SetRawBytes(itemEvent, "item", []byte(item.Raw))
			if sequence.Exists() {
				itemEvent, _ = sjson.SetBytes(itemEvent, "sequence_number", sequence.Int()+int64(offset))
			}
			events = append(events, itemEvent)
		}
		if sequence.Exists() {
			terminal, _ = sjson.SetBytes(terminal, "sequence_number", sequence.Int()+2)
		}
	}
	return terminal, events, nil
}

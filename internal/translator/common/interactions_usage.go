package common

import "github.com/tidwall/gjson"

func InteractionsUsage(root gjson.Result) gjson.Result {
	for _, path := range []string{
		"interaction.usage",
		"usage",
		"metadata.total_usage",
		"metadata.usage",
		"interaction.metadata.total_usage",
		"interaction.metadata.usage",
	} {
		if value := root.Get(path); value.Exists() {
			return value
		}
	}
	return gjson.Result{}
}

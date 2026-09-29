package authz

const (
	ResourceTaskPlugin = "task_plugin"

	ActionBind = "bind"
)

var TaskPluginBind = Permission{Resource: ResourceTaskPlugin, Action: ActionBind}

func init() {
	RegisterResource(ResourceDefinition{
		Resource: ResourceTaskPlugin,
		LabelKey: "Task Plugin",
		Actions: []ActionDefinition{
			{
				Action:         ActionBind,
				LabelKey:       "Bind task plugins",
				DescriptionKey: "List registered task plugins and bind them when creating or editing task plugin channels.",
			},
		},
	})
}

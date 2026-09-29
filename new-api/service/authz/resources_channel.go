package authz

const (
	ResourceChannel = "channel"

	ActionRead           = "read"
	ActionOperate        = "operate"
	ActionWrite          = "write"
	ActionSensitiveWrite = "sensitive_write"
	ActionSecretView     = "secret_view"
)

var (
	ChannelRead           = Permission{Resource: ResourceChannel, Action: ActionRead}
	ChannelOperate        = Permission{Resource: ResourceChannel, Action: ActionOperate}
	ChannelWrite          = Permission{Resource: ResourceChannel, Action: ActionWrite}
	ChannelSensitiveWrite = Permission{Resource: ResourceChannel, Action: ActionSensitiveWrite}
	ChannelSecretView     = Permission{Resource: ResourceChannel, Action: ActionSecretView}
)

func init() {
	RegisterResource(ResourceDefinition{
		Resource: ResourceChannel,
		LabelKey: "Channel Management",
		Actions: []ActionDefinition{
			{
				Action:         ActionRead,
				LabelKey:       "Read channels",
				DescriptionKey: "View channel lists and details without secrets.",
				DefaultRoles:   []string{BuiltInRoleAdmin},
			},
			{
				Action:         ActionOperate,
				LabelKey:       "Operate channels",
				DescriptionKey: "Test channels, refresh balances, and enable/disable individual, batch, or tagged channels.",
				DefaultRoles:   []string{BuiltInRoleAdmin},
			},
			{
				Action:         ActionWrite,
				LabelKey:       "Edit channel routing",
				DescriptionKey: "Edit non-sensitive settings such as models, groups, and routing rules.",
				DefaultRoles:   []string{BuiltInRoleAdmin},
			},
			{
				Action:         ActionSensitiveWrite,
				LabelKey:       "Edit sensitive channel settings",
				DescriptionKey: "Create channels or edit keys, base URLs, and overrides.",
			},
			{
				Action:         ActionSecretView,
				LabelKey:       "View channel secrets",
				DescriptionKey: "Reserved for viewing complete channel keys after secure verification.",
			},
		},
	})
}

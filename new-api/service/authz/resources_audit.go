package authz

const ResourceAudit = "audit"

var AuditRead = Permission{Resource: ResourceAudit, Action: ActionRead}

func init() {
	RegisterResource(ResourceDefinition{
		Resource: ResourceAudit,
		LabelKey: "Audit Logs",
		Actions: []ActionDefinition{{
			Action:         ActionRead,
			LabelKey:       "View other accounts' audit logs",
			DescriptionKey: "View audit records from user and admin roles. Root records are always excluded.",
		}},
	})
}

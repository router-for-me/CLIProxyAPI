package replicate

const (
	// ChannelName identifies the replicate channel.
	ChannelName = "replicate"
	// ModelFlux11Pro is the default image generation model supported by this channel.
	ModelFlux11Pro = "black-forest-labs/flux-1.1-pro"
)

var ModelList = []string{
	ModelFlux11Pro,
}

package constant

// WaffoPayMethod defines the display and API parameter mapping for Waffo payment methods.
type WaffoPayMethod struct {
	Name          string `json:"name"`          // Frontend display name
	Icon          string `json:"icon"`          // Frontend icon identifier: credit-card, apple, google
	PayMethodType string `json:"payMethodType"` // Waffo API PayMethodType, can be comma-separated
	PayMethodName string `json:"payMethodName"` // Waffo API PayMethodName, empty means auto-select by Waffo checkout
}

// DefaultWaffoPayMethods is the default list of supported payment methods.
var DefaultWaffoPayMethods = []WaffoPayMethod{
	{Name: "Card", Icon: "/pay-card.png", PayMethodType: "CREDITCARD,DEBITCARD", PayMethodName: ""},
	{Name: "Apple Pay", Icon: "/pay-apple.png", PayMethodType: "APPLEPAY", PayMethodName: "APPLEPAY"},
	{Name: "Google Pay", Icon: "/pay-google.png", PayMethodType: "GOOGLEPAY", PayMethodName: "GOOGLEPAY"},
}

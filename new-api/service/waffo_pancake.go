package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	pancake "github.com/waffo-com/waffo-pancake-sdk-go"
)

// WaffoPancakePriceSnapshot is the per-session price override sent with checkout.
type WaffoPancakePriceSnapshot struct {
	Amount      string
	TaxCategory string
}

// WaffoPancakeCreateSessionParams is the input to CreateWaffoPancakeCheckoutSession.
// BuyerIdentity must be stable per user (see WaffoPancakeBuyerIdentityFromUserID).
// OrderMerchantExternalID = our trade_no; Pancake echoes it back in webhooks.
type WaffoPancakeCreateSessionParams struct {
	ProductID               string
	BuyerIdentity           string
	PriceSnapshot           *WaffoPancakePriceSnapshot
	BuyerEmail              string
	ExpiresInSeconds        *int
	OrderMerchantExternalID string
}

// WaffoPancakeCheckoutSession is the response of CreateWaffoPancakeCheckoutSession.
// CheckoutURL already carries the `#token=...` fragment; Token / TokenExpiresAt
// are exposed separately for self-service flows driven from new-api's own UI.
type WaffoPancakeCheckoutSession struct {
	SessionID      string
	CheckoutURL    string
	ExpiresAt      string
	OrderID        string
	Token          string
	TokenExpiresAt string
}

// WaffoPancakeWebhookEvent mirrors the SDK's WebhookEvent shape using plain
// strings so controllers don't have to import the SDK package.
type WaffoPancakeWebhookEvent struct {
	ID        string
	Timestamp string
	EventType string
	EventID   string
	StoreID   string
	Mode      string
	Data      WaffoPancakeWebhookData
}

type WaffoPancakeWebhookData struct {
	// OrderID = Pancake ORD_* (logs); OrderMerchantExternalID = our trade_no (lookup).
	OrderID                       string
	OrderMerchantExternalID       string
	BuyerEmail                    string
	Currency                      string
	Amount                        string
	TaxAmount                     string
	ProductName                   string
	MerchantProvidedBuyerIdentity string
}

// NormalizedEventType returns the event type or empty string for a nil event.
func (e *WaffoPancakeWebhookEvent) NormalizedEventType() string {
	if e == nil {
		return ""
	}
	return e.EventType
}

// newWaffoPancakeClient builds an SDK client from persisted settings. The
// runtime checkout / webhook paths use this; configuration endpoints use
// newWaffoPancakeClientFromCreds so the operator can verify typed-but-not-
// yet-saved credentials.
func newWaffoPancakeClient() (*pancake.Client, error) {
	return pancake.New(pancake.Config{
		MerchantID: setting.WaffoPancakeMerchantID,
		PrivateKey: setting.WaffoPancakePrivateKey,
	})
}

func newWaffoPancakeClientFromCreds(merchantID, privateKey string) (*pancake.Client, error) {
	if strings.TrimSpace(merchantID) == "" || strings.TrimSpace(privateKey) == "" {
		return nil, fmt.Errorf("merchant id and private key are required")
	}
	return pancake.New(pancake.Config{
		MerchantID: merchantID,
		PrivateKey: privateKey,
	})
}

// CreateWaffoPancakeCheckoutSession creates an Authenticated-mode checkout
// session: the order is bound to BuyerIdentity (stable per user) so it stays
// attributable even if the buyer edits the email on Waffo's checkout form.
func CreateWaffoPancakeCheckoutSession(ctx context.Context, params *WaffoPancakeCreateSessionParams) (*WaffoPancakeCheckoutSession, error) {
	if params == nil {
		return nil, fmt.Errorf("missing checkout params")
	}
	if strings.TrimSpace(params.BuyerIdentity) == "" {
		return nil, fmt.Errorf("missing buyer identity")
	}
	if strings.TrimSpace(params.OrderMerchantExternalID) == "" {
		return nil, fmt.Errorf("missing order merchant external id")
	}
	client, err := newWaffoPancakeClient()
	if err != nil {
		return nil, fmt.Errorf("build Waffo Pancake client: %w", err)
	}

	sdkParams := pancake.AuthenticatedCheckoutParams{
		CreateCheckoutSessionParams: pancake.CreateCheckoutSessionParams{
			ProductID:               params.ProductID,
			Currency:                "USD",
			BuyerEmail:              optionalString(params.BuyerEmail),
			ExpiresInSeconds:        params.ExpiresInSeconds,
			OrderMerchantExternalID: optionalString(params.OrderMerchantExternalID),
		},
		BuyerIdentity: params.BuyerIdentity,
	}
	if params.PriceSnapshot != nil {
		sdkParams.PriceSnapshot = &pancake.PriceInfo{
			Amount:      params.PriceSnapshot.Amount,
			TaxCategory: pancake.TaxCategory(params.PriceSnapshot.TaxCategory),
		}
	}

	session, err := client.Checkout.Authenticated.Create(ctx, sdkParams)
	if err != nil {
		return nil, err
	}
	if session == nil || strings.TrimSpace(session.CheckoutURL) == "" || strings.TrimSpace(session.SessionID) == "" {
		return nil, fmt.Errorf("Waffo Pancake returned empty checkout session")
	}
	return &WaffoPancakeCheckoutSession{
		SessionID:      session.SessionID,
		CheckoutURL:    session.CheckoutURL,
		ExpiresAt:      session.ExpiresAt,
		Token:          session.Token,
		TokenExpiresAt: session.TokenExpiresAt,
	}, nil
}

func optionalString(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	v := s
	return &v
}

// WaffoPancakeBuyerIdentityFromUserID renders the canonical buyer identity
// for checkout. Webhook handlers compare against the value rendered here to
// reject identity mismatches, so both call sites must use this function.
func WaffoPancakeBuyerIdentityFromUserID(userID int) string {
	return fmt.Sprintf("new-api-user-%d", userID)
}

// VerifyConfiguredWaffoPancakeWebhook verifies the signature header. The SDK
// picks the matching test / prod public key from the payload's `mode` field.
func VerifyConfiguredWaffoPancakeWebhook(payload string, signatureHeader string) (*WaffoPancakeWebhookEvent, error) {
	evt, err := pancake.VerifyWebhookTyped[pancake.WebhookEventData](payload, signatureHeader, nil)
	if err != nil {
		return nil, err
	}
	identity := ""
	if evt.Data.MerchantProvidedBuyerIdentity != nil {
		identity = *evt.Data.MerchantProvidedBuyerIdentity
	}
	externalID := ""
	if evt.Data.OrderMerchantExternalID != nil {
		externalID = *evt.Data.OrderMerchantExternalID
	}
	return &WaffoPancakeWebhookEvent{
		ID:        evt.ID,
		Timestamp: evt.Timestamp,
		EventType: evt.EventType,
		EventID:   evt.EventID,
		StoreID:   evt.StoreID,
		Mode:      string(evt.Mode),
		Data: WaffoPancakeWebhookData{
			OrderID:                       evt.Data.OrderID,
			OrderMerchantExternalID:       externalID,
			BuyerEmail:                    evt.Data.BuyerEmail,
			Currency:                      evt.Data.Currency,
			Amount:                        evt.Data.Amount,
			TaxAmount:                     evt.Data.TaxAmount,
			ProductName:                   evt.Data.ProductName,
			MerchantProvidedBuyerIdentity: identity,
		},
	}, nil
}

// ResolveWaffoPancakeTradeNo maps a verified webhook event to a local TopUp
// trade_no via OrderMerchantExternalID, and rejects buyer-identity mismatches.
func ResolveWaffoPancakeTradeNo(event *WaffoPancakeWebhookEvent) (string, error) {
	if event == nil {
		return "", fmt.Errorf("missing webhook event")
	}
	tradeNo := strings.TrimSpace(event.Data.OrderMerchantExternalID)
	if tradeNo == "" {
		return "", fmt.Errorf("missing webhook orderMerchantExternalId")
	}
	topUp := model.GetTopUpByTradeNo(tradeNo)
	if topUp == nil || topUp.PaymentProvider != model.PaymentProviderWaffoPancake {
		return "", fmt.Errorf("waffo pancake order not found for tradeNo=%s", tradeNo)
	}
	expectedIdentity := WaffoPancakeBuyerIdentityFromUserID(topUp.UserId)
	actualIdentity := strings.TrimSpace(event.Data.MerchantProvidedBuyerIdentity)
	if actualIdentity != expectedIdentity {
		return "", fmt.Errorf(
			"waffo pancake buyer identity mismatch for tradeNo=%s: expected=%q actual=%q",
			tradeNo,
			expectedIdentity,
			actualIdentity,
		)
	}
	return tradeNo, nil
}

// ResolveWaffoPancakeSubscriptionTradeNo is the SubscriptionOrder counterpart
// of ResolveWaffoPancakeTradeNo.
func ResolveWaffoPancakeSubscriptionTradeNo(event *WaffoPancakeWebhookEvent) (string, error) {
	if event == nil {
		return "", fmt.Errorf("missing webhook event")
	}
	tradeNo := strings.TrimSpace(event.Data.OrderMerchantExternalID)
	if tradeNo == "" {
		return "", fmt.Errorf("missing webhook orderMerchantExternalId")
	}
	order := model.GetSubscriptionOrderByTradeNo(tradeNo)
	if order == nil || order.PaymentProvider != model.PaymentProviderWaffoPancake {
		return "", fmt.Errorf("waffo pancake subscription order not found for tradeNo=%s", tradeNo)
	}
	expectedIdentity := WaffoPancakeBuyerIdentityFromUserID(order.UserId)
	actualIdentity := strings.TrimSpace(event.Data.MerchantProvidedBuyerIdentity)
	if actualIdentity != expectedIdentity {
		return "", fmt.Errorf(
			"waffo pancake buyer identity mismatch for subscription tradeNo=%s: expected=%q actual=%q",
			tradeNo,
			expectedIdentity,
			actualIdentity,
		)
	}
	return tradeNo, nil
}

// Deterministic default names for "+ Create": stable bodies mean stable
// X-Idempotency-Key, which lets Pancake dedupe retries server-side.
const (
	defaultWaffoPancakeStoreName   = "new-api-store"
	defaultWaffoPancakeProductName = "new-api-charge-product"
)

// CreateWaffoPancakePrimaryStore creates a Pancake Store using in-flight
// (not-yet-persisted) credentials and returns the new store ID.
func CreateWaffoPancakePrimaryStore(ctx context.Context, merchantID, privateKey string) (string, error) {
	client, err := newWaffoPancakeClientFromCreds(merchantID, privateKey)
	if err != nil {
		return "", err
	}
	storeRes, err := client.Stores.Create(ctx, pancake.CreateStoreParams{
		Name: defaultWaffoPancakeStoreName,
	})
	if err != nil {
		return "", fmt.Errorf("create Waffo Pancake store: %w", err)
	}
	return storeRes.Store.ID, nil
}

// CreateWaffoPancakeProductForPlan mints (and publishes) a Pancake
// OnetimeProduct priced at `amount` USD, used as a subscription plan's
// SubscriptionPlan.WaffoPancakeProductId.
//
// OnetimeProduct (not SubscriptionProduct) because new-api has no renewal-
// event handling; Pancake auto-renewing without new-api extending user
// access would be a UX divergence. Revisit if renewal handling is added.
func CreateWaffoPancakeProductForPlan(ctx context.Context, merchantID, privateKey, storeID, name, amount, returnURL string) (string, error) {
	storeID = strings.TrimSpace(storeID)
	if storeID == "" {
		return "", fmt.Errorf("store id is required to create a product")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("plan name is required")
	}
	amount = strings.TrimSpace(amount)
	if amount == "" {
		return "", fmt.Errorf("plan price is required")
	}
	client, err := newWaffoPancakeClientFromCreds(merchantID, privateKey)
	if err != nil {
		return "", err
	}
	prodRes, err := client.OnetimeProducts.Create(ctx, pancake.CreateOnetimeProductParams{
		StoreID: storeID,
		Name:    name,
		Prices: pancake.Prices{
			"USD": {
				Amount:      amount,
				TaxCategory: pancake.TaxCategory("saas"),
			},
		},
		SuccessURL: optionalString(strings.TrimSpace(returnURL)),
	})
	if err != nil {
		return "", fmt.Errorf("create Waffo Pancake plan product: %w", err)
	}
	productID := prodRes.Product.ID
	if _, err := client.OnetimeProducts.Publish(ctx, pancake.PublishOnetimeProductParams{ID: productID}); err != nil {
		return "", fmt.Errorf("publish Waffo Pancake plan product: %w", err)
	}
	return productID, nil
}

// CreateWaffoPancakePrimaryProduct mints (and publishes) the wallet-top-up
// OnetimeProduct under storeID. Per-checkout price overrides via PriceSnapshot
// are what make the "1.00" seed price irrelevant at runtime.
func CreateWaffoPancakePrimaryProduct(ctx context.Context, merchantID, privateKey, storeID, returnURL string) (string, error) {
	storeID = strings.TrimSpace(storeID)
	if storeID == "" {
		return "", fmt.Errorf("store id is required to create a product")
	}
	client, err := newWaffoPancakeClientFromCreds(merchantID, privateKey)
	if err != nil {
		return "", err
	}
	prodRes, err := client.OnetimeProducts.Create(ctx, pancake.CreateOnetimeProductParams{
		StoreID: storeID,
		Name:    defaultWaffoPancakeProductName,
		Prices: pancake.Prices{
			"USD": {
				Amount:      "1.00", // overridden at checkout via PriceSnapshot
				TaxCategory: pancake.TaxCategory("saas"),
			},
		},
		SuccessURL: optionalString(strings.TrimSpace(returnURL)),
	})
	if err != nil {
		return "", fmt.Errorf("create Waffo Pancake product: %w", err)
	}
	productID := prodRes.Product.ID
	if _, err := client.OnetimeProducts.Publish(ctx, pancake.PublishOnetimeProductParams{ID: productID}); err != nil {
		return "", fmt.Errorf("publish Waffo Pancake product: %w", err)
	}
	return productID, nil
}

// WaffoPancakePairResult is the response of CreateWaffoPancakePrimaryPair.
// When OrphanStore is true the store was created but the product wasn't,
// so the caller can surface a partial-failure message with StoreID.
type WaffoPancakePairResult struct {
	StoreID     string
	StoreName   string
	ProductID   string
	ProductName string
	OrphanStore bool
}

// CreateWaffoPancakePrimaryPair mints a Store + OnetimeProduct in one
// round-trip — the canonical "+ Create" entry point. Nothing is persisted
// to settings; the operator's final Save commits the chosen IDs.
func CreateWaffoPancakePrimaryPair(ctx context.Context, merchantID, privateKey, returnURL string) (*WaffoPancakePairResult, error) {
	storeID, err := CreateWaffoPancakePrimaryStore(ctx, merchantID, privateKey)
	if err != nil {
		return nil, err
	}
	productID, err := CreateWaffoPancakePrimaryProduct(ctx, merchantID, privateKey, storeID, returnURL)
	if err != nil {
		return &WaffoPancakePairResult{
			StoreID:     storeID,
			StoreName:   defaultWaffoPancakeStoreName,
			OrphanStore: true,
		}, fmt.Errorf("store created at %s but product creation failed: %w", storeID, err)
	}
	return &WaffoPancakePairResult{
		StoreID:     storeID,
		StoreName:   defaultWaffoPancakeStoreName,
		ProductID:   productID,
		ProductName: defaultWaffoPancakeProductName,
	}, nil
}

// SaveWaffoPancakeConfig persists the operator-controlled fields atomically
// at the end of the configuration flow via model.UpdateOptionsBulk (single
// DB transaction). A blank privateKey is treated as "keep current"
// (Stripe-style API-secret UX) and is omitted from the bulk payload.
func SaveWaffoPancakeConfig(ctx context.Context, merchantID, privateKey, returnURL, storeID, productID string) error {
	merchantID = strings.TrimSpace(merchantID)
	storeID = strings.TrimSpace(storeID)
	productID = strings.TrimSpace(productID)
	if merchantID == "" || storeID == "" || productID == "" {
		return fmt.Errorf("merchant id, store id, and product id are required to save")
	}
	values := map[string]string{
		"WaffoPancakeMerchantID": merchantID,
		"WaffoPancakeReturnURL":  strings.TrimSpace(returnURL),
		"WaffoPancakeStoreID":    storeID,
		"WaffoPancakeProductID":  productID,
	}
	if pk := strings.TrimSpace(privateKey); pk != "" {
		values["WaffoPancakePrivateKey"] = pk
	}
	if err := model.UpdateOptionsBulk(values); err != nil {
		return fmt.Errorf("persist Waffo Pancake config: %w", err)
	}
	return nil
}

type WaffoPancakeCatalogProduct struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// WaffoPancakeCatalogStore nests its OnetimeProducts so the UI can render a
// dependent store→product select without a second round-trip.
type WaffoPancakeCatalogStore struct {
	ID              string                       `json:"id"`
	Name            string                       `json:"name"`
	Status          string                       `json:"status"`
	ProdEnabled     bool                         `json:"prodEnabled"`
	OnetimeProducts []WaffoPancakeCatalogProduct `json:"onetimeProducts"`
}

type WaffoPancakeCatalog struct {
	Stores []WaffoPancakeCatalogStore `json:"stores"`
}

// ListWaffoPancakeCatalog queries Pancake's GraphQL `stores` for the
// merchant's stores + onetime products. A successful call also proves
// the supplied credentials authenticate (doubles as a credential probe).
func ListWaffoPancakeCatalog(ctx context.Context, merchantID, privateKey string) (*WaffoPancakeCatalog, error) {
	client, err := newWaffoPancakeClientFromCreds(merchantID, privateKey)
	if err != nil {
		return nil, err
	}

	type queryShape struct {
		Stores []WaffoPancakeCatalogStore `json:"stores"`
	}
	// `limit: 100` because the API returns a single store when limit is
	// omitted, even for multi-store merchants. Bump to paginated fetches
	// (via `offset`) if real catalogs ever cross the cap.
	resp, err := pancake.GraphQLQuery[queryShape](ctx, client, pancake.GraphQLParams{
		Query: `query {
			stores(limit: 100) {
				id
				name
				status
				prodEnabled
				onetimeProducts {
					id
					name
					status
				}
			}
		}`,
	})
	if err != nil {
		return nil, fmt.Errorf("query Waffo Pancake catalog: %w", err)
	}
	if len(resp.Errors) > 0 {
		return nil, fmt.Errorf("waffo pancake catalog query returned %d errors: %s",
			len(resp.Errors), resp.Errors[0].Message)
	}
	// Drop non-active products. Operators should only see items they can
	// actually bind without later hitting "product unavailable" at checkout.
	stores := resp.Data.Stores
	for i := range stores {
		active := stores[i].OnetimeProducts[:0]
		for _, p := range stores[i].OnetimeProducts {
			if strings.EqualFold(strings.TrimSpace(p.Status), "active") {
				active = append(active, p)
			}
		}
		stores[i].OnetimeProducts = active
	}
	return &WaffoPancakeCatalog{Stores: stores}, nil
}

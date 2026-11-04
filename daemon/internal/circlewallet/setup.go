package circlewallet

// One-time setup and the read-only check, against the same Circle API the lane uses.
//
// Admin holds the API key only. The entity secret is passed to the two calls that need it
// (registering it, creating wallets) and to nothing else, so the read-only check never has
// it. None of these calls moves funds or signs a transaction: they register a key, create
// wallets, and read.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// WalletInfo is the part of Circle's wallet object Snapfall acts on.
type WalletInfo struct {
	ID          string `json:"id"`
	Address     string `json:"address"`
	Blockchain  string `json:"blockchain"`
	State       string `json:"state"`
	CustodyType string `json:"custodyType"`
	AccountType string `json:"accountType"`
	WalletSetID string `json:"walletSetId"`
	Name        string `json:"name"`
	RefID       string `json:"refId"`
}

// TokenBalance is one line of a wallet's balance as Circle reports it. Amount is a decimal
// string in whole tokens; it is informational, the chain is read for anything that matters.
type TokenBalance struct {
	Amount string `json:"amount"`
	Token  struct {
		Symbol       string `json:"symbol"`
		IsNative     bool   `json:"isNative"`
		TokenAddress string `json:"tokenAddress"`
	} `json:"token"`
}

// Admin is the Circle API with no wallet bound.
type Admin struct{ api api }

// AdminFromEnv builds an Admin from CIRCLE_API_KEY. Like NewFromEnv, a missing or malformed
// key is a refusal that names the variable, never the value.
func AdminFromEnv() (*Admin, error) {
	v, ok := os.LookupEnv(EnvAPIKey)
	if !ok || strings.TrimSpace(v) == "" {
		return nil, fmt.Errorf("circle wallet: %s is not set", EnvAPIKey)
	}
	return newAdmin(defaultBaseURL, strings.TrimSpace(v))
}

func newAdmin(baseURL, apiKey string) (*Admin, error) {
	a, err := newAPI(baseURL, apiKey)
	if err != nil {
		return nil, err
	}
	return &Admin{api: a}, nil
}

// KeyEnvironment is the Circle environment an API key belongs to, from its prefix:
// "testnet" for TEST_API_KEY, "mainnet" for LIVE_API_KEY, "" for anything else.
func KeyEnvironment(apiKey string) string {
	switch strings.SplitN(strings.TrimSpace(apiKey), ":", 2)[0] {
	case "TEST_API_KEY":
		return "testnet"
	case "LIVE_API_KEY":
		return "mainnet"
	}
	return ""
}

// Environment is KeyEnvironment for this Admin's key.
func (a *Admin) Environment() string { return KeyEnvironment(a.api.apiKey) }

// Ping proves the API key works: it reads the entity public key, which every key may read.
func (a *Admin) Ping(ctx context.Context) error {
	_, err := a.api.publicKey(ctx)
	return err
}

// RegisterEntitySecret registers secret as this account's entity secret and returns the
// recovery file Circle issues for it. Circle answers 2xx only once per account; a second
// registration is Circle's error, passed through.
func (a *Admin) RegisterEntitySecret(ctx context.Context, secret []byte) (string, error) {
	if len(secret) != 32 {
		return "", fmt.Errorf("circle wallet: the entity secret must be 32 bytes")
	}
	pub, err := a.api.publicKey(ctx)
	if err != nil {
		return "", err
	}
	ct, err := encryptSecret(pub, secret)
	if err != nil {
		return "", err
	}
	var out struct {
		Data struct {
			RecoveryFile string `json:"recoveryFile"`
		} `json:"data"`
	}
	if err := a.api.call(ctx, http.MethodPost, "/v1/w3s/config/entity/entitySecret", map[string]any{"entitySecretCiphertext": ct}, &out); err != nil {
		return "", fmt.Errorf("circle wallet: registering the entity secret: %w", err)
	}
	if out.Data.RecoveryFile == "" {
		// Registered but unrecoverable: say so loudly, the caller must not treat it as done.
		return "", fmt.Errorf("circle wallet: the entity secret was registered but Circle returned no recovery file; download it from the console before relying on this secret")
	}
	return out.Data.RecoveryFile, nil
}

// Wallet reads one wallet.
func (a *Admin) Wallet(ctx context.Context, id string) (WalletInfo, error) {
	if !isUUID(id) {
		return WalletInfo{}, fmt.Errorf("circle wallet: %q is not a wallet id (UUID)", id)
	}
	return a.api.wallet(ctx, id)
}

// WalletsByRef lists the wallets on blockchain whose refId is ref.
func (a *Admin) WalletsByRef(ctx context.Context, blockchain, ref string) ([]WalletInfo, error) {
	q := url.Values{"blockchain": {blockchain}, "refId": {ref}, "pageSize": {"50"}}
	var out struct {
		Data struct {
			Wallets []WalletInfo `json:"wallets"`
		} `json:"data"`
	}
	if err := a.api.call(ctx, http.MethodGet, "/v1/w3s/wallets?"+q.Encode(), nil, &out); err != nil {
		return nil, fmt.Errorf("circle wallet: listing %s wallets with refId %q: %w", blockchain, ref, err)
	}
	// Filter again: a server that ignored a query parameter must not hand back a stranger.
	var mine []WalletInfo
	for _, w := range out.Data.Wallets {
		if w.Blockchain == blockchain && w.RefID == ref {
			mine = append(mine, w)
		}
	}
	return mine, nil
}

// Balances reads a wallet's token balances as Circle sees them.
func (a *Admin) Balances(ctx context.Context, id string) ([]TokenBalance, error) {
	var out struct {
		Data struct {
			TokenBalances []TokenBalance `json:"tokenBalances"`
		} `json:"data"`
	}
	if err := a.api.call(ctx, http.MethodGet, "/v1/w3s/wallets/"+id+"/balances", nil, &out); err != nil {
		return nil, fmt.Errorf("circle wallet: balances of %s: %w", id, err)
	}
	return out.Data.TokenBalances, nil
}

func (a *Admin) createWalletSet(ctx context.Context, secret []byte, name string) (string, error) {
	pub, err := a.api.publicKey(ctx)
	if err != nil {
		return "", err
	}
	ct, err := encryptSecret(pub, secret)
	if err != nil {
		return "", err
	}
	key, err := newUUIDv4()
	if err != nil {
		return "", err
	}
	var out struct {
		Data struct {
			WalletSet struct {
				ID string `json:"id"`
			} `json:"walletSet"`
		} `json:"data"`
	}
	req := map[string]any{"idempotencyKey": key, "name": name, "entitySecretCiphertext": ct}
	if err := a.api.call(ctx, http.MethodPost, "/v1/w3s/developer/walletSets", req, &out); err != nil {
		return "", fmt.Errorf("circle wallet: creating wallet set %q: %w", name, err)
	}
	if !isUUID(out.Data.WalletSet.ID) {
		return "", fmt.Errorf("circle wallet: creating wallet set %q returned no wallet set id", name)
	}
	return out.Data.WalletSet.ID, nil
}

func (a *Admin) createWallet(ctx context.Context, secret []byte, setID, blockchain string, role Role) (WalletInfo, error) {
	pub, err := a.api.publicKey(ctx)
	if err != nil {
		return WalletInfo{}, err
	}
	ct, err := encryptSecret(pub, secret)
	if err != nil {
		return WalletInfo{}, err
	}
	key, err := newUUIDv4()
	if err != nil {
		return WalletInfo{}, err
	}
	var out struct {
		Data struct {
			Wallets []WalletInfo `json:"wallets"`
		} `json:"data"`
	}
	req := map[string]any{
		"idempotencyKey":         key,
		"walletSetId":            setID,
		"blockchains":            []string{blockchain},
		"count":                  1,
		"accountType":            "EOA", // see the package doc: never an SCA
		"metadata":               []map[string]string{{"name": role.Name, "refId": role.RefID}},
		"entitySecretCiphertext": ct,
	}
	if err := a.api.call(ctx, http.MethodPost, "/v1/w3s/developer/wallets", req, &out); err != nil {
		return WalletInfo{}, fmt.Errorf("circle wallet: creating the %s wallet: %w", role.Name, err)
	}
	if len(out.Data.Wallets) != 1 {
		return WalletInfo{}, fmt.Errorf("circle wallet: creating the %s wallet returned %d wallets, want 1", role.Name, len(out.Data.Wallets))
	}
	if !isUUID(out.Data.Wallets[0].ID) {
		return WalletInfo{}, fmt.Errorf("circle wallet: creating the %s wallet returned id %q, not a wallet id", role.Name, out.Data.Wallets[0].ID)
	}
	return out.Data.Wallets[0], nil
}

// Role is one wallet Snapfall needs, found again by its refId on every run.
type Role struct {
	Name  string // human label, also the wallet's Circle name
	RefID string // stable key; one wallet per (blockchain, refId)
	Env   string // the variable the wallet's id goes into
}

// Roles are the two developer-controlled wallets Snapfall runs on: the operator treasury
// (daemon advances, sidecar x402 signer) and the self-facilitator's gas relayer.
var Roles = []Role{
	{Name: "snapfall-treasury", RefID: "snapfall-treasury", Env: EnvWalletID},
	{Name: "snapfall-relayer", RefID: "snapfall-relayer", Env: "CIRCLE_BROADCASTER_WALLET_ID"},
}

// Provisioned is one role's wallet after EnsureWallets.
type Provisioned struct {
	Role    Role
	Wallet  WalletInfo
	Created bool
}

// EnsureWallets finds or creates one EOA per role on blockchain. Re-running it is safe: a
// role whose refId already names a wallet reuses it (after the same checks the lane makes),
// new wallets join the existing wallets' set, and a new set is created only when none of
// the roles has a wallet yet. Two wallets under one refId is ambiguity it refuses to guess.
func (a *Admin) EnsureWallets(ctx context.Context, secret []byte, blockchain string, roles []Role) ([]Provisioned, error) {
	if len(secret) != 32 {
		return nil, fmt.Errorf("circle wallet: the entity secret must be 32 bytes")
	}
	found := make([]*WalletInfo, len(roles))
	setID := ""
	for i, r := range roles {
		ws, err := a.WalletsByRef(ctx, blockchain, r.RefID)
		if err != nil {
			return nil, err
		}
		switch len(ws) {
		case 0:
			continue
		case 1:
		default:
			return nil, fmt.Errorf("circle wallet: %d %s wallets carry refId %q; resolve that in the console first", len(ws), blockchain, r.RefID)
		}
		if err := CheckLane(ws[0], blockchain); err != nil {
			return nil, fmt.Errorf("existing %s wallet: %w", r.Name, err)
		}
		found[i] = &ws[0]
		if setID == "" {
			setID = ws[0].WalletSetID
		}
	}
	var out []Provisioned
	for i, r := range roles {
		if found[i] != nil {
			out = append(out, Provisioned{Role: r, Wallet: *found[i]})
			continue
		}
		if setID == "" {
			id, err := a.createWalletSet(ctx, secret, "snapfall")
			if err != nil {
				return out, err
			}
			setID = id
		}
		w, err := a.createWallet(ctx, secret, setID, blockchain, r)
		if err != nil {
			return out, err
		}
		if err := CheckLane(w, blockchain); err != nil {
			return out, fmt.Errorf("new %s wallet: %w", r.Name, err)
		}
		out = append(out, Provisioned{Role: r, Wallet: w, Created: true})
	}
	return out, nil
}

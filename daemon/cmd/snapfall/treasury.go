package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/gnanam1990/snapfall/daemon/internal/chain"
	"github.com/gnanam1990/snapfall/daemon/internal/circlewallet"
)

// chainSigner is what a lane needs from a signer. *chain.Client (a raw key from the
// environment) and *circlewallet.Wallet (a Circle developer-controlled wallet) both satisfy
// it, with the same receipt contract: mined-but-reverted is Reverted=true, never success.
type chainSigner interface {
	Submit(ctx context.Context, to common.Address, calldata []byte) (chain.Receipt, error)
	Address() common.Address
}

// openTreasury chooses the operator's signer. CIRCLE_TREASURY_WALLET_ID present selects the
// Circle lane; otherwise TREASURY_PRIVATE_KEY is used, exactly as before. Either way the
// returned reader is a plain RPC client for views and receipts.
//
// The Circle lane does not fall back to the raw key when it fails to open: a daemon told to
// sign through Circle that quietly signed with a shell-file key instead would be the exact
// surprise this lane exists to remove. A failure here wires no lane, and advances stop
// loudly at advance.pending_chain.
//
// TREASURY_PRIVATE_KEY may legitimately still be set alongside the Circle variables, because
// the sidecar's x402 buyer reads it. The daemon then warns and ignores it rather than
// refusing, but the warning is the reminder that the raw key has not left the machine.
func openTreasury(ctx context.Context, log *slog.Logger, rpcURL string, chainID uint64) (chainSigner, *chain.Client, string, error) {
	if v, ok := os.LookupEnv(circlewallet.EnvWalletID); !ok || strings.TrimSpace(v) == "" {
		c, err := chain.NewFromEnv("TREASURY_PRIVATE_KEY", rpcURL, chainID)
		if err != nil {
			return nil, nil, "", err
		}
		return c, c, "raw key", nil
	}
	reader, err := chain.NewReadOnly(rpcURL, chainID)
	if err != nil {
		return nil, nil, "", err
	}
	w, err := circlewallet.NewFromEnv(chainID, reader)
	if err != nil {
		return nil, nil, "", err
	}
	octx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := w.Open(octx); err != nil {
		return nil, nil, "", fmt.Errorf("%w (not falling back to TREASURY_PRIVATE_KEY)", err)
	}
	if v, ok := os.LookupEnv("TREASURY_PRIVATE_KEY"); ok && strings.TrimSpace(v) != "" {
		log.Warn("TREASURY_PRIVATE_KEY is also set; the daemon ignores it and signs through the Circle wallet, but the sidecar's x402 buyer still reads it")
	}
	return w, reader, "circle developer-controlled wallet", nil
}

// treasuryAddressMismatch reports whether SNAPFALL_TREASURY_ADDRESS (the org address the
// owner API reads rates and advances for) names a different address from the one that
// actually signs. Moving the treasury to a Circle wallet changes the operator address, and a
// stale value would show the old org's numbers with nothing saying so. Unset is not a
// mismatch: the owner API already handles a missing org address on its own.
func treasuryAddressMismatch(signer common.Address) (string, bool) {
	v := strings.TrimSpace(os.Getenv("SNAPFALL_TREASURY_ADDRESS"))
	if v == "" {
		return "", false
	}
	if !common.IsHexAddress(v) || common.HexToAddress(v) != signer {
		return v, true
	}
	return v, false
}

// Command testnet-ops checks and optionally funds every wallet in the Arc deployment handoff.
package main

import (
	"context"
	"flag"
	"fmt"
	"math/big"
	"os"
	"sort"
	"strings"

	"github.com/gnanam1990/snapfall/daemon/internal/chaincfg"
	"github.com/gnanam1990/snapfall/daemon/internal/testnetops"
)

const faucetURL = "https://faucet.circle.com"

func main() {
	deploymentPath := flag.String("deployment", "../deployments/arc-testnet.json", "deployment artifact")
	fund := flag.Bool("fund", false, "top up deficient wallets using a named Foundry keystore account")
	funderAccount := flag.String("funder-account", os.Getenv("SNAPFALL_FUNDER_ACCOUNT"), "Foundry keystore account name")
	customerMinimum := flag.String("customer-min", envOr("SNAPFALL_CUSTOMER_MIN_USDC", "25.10"), "external customer minimum native USDC")
	treasuryMinimum := flag.String("treasury-min", envOr("SNAPFALL_TREASURY_MIN_USDC", "0"), "operator treasury minimum native USDC")
	funderReserve := flag.String("funder-reserve", envOr("SNAPFALL_FUNDER_RESERVE_USDC", "0.25"), "native USDC retained by the funder")
	flag.Parse()

	if err := run(
		context.Background(), *deploymentPath, *fund, *funderAccount,
		*customerMinimum, *treasuryMinimum, *funderReserve,
	); err != nil {
		fmt.Fprintln(os.Stderr, "testnet ops:", err)
		os.Exit(1)
	}
}

func run(
	ctx context.Context,
	deploymentPath string,
	fund bool,
	funderAccount string,
	customerMinimum string,
	treasuryMinimum string,
	funderReserve string,
) error {
	deployment, err := chaincfg.Load(deploymentPath, os.LookupEnv)
	if err != nil {
		return err
	}
	minimums, err := walletMinimums(customerMinimum, treasuryMinimum)
	if err != nil {
		return err
	}
	reserve, err := testnetops.ParseUSDC(funderReserve)
	if err != nil {
		return fmt.Errorf("funder reserve: %w", err)
	}

	roles := make([]string, 0, len(deployment.WalletAddresses))
	for role := range deployment.WalletAddresses {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	wallets := make([]testnetops.Wallet, 0, len(roles))
	for _, role := range roles {
		minimum, ok := minimums[role]
		if !ok {
			return fmt.Errorf("no minimum configured for funded wallet role %q", role)
		}
		wallets = append(wallets, testnetops.Wallet{
			Role: role, Address: deployment.WalletAddresses[role], Minimum: minimum,
		})
	}

	source, err := testnetops.NewRPCClient(deployment.Network.RPCURL, nil)
	if err != nil {
		return err
	}
	var funder testnetops.Funder
	if fund {
		funder, err = testnetops.NewCastFunder(funderAccount, deployment.Network.RPCURL)
		if err != nil {
			return err
		}
	}
	report, err := testnetops.EnsureWallets(
		ctx, source, deployment.Network.ChainID, wallets, funder, reserve,
	)
	if err != nil {
		return err
	}

	fmt.Printf("Arc testnet wallet health (chain %d)\n", deployment.Network.ChainID)
	for _, status := range report.Wallets {
		state := "HEALTHY"
		if status.After.Cmp(status.Minimum) < 0 {
			state = "LOW"
		}
		fmt.Printf(
			"  %-18s %-7s %s / %s USDC  %s\n",
			status.Role, state,
			testnetops.FormatUSDC(status.After), testnetops.FormatUSDC(status.Minimum), status.Address,
		)
		if status.Funding != nil {
			fmt.Printf("    topped up %s USDC\n", testnetops.FormatUSDC(status.Funding.Amount))
		}
	}
	if !report.Healthy {
		return fmt.Errorf(
			"wallets are below minimum; rerun with --fund --funder-account <keystore-name>, "+
				"or use the human-only faucet at %s (observed: 20 USDC per claim, about 2h cooldown)",
			faucetURL,
		)
	}
	fmt.Println("All configured wallets are healthy.")
	return nil
}

func walletMinimums(customer, treasury string) (map[string]*big.Int, error) {
	customerAmount, err := testnetops.ParseUSDC(customer)
	if err != nil {
		return nil, fmt.Errorf("customer minimum: %w", err)
	}
	treasuryAmount, err := testnetops.ParseUSDC(treasury)
	if err != nil {
		return nil, fmt.Errorf("treasury minimum: %w", err)
	}
	return map[string]*big.Int{
		"externalCustomer": customerAmount,
		"operatorTreasury": treasuryAmount,
	}, nil
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

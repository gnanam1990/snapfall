// Command redeploy-testnet performs a cadence-guarded Arc testnet deployment.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/chaincfg"
	"github.com/gnanam1990/snapfall/daemon/internal/testnetops"
)

func main() {
	deploymentPath := flag.String("deployment", "../deployments/arc-testnet.json", "current deployment artifact")
	contractsRoot := flag.String("contracts-root", "../contracts", "Foundry project root")
	account := flag.String("account", os.Getenv("SNAPFALL_DEPLOYER_ACCOUNT"), "Foundry keystore account name")
	flag.Parse()

	if err := run(context.Background(), *deploymentPath, *contractsRoot, *account, time.Now()); err != nil {
		fmt.Fprintln(os.Stderr, "redeploy testnet:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, deploymentPath, contractsRoot, account string, now time.Time) error {
	account = strings.TrimSpace(account)
	if account == "" {
		return fmt.Errorf("deployer account is required; import one with cast wallet import")
	}
	deployment, err := chaincfg.Load(deploymentPath, os.LookupEnv)
	if err != nil {
		return err
	}
	source, err := testnetops.NewRPCClient(deployment.Network.RPCURL, nil)
	if err != nil {
		return err
	}
	chainID, err := source.ChainID(ctx)
	if err != nil {
		return err
	}
	if chainID != deployment.Network.ChainID {
		return fmt.Errorf("RPC chain ID %d does not match deployment chain ID %d", chainID, deployment.Network.ChainID)
	}
	if err := testnetops.CheckRedeployCadence(
		ctx, source, deployment.Network.StartBlock, now, 48*time.Hour,
	); err != nil {
		return err
	}

	command := exec.CommandContext(
		ctx,
		"forge", "script", "script/Deploy.s.sol:Deploy",
		"--root", contractsRoot,
		"--rpc-url", deployment.Network.RPCURL,
		"--account", account,
		"--broadcast",
	)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("forge deployment: %w", err)
	}
	fmt.Println("Deployment broadcast. Verify the addresses, then update deployments/arc-testnet.json.")
	return nil
}

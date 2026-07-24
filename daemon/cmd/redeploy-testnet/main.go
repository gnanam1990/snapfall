// Command redeploy-testnet performs a cadence-guarded Arc testnet deployment.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/chaincfg"
	"github.com/gnanam1990/snapfall/daemon/internal/testnetops"
)

const arcTestnetChainID uint64 = 5042002

func main() {
	deploymentPath := flag.String("deployment", "../deployments/arc-testnet.json", "current deployment artifact")
	contractsRoot := flag.String("contracts-root", "../contracts", "Foundry project root")
	account := flag.String("account", os.Getenv("SNAPFALL_DEPLOYER_ACCOUNT"), "Foundry keystore account name")
	flag.Parse()

	if err := run(context.Background(), *deploymentPath, *contractsRoot, *account); err != nil {
		fmt.Fprintln(os.Stderr, "redeploy testnet:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, deploymentPath, contractsRoot, account string) (runErr error) {
	account = strings.TrimSpace(account)
	if account == "" {
		return fmt.Errorf("deployer account is required; import one with cast wallet import")
	}
	deployment, err := chaincfg.Load(deploymentPath, os.LookupEnv)
	if err != nil {
		return err
	}
	if deployment.Network.ChainID != arcTestnetChainID {
		return fmt.Errorf(
			"deployment artifact chain ID %d is not Arc testnet %d",
			deployment.Network.ChainID, arcTestnetChainID,
		)
	}
	source, err := testnetops.NewRPCClient(deployment.Network.RPCURL, nil)
	if err != nil {
		return err
	}
	chainID, err := source.ChainID(ctx)
	if err != nil {
		return err
	}
	if chainID != arcTestnetChainID {
		return fmt.Errorf("RPC chain ID %d is not Arc testnet %d", chainID, arcTestnetChainID)
	}
	guardPath := deploymentPath + ".redeploy-guard.json"
	reservation, err := testnetops.AcquireRedeployReservation(
		guardPath+".pending", arcTestnetChainID,
	)
	if err != nil {
		return err
	}
	releaseBeforeBroadcast := true
	defer func() {
		if releaseBeforeBroadcast {
			if err := reservation.Release(); err != nil {
				runErr = errors.Join(runErr, err)
			}
		}
	}()
	lastBroadcastAt, err := testnetops.ReadRedeployMarker(guardPath, arcTestnetChainID)
	if err != nil {
		return err
	}
	if err := testnetops.CheckRedeployCadence(
		ctx, source, deployment.Network.StartBlock, lastBroadcastAt, 48*time.Hour,
	); err != nil {
		return err
	}
	signer, err := testnetops.NewCastFunder(account, deployment.Network.RPCURL)
	if err != nil {
		return err
	}
	sender, err := signer.Address(ctx)
	if err != nil {
		return fmt.Errorf("resolving deployment sender: %w", err)
	}

	command := exec.CommandContext(
		ctx,
		"forge", "script", "script/Deploy.s.sol:Deploy",
		"--root", contractsRoot,
		"--rpc-url", deployment.Network.RPCURL,
		"--account", account,
		"--sender", sender,
		"--broadcast",
	)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := runForgeCommand(command, func() { releaseBeforeBroadcast = false }); err != nil {
		if releaseBeforeBroadcast {
			return err
		}
		return fmt.Errorf(
			"%w; pending reservation retained because broadcast status may be ambiguous",
			err,
		)
	}
	broadcastAt, err := source.LatestBlockTimestamp(ctx)
	if err != nil {
		return fmt.Errorf("deployment broadcast succeeded but reading chain time for the redeploy guard failed: %w", err)
	}
	if err := testnetops.WriteRedeployMarker(guardPath, arcTestnetChainID, broadcastAt); err != nil {
		return fmt.Errorf("deployment broadcast succeeded but the redeploy guard was not recorded: %w", err)
	}
	if err := reservation.Release(); err != nil {
		return fmt.Errorf("deployment guard recorded but pending reservation could not be removed: %w", err)
	}
	fmt.Println("Deployment broadcast. Verify the addresses, then update deployments/arc-testnet.json.")
	return nil
}

func runForgeCommand(command *exec.Cmd, markStarted func()) error {
	if err := command.Start(); err != nil {
		return fmt.Errorf("starting forge deployment: %w", err)
	}
	markStarted()
	if err := command.Wait(); err != nil {
		return fmt.Errorf("forge deployment: %w", err)
	}
	return nil
}

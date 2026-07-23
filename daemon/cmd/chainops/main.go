// Command chainops drives the customer/ops half of the on-chain lifecycle — the calls
// that are NOT the daemon's to make: pool seeding (LP), customer funding, job creation,
// work/delivery progression. Every submission prints its transaction hash, explorer
// link, gas used, and — loudly — REVERTED when a receipt carries status 0.
//
// Keys come from env only (-key-env names the variable), format-validated by the chain
// client, never logged. Run this from the shell that holds the keys.
package main

import (
	"context"
	"flag"
	"fmt"
	"math/big"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/common"

	"github.com/gnanam1990/snapfall/daemon/internal/chain"
	"github.com/gnanam1990/snapfall/daemon/internal/chaincfg"
)

func main() {
	deployment := flag.String("deployment", "deployments/arc-testnet.json", "chain deployment config")
	keyEnv := flag.String("key-env", "TREASURY_PRIVATE_KEY", "environment variable holding the signing key")
	flag.Parse()
	if err := run(*deployment, *keyEnv, flag.Args()); err != nil {
		fmt.Fprintln(os.Stderr, "chainops:", err)
		os.Exit(1)
	}
}

func run(deployment, keyEnv string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf(`usage: chainops [-key-env VAR] <command> ...
  seed-pool <usdc>                                approve + deposit into FloatPool (LP)
  transfer <to> <usdc>                            USDC transfer (e.g. gas+funds to the customer wallet)
  create-job <jobid32> <customer> <usdc> <budget> JobVault.createJob (operator)
  fund-job <jobid32> <usdc>                       approve + JobVault.fund (CUSTOMER key)
  start-work <jobid32>                            JobVault.startWork (operator; AFTER the advance)
  record-expense <jobid32> <usdc> <receipt32>     JobVault.recordExpense (operator)
  submit-delivery <jobid32> <hash32>              JobVault.submitDelivery (operator)
  job-status <jobid32>                            read JobVault state (no key use)
  advance-of <jobid32>                            read FloatPool.openAdvanceOf (no key use)`)
	}
	dep, err := chaincfg.Load(deployment, os.LookupEnv)
	if err != nil {
		return err
	}
	client, err := chain.NewFromEnv(keyEnv, dep.Network.RPCURL, dep.Network.ChainID)
	if err != nil {
		return err
	}
	fp := common.HexToAddress(dep.Contracts.FloatPool.Address)
	jv := common.HexToAddress(dep.Contracts.JobVault.Address)
	usdc := common.HexToAddress(dep.Contracts.USDC.Address)
	ctx := context.Background()
	explorer := strings.TrimSuffix(dep.Network.ExplorerURL, "/")

	show := func(label string, r chain.Receipt, err error) error {
		if err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		verdict := "OK"
		if r.Reverted {
			verdict = "*** REVERTED (mined and failed — this action did NOT happen) ***"
		}
		fmt.Printf("%-16s %s\n  tx   %s/tx/%s\n  block %d  gas %d\n", label, verdict, explorer, r.TxHash, r.Block, r.GasUsed)
		return nil
	}

	cmd := args[0]
	arg := func(i int) string {
		if len(args) <= i {
			fmt.Fprintln(os.Stderr, "missing argument")
			os.Exit(2)
		}
		return args[i]
	}

	switch cmd {
	case "seed-pool":
		amt, err := usdcMicros(arg(1))
		if err != nil {
			return err
		}
		r, err := client.Submit(ctx, usdc, chain.CalldataApprove(fp, amt))
		if err := show("approve", r, err); err != nil {
			return err
		}
		r, err = client.Submit(ctx, fp, chain.CalldataDeposit(amt, client.Address()))
		return show("deposit", r, err)
	case "transfer":
		amt, err := usdcMicros(arg(2))
		if err != nil {
			return err
		}
		r, err := client.Submit(ctx, usdc, chain.CalldataTransfer(common.HexToAddress(arg(1)), amt))
		return show("transfer", r, err)
	case "create-job":
		id, err := chain.JobID32(arg(1))
		if err != nil {
			return err
		}
		pay, err := usdcMicros(arg(3))
		if err != nil {
			return err
		}
		budget, err := usdcMicros(arg(4))
		if err != nil {
			return err
		}
		r, err := client.Submit(ctx, jv, chain.CalldataCreateJob(id, common.HexToAddress(arg(2)), client.Address(), pay, budget, keccakHash("snapfall-demo-terms"), 1800000000))
		return show("create-job", r, err)
	case "fund-job":
		id, err := chain.JobID32(arg(1))
		if err != nil {
			return err
		}
		pay, err := usdcMicros(arg(2))
		if err != nil {
			return err
		}
		r, err := client.Submit(ctx, usdc, chain.CalldataApprove(jv, pay))
		if err := show("approve", r, err); err != nil {
			return err
		}
		r, err = client.Submit(ctx, jv, chain.CalldataFund(id))
		return show("fund", r, err)
	case "start-work":
		id, err := chain.JobID32(arg(1))
		if err != nil {
			return err
		}
		r, err := client.Submit(ctx, jv, chain.CalldataStartWork(id))
		return show("start-work", r, err)
	case "record-expense":
		id, err := chain.JobID32(arg(1))
		if err != nil {
			return err
		}
		amt, err := usdcMicros(arg(2))
		if err != nil {
			return err
		}
		receipt, err := chain.JobID32(arg(3))
		if err != nil {
			return err
		}
		r, err := client.Submit(ctx, jv, chain.CalldataRecordExpense(id, amt, receipt))
		return show("record-expense", r, err)
	case "submit-delivery":
		id, err := chain.JobID32(arg(1))
		if err != nil {
			return err
		}
		h, err := chain.JobID32(arg(2))
		if err != nil {
			return err
		}
		r, err := client.Submit(ctx, jv, chain.CalldataSubmitDelivery(id, h))
		return show("submit-delivery", r, err)
	case "job-status":
		id, err := chain.JobID32(arg(1))
		if err != nil {
			return err
		}
		ret, err := client.CallView(ctx, jv, chain.CalldataJobStatus(id))
		if err != nil {
			return err
		}
		st, err := chain.DecodeJobStatus(ret)
		if err != nil {
			return err
		}
		names := []string{"Created", "Funded", "InProgress", "Delivered", "Accepted", "Refunded", "Cancelled"}
		name := "?"
		if int(st) < len(names) {
			name = names[st]
		}
		fmt.Printf("job-status: %d (%s)\n", st, name)
		return nil
	case "advance-of":
		id, err := chain.JobID32(arg(1))
		if err != nil {
			return err
		}
		ret, err := client.CallView(ctx, fp, chain.CalldataOpenAdvanceOf(id))
		if err != nil {
			return err
		}
		p, fee, open, err := chain.DecodeOpenAdvance(ret)
		if err != nil {
			return err
		}
		fmt.Printf("advance-of: principal=%s fee=%s open=%v\n", p, fee, open)
		return nil
	default:
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func keccakHash(s string) [32]byte {
	var out [32]byte
	sum, _ := chain.JobID32("0x" + fmt.Sprintf("%064x", big.NewInt(0).SetBytes([]byte(s))))
	copy(out[:], sum[:])
	return out
}

// usdcMicros parses a human USDC amount into 6dp micros, exactly — no floats.
func usdcMicros(v string) (*big.Int, error) {
	parts := strings.SplitN(strings.TrimSpace(v), ".", 2)
	whole, ok := new(big.Int).SetString(parts[0], 10)
	if !ok || whole.Sign() < 0 {
		return nil, fmt.Errorf("invalid USDC amount %q", v)
	}
	out := new(big.Int).Mul(whole, big.NewInt(1_000_000))
	if len(parts) == 2 {
		frac := parts[1]
		if frac == "" || len(frac) > 6 {
			return nil, fmt.Errorf("invalid USDC amount %q", v)
		}
		f, ok := new(big.Int).SetString(frac+strings.Repeat("0", 6-len(frac)), 10)
		if !ok {
			return nil, fmt.Errorf("invalid USDC amount %q", v)
		}
		out.Add(out, f)
	}
	return out, nil
}

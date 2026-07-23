package testnetops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"
)

// RPCClient reads wallet balances directly from an EVM JSON-RPC endpoint.
type RPCClient struct {
	url    string
	client *http.Client
}

// NewRPCClient creates a bounded Arc RPC client.
func NewRPCClient(url string, client *http.Client) (*RPCClient, error) {
	url = strings.TrimSpace(url)
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("RPC URL must be http(s), got %q", url)
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	return &RPCClient{url: url, client: client}, nil
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (r *RPCClient) call(ctx context.Context, method string, params any, out any) error {
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: 1, Method: method, Params: params})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	response, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("Arc RPC %s: %w", method, err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("reading Arc RPC %s: %w", method, err)
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Arc RPC %s returned HTTP %d: %s", method, response.StatusCode, strings.TrimSpace(string(raw)))
	}
	var reply rpcResponse
	if err := json.Unmarshal(raw, &reply); err != nil {
		return fmt.Errorf("decoding Arc RPC %s: %w", method, err)
	}
	if reply.Error != nil {
		return fmt.Errorf("Arc RPC %s error %d: %s", method, reply.Error.Code, reply.Error.Message)
	}
	if len(reply.Result) == 0 || string(reply.Result) == "null" {
		return fmt.Errorf("Arc RPC %s returned no result", method)
	}
	if err := json.Unmarshal(reply.Result, out); err != nil {
		return fmt.Errorf("decoding Arc RPC %s result: %w", method, err)
	}
	return nil
}

// ChainID returns the connected network's EIP-155 chain ID.
func (r *RPCClient) ChainID(ctx context.Context) (uint64, error) {
	var encoded string
	if err := r.call(ctx, "eth_chainId", []any{}, &encoded); err != nil {
		return 0, err
	}
	value, err := parseHexBig(encoded)
	if err != nil || !value.IsUint64() {
		return 0, fmt.Errorf("invalid chain ID %q", encoded)
	}
	return value.Uint64(), nil
}

// Balance returns one address's Arc native USDC balance in 18-decimal native units.
func (r *RPCClient) Balance(ctx context.Context, address string) (*big.Int, error) {
	var encoded string
	if err := r.call(ctx, "eth_getBalance", []any{address, "latest"}, &encoded); err != nil {
		return nil, err
	}
	return parseHexBig(encoded)
}

// BlockTimestamp returns the UTC timestamp recorded on one Arc block.
func (r *RPCClient) BlockTimestamp(ctx context.Context, block uint64) (time.Time, error) {
	var result struct {
		Timestamp string `json:"timestamp"`
	}
	if err := r.call(ctx, "eth_getBlockByNumber", []any{fmt.Sprintf("0x%x", block), false}, &result); err != nil {
		return time.Time{}, err
	}
	seconds, err := parseHexBig(result.Timestamp)
	if err != nil || !seconds.IsInt64() {
		return time.Time{}, fmt.Errorf("invalid block timestamp %q", result.Timestamp)
	}
	return time.Unix(seconds.Int64(), 0).UTC(), nil
}

func parseHexBig(value string) (*big.Int, error) {
	value = strings.TrimSpace(value)
	if len(value) < 3 || !strings.HasPrefix(value, "0x") {
		return nil, fmt.Errorf("%q is not 0x hex", value)
	}
	parsed, ok := new(big.Int).SetString(value[2:], 16)
	if !ok {
		return nil, fmt.Errorf("invalid hex quantity %q", value)
	}
	return parsed, nil
}

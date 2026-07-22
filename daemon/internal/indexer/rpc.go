package indexer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RPCSource is the production Arc JSON-RPC adapter. It deliberately implements polling only;
// the durable cursor makes polling and restart replay the same code path.
type RPCSource struct {
	url    string
	client *http.Client
}

// NewRPCSource constructs the production adapter. A nil client receives a bounded default.
func NewRPCSource(url string, client *http.Client) (*RPCSource, error) {
	url = strings.TrimSpace(url)
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("RPC URL must be http(s), got %q", url)
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	return &RPCSource{url: url, client: client}, nil
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (r *RPCSource) call(ctx context.Context, method string, params any, out any) error {
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: 1, Method: method, Params: params})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	res, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("Arc RPC %s: %w", method, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return fmt.Errorf("reading Arc RPC %s: %w", method, err)
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("Arc RPC %s returned HTTP %d: %s", method, res.StatusCode, strings.TrimSpace(string(raw)))
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

// Head returns the current Arc block number.
func (r *RPCSource) Head(ctx context.Context) (uint64, error) {
	var encoded string
	if err := r.call(ctx, "eth_blockNumber", []any{}, &encoded); err != nil {
		return 0, err
	}
	return parseHexUint(encoded)
}

// ChainID lets the indexer fail closed before reading from an RPC endpoint configured for a
// different network.
func (r *RPCSource) ChainID(ctx context.Context) (uint64, error) {
	var encoded string
	if err := r.call(ctx, "eth_chainId", []any{}, &encoded); err != nil {
		return 0, err
	}
	return parseHexUint(encoded)
}

type rpcLog struct {
	Address         string   `json:"address"`
	Topics          []string `json:"topics"`
	Data            string   `json:"data"`
	BlockNumber     string   `json:"blockNumber"`
	BlockHash       string   `json:"blockHash"`
	TransactionHash string   `json:"transactionHash"`
	LogIndex        string   `json:"logIndex"`
	Removed         bool     `json:"removed"`
}

// Logs returns every contract log in an inclusive block range. Topic filtering happens inside
// the module so unknown AuditAnchor logs can still be retained as raw evidence.
func (r *RPCSource) Logs(ctx context.Context, filter Filter) ([]Log, error) {
	if filter.ToBlock < filter.FromBlock {
		return nil, fmt.Errorf("invalid log range %d..%d", filter.FromBlock, filter.ToBlock)
	}
	params := []any{map[string]any{
		"fromBlock": fmt.Sprintf("0x%x", filter.FromBlock),
		"toBlock":   fmt.Sprintf("0x%x", filter.ToBlock),
		"address":   filter.Addresses,
	}}
	var raw []rpcLog
	if err := r.call(ctx, "eth_getLogs", params, &raw); err != nil {
		return nil, err
	}
	logs := make([]Log, 0, len(raw))
	for i, item := range raw {
		block, err := parseHexUint(item.BlockNumber)
		if err != nil {
			return nil, fmt.Errorf("log %d blockNumber: %w", i, err)
		}
		index, err := parseHexUint(item.LogIndex)
		if err != nil {
			return nil, fmt.Errorf("log %d logIndex: %w", i, err)
		}
		address, err := normalizeAddress(item.Address)
		if err != nil {
			return nil, fmt.Errorf("log %d address: %w", i, err)
		}
		topics := make([]string, len(item.Topics))
		for j, topic := range item.Topics {
			topics[j] = strings.ToLower(topic)
		}
		logs = append(logs, Log{
			Address: address, Topics: topics, Data: strings.ToLower(item.Data),
			BlockNumber: block, BlockHash: strings.ToLower(item.BlockHash),
			TransactionHash: strings.ToLower(item.TransactionHash), LogIndex: index, Removed: item.Removed,
		})
	}
	return logs, nil
}

func parseHexUint(v string) (uint64, error) {
	if len(v) < 3 || !strings.HasPrefix(v, "0x") {
		return 0, fmt.Errorf("%q is not 0x hex", v)
	}
	n, err := strconv.ParseUint(v[2:], 16, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing %q: %w", v, err)
	}
	return n, nil
}

package indexer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestRPCSourceImplementsArcPollingSeam(t *testing.T) {
	var methods []string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return nil, err
		}
		methods = append(methods, req.Method)
		switch req.Method {
		case "eth_chainId":
			return jsonResponse(`{"jsonrpc":"2.0","id":1,"result":"0x4cef52"}`), nil
		case "eth_blockNumber":
			return jsonResponse(`{"jsonrpc":"2.0","id":1,"result":"0x6a"}`), nil
		case "eth_getLogs":
			var filter map[string]any
			if len(req.Params) != 1 || json.Unmarshal(req.Params[0], &filter) != nil {
				t.Errorf("bad filter params: %s", req.Params)
			}
			if filter["fromBlock"] != "0x64" || filter["toBlock"] != "0x66" {
				t.Errorf("range filter = %+v", filter)
			}
			return jsonResponse(`{"jsonrpc":"2.0","id":1,"result":[{
			  "address":"0x1111111111111111111111111111111111111111",
			  "topics":["0x8220b978cac568b980751c54df59af3be6c1d3bd9874232210cc1cf89740142b"],
			  "data":"0x00","blockNumber":"0x65","blockHash":"0xabc",
			  "transactionHash":"0xdef","logIndex":"0x2","removed":false
			}]}`), nil
		default:
			return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader("unexpected"))}, nil
		}
	})}

	source, err := NewRPCSource("https://rpc.example", client)
	if err != nil {
		t.Fatal(err)
	}
	chainID, err := source.ChainID(context.Background())
	if err != nil || chainID != testChain {
		t.Fatalf("chain ID = %d, %v", chainID, err)
	}
	head, err := source.Head(context.Background())
	if err != nil || head != 106 {
		t.Fatalf("head = %d, %v", head, err)
	}
	logs, err := source.Logs(context.Background(), Filter{FromBlock: 100, ToBlock: 102, Addresses: []string{vaultAddr}})
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || logs[0].BlockNumber != 101 || logs[0].LogIndex != 2 || logs[0].Address != vaultAddr {
		t.Fatalf("logs = %+v", logs)
	}
	if !reflect.DeepEqual(methods, []string{"eth_chainId", "eth_blockNumber", "eth_getLogs"}) {
		t.Fatalf("methods = %v", methods)
	}
}

func TestRPCSourceSurfacesJSONRPCError(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"range too wide"}}`), nil
	})}
	source, err := NewRPCSource("https://rpc.example", client)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Head(context.Background()); err == nil {
		t.Fatal("expected JSON-RPC error")
	}
}

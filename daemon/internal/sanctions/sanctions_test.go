package sanctions

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

const (
	listed = "0x098B716B8Aaf21512996dC57EB0615e2383E2f96"
	clean  = "0x0000000000000000000000000000000000000001"
	word0  = "0x0000000000000000000000000000000000000000000000000000000000000000"
	word1  = "0x0000000000000000000000000000000000000000000000000000000000000001"
)

// rpcStub answers eth_call with whatever reply returns for the requested calldata, and
// records the calls so a test can assert the exact contract and calldata sent.
func rpcStub(t *testing.T, reply func(data string) (status int, body string)) (*Oracle, *[]map[string]string) {
	t.Helper()
	var calls []map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(raw, &req); err != nil || req.Method != "eth_call" || len(req.Params) != 2 {
			t.Errorf("unexpected request %s", raw)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var call map[string]string
		_ = json.Unmarshal(req.Params[0], &call)
		calls = append(calls, call)
		status, body := reply(call["data"])
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	o := NewOracle(srv.URL)
	return o, &calls
}

func result(word string) string { return `{"jsonrpc":"2.0","id":1,"result":"` + word + `"}` }

func TestSanctionedReadsTheOracleWithExactCalldata(t *testing.T) {
	o, calls := rpcStub(t, func(data string) (int, string) {
		if strings.HasSuffix(data, strings.ToLower(listed[2:])) {
			return 200, result(word1)
		}
		return 200, result(word0)
	})
	ctx := context.Background()

	hit, err := o.Sanctioned(ctx, listed)
	if err != nil || !hit {
		t.Fatalf("listed address: got %v, %v; want true", hit, err)
	}
	ok, err := o.Sanctioned(ctx, clean)
	if err != nil || ok {
		t.Fatalf("clean address: got %v, %v; want false", ok, err)
	}

	got := (*calls)[0]
	if got["to"] != OracleAddress {
		t.Fatalf("called %s, want the oracle %s", got["to"], OracleAddress)
	}
	want := "0xdf592f7d" + strings.Repeat("0", 24) + strings.ToLower(listed[2:])
	if got["data"] != want {
		t.Fatalf("calldata %s, want %s", got["data"], want)
	}
}

// Every way of not knowing is an error, never a quiet "not sanctioned".
func TestSanctionedFailsClosed(t *testing.T) {
	cases := map[string]func(string) (int, string){
		"http error": func(string) (int, string) { return 502, "bad gateway" },
		"rpc error": func(string) (int, string) {
			return 200, `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"limit"}}`
		},
		"no result":       func(string) (int, string) { return 200, `{"jsonrpc":"2.0","id":1}` },
		"empty code":      func(string) (int, string) { return 200, result("0x") },
		"not a bool":      func(string) (int, string) { return 200, result("0x" + strings.Repeat("0", 62) + "02") },
		"not json":        func(string) (int, string) { return 200, "<html>" },
		"too wide a word": func(string) (int, string) { return 200, result(word1 + "00") },
	}
	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			o, _ := rpcStub(t, reply)
			if hit, err := o.Sanctioned(context.Background(), clean); err == nil {
				t.Fatalf("got %v with no error; an unreadable oracle must be an error", hit)
			}
		})
	}
}

func TestSanctionedRefusesANonAddressWithoutCalling(t *testing.T) {
	o, calls := rpcStub(t, func(string) (int, string) { return 200, result(word0) })
	for _, bad := range []string{"", "api.research-data.example", "0x1234", clean + "00"} {
		if _, err := o.Sanctioned(context.Background(), bad); err == nil {
			t.Fatalf("%q was screened; a non-address must be refused", bad)
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("the oracle was called %d times for non-addresses", len(*calls))
	}
}

// TestLiveOracle reads the real oracle on Ethereum mainnet. Opt-in, because it needs the
// network: SNAPFALL_LIVE_SANCTIONS=1 go test ./internal/sanctions -run Live
func TestLiveOracle(t *testing.T) {
	if os.Getenv("SNAPFALL_LIVE_SANCTIONS") != "1" {
		t.Skip("set SNAPFALL_LIVE_SANCTIONS=1 to read the live oracle")
	}
	o := NewOracle(os.Getenv("SNAPFALL_SANCTIONS_RPC"))
	ctx := context.Background()
	if hit, err := o.Sanctioned(ctx, listed); err != nil || !hit {
		t.Fatalf("listed address (Ronin bridge exploiter): got %v, %v; want true", hit, err)
	}
	if hit, err := o.Sanctioned(ctx, clean); err != nil || hit {
		t.Fatalf("clean address: got %v, %v; want false", hit, err)
	}
}

// A keyed RPC carries its key in the URL. No error may repeat it, because the error
// becomes the refusal reason the owner sees and the store keeps.
func TestErrorsNeverQuoteTheRPCURL(t *testing.T) {
	const secret = "s3cr3t-provider-key"
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL + "/v2/" + secret + "?apikey=" + secret
	dead.Close() // connection refused from here on

	for name, rpc := range map[string]string{
		"unreachable": deadURL,
		"unparseable": "http://[::1" + "/v2/" + secret,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewOracle(rpc).Sanctioned(context.Background(), clean)
			if err == nil {
				t.Fatal("expected an error")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("error leaks the RPC URL: %v", err)
			}
		})
	}
}

package chain

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// Every custom error the contracts declare is in the table, and nothing else is. A new error
// that is not named here would surface as bare revert data exactly when an operator needs to
// know which check refused.
func TestContractErrorsMatchSources(t *testing.T) {
	re := regexp.MustCompile(`(?m)^\s*error\s+([A-Za-z0-9_]+)\(\s*\)\s*;`)
	withArgs := regexp.MustCompile(`(?m)^\s*error\s+[A-Za-z0-9_]+\(\s*[^)\s]`)
	declared := map[string]bool{}
	for _, f := range []string{"FloatPool.sol", "JobVault.sol", "AuditAnchor.sol"} {
		src, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "src", f))
		if err != nil {
			t.Skipf("contract source not available (%v)", err)
		}
		if withArgs.Match(src) {
			t.Fatalf("%s declares an error with arguments; RevertName only decodes bare selectors", f)
		}
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			declared[m[1]] = true
		}
	}
	listed := map[string]bool{}
	for _, n := range contractErrors {
		listed[n] = true
	}
	var missing, extra []string
	for n := range declared {
		if !listed[n] {
			missing = append(missing, n)
		}
	}
	for n := range listed {
		if !declared[n] {
			extra = append(extra, n)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing)+len(extra) > 0 {
		t.Fatalf("contractErrors drifted from contracts/src: missing %v, not declared %v", missing, extra)
	}
}

// The node's revert data reaches the error message as the contract's error name, so a refused
// advance says which control refused it. Unknown data is shown raw; a non-hex data field adds nothing.
func TestRPCErrorNamesTheRevert(t *testing.T) {
	sel := hex.EncodeToString(selector("ExposureCapExceeded()"))
	for _, c := range []struct{ data, want, not string }{
		{`"0x` + sel + `"`, "execution reverted: ExposureCapExceeded() (HTTP 200)", ""},
		{`"0xdeadbeef"`, "execution reverted (revert data 0xdeadbeef) (HTTP 200)", ""},
		{`{"reason":"x"}`, "execution reverted (HTTP 200)", "revert data"},
		{`null`, "execution reverted (HTTP 200)", "revert data"},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"error":{"code":3,"message":"execution reverted","data":%s}}`, c.data)
		}))
		cl, err := NewReadOnly(srv.URL, 5042002)
		if err != nil {
			t.Fatal(err)
		}
		_, err = cl.CallViewAt(context.Background(), common.Address{}, []byte{1, 2, 3, 4}, "latest")
		srv.Close()
		if err == nil || !strings.HasSuffix(err.Error(), c.want) || (c.not != "" && strings.Contains(err.Error(), c.not)) {
			t.Fatalf("data %s: error %v, want suffix %q", c.data, err, c.want)
		}
	}
}

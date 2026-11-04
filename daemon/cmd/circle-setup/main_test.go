package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/gnanam1990/snapfall/daemon/internal/circlewallet"
)

// fakeRegistrar records what it was asked to register; no network.
type fakeRegistrar struct {
	env        string
	pingErr    error
	regErr     error
	registered [][]byte
}

func (f *fakeRegistrar) Environment() string        { return f.env }
func (f *fakeRegistrar) Ping(context.Context) error { return f.pingErr }
func (f *fakeRegistrar) RegisterEntitySecret(_ context.Context, s []byte) (string, error) {
	f.registered = append(f.registered, append([]byte(nil), s...))
	if f.regErr != nil {
		return "", f.regErr
	}
	return "RECOVERY", nil
}

// THROWAWAY entropy: 32 bytes of a visible pattern, never a real secret.
func entropy() *bytes.Reader { return bytes.NewReader(bytes.Repeat([]byte{0xab}, 32)) }

const patternHex = "abababababababababababababababababababababababababababababababab"

func newDir(t *testing.T) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), "circle")
	return d
}

func mode(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

// The secret is written 0600 before it is registered, registered as exactly those bytes,
// the recovery file lands 0600 beside it, and the secret never reaches the output.
func TestSecret_WritesRegistersAndNeverPrints(t *testing.T) {
	dir := newDir(t)
	reg := &fakeRegistrar{env: "testnet"}
	var out bytes.Buffer
	if err := setupSecret(context.Background(), &out, reg, "testnet", dir, false, entropy()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "entity-secret.hex"))
	if err != nil || string(raw) != patternHex+"\n" {
		t.Fatalf("secret file = %q, %v", raw, err)
	}
	if len(reg.registered) != 1 || hex.EncodeToString(reg.registered[0]) != patternHex {
		t.Fatalf("registered %x, want the written secret", reg.registered)
	}
	rec, _ := os.ReadFile(filepath.Join(dir, "recovery-file.dat"))
	if string(rec) != "RECOVERY" {
		t.Fatalf("recovery file = %q", rec)
	}
	for _, p := range []string{"entity-secret.hex", "recovery-file.dat"} {
		if m := mode(t, filepath.Join(dir, p)); m != 0o600 {
			t.Fatalf("%s mode %o, want 600", p, m)
		}
	}
	if m := mode(t, dir); m != 0o700 {
		t.Fatalf("dir mode %o, want 700", m)
	}
	if strings.Contains(out.String(), patternHex) || strings.Contains(out.String(), "RECOVERY") {
		t.Fatalf("output shows a secret:\n%s", out.String())
	}
	if !strings.Contains(out.String(), `export CIRCLE_ENTITY_SECRET="$(cat `) {
		t.Fatalf("output does not say how to load the secret:\n%s", out.String())
	}
}

// A failed registration keeps the secret, writes no recovery file, and says how to retry;
// the retry registers the SAME secret instead of minting a new one.
func TestSecret_FailedRegistrationIsRetriedWithTheSameSecret(t *testing.T) {
	dir := newDir(t)
	reg := &fakeRegistrar{env: "testnet", regErr: errors.New("HTTP 500: code 0: upstream")}
	var out bytes.Buffer
	err := setupSecret(context.Background(), &out, reg, "testnet", dir, false, entropy())
	if err == nil || !strings.Contains(err.Error(), "-register-existing") || strings.Contains(err.Error(), patternHex) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "recovery-file.dat")); !os.IsNotExist(err) {
		t.Fatalf("a recovery file exists after a failed registration")
	}
	// Plain rerun refuses to overwrite.
	reg.regErr = nil
	if err := setupSecret(context.Background(), &out, reg, "testnet", dir, false, bytes.NewReader(bytes.Repeat([]byte{0xcd}, 32))); err == nil || !strings.Contains(err.Error(), "never overwrites an entity secret") {
		t.Fatalf("rerun without the flag: %v", err)
	}
	if err := setupSecret(context.Background(), &out, reg, "testnet", dir, true, bytes.NewReader(bytes.Repeat([]byte{0xcd}, 32))); err != nil {
		t.Fatal(err)
	}
	if len(reg.registered) != 2 || !bytes.Equal(reg.registered[0], reg.registered[1]) {
		t.Fatalf("the retry registered a different secret")
	}
	// Once registered and recovered, even the flag refuses.
	if err := setupSecret(context.Background(), &out, reg, "testnet", dir, true, entropy()); err == nil || !strings.Contains(err.Error(), "never overwrites a recovery file") {
		t.Fatalf("after success: %v", err)
	}
}

// Refusals that happen before anything is written or sent.
func TestSecret_RefusesBeforeWriting(t *testing.T) {
	ctx := context.Background()
	var out bytes.Buffer
	cases := []struct {
		name string
		reg  *fakeRegistrar
		net  string
		prep func(dir string)
		flag bool
		want string
	}{
		{"testnet key against mainnet", &fakeRegistrar{env: "testnet"}, "mainnet", nil, false, "is a testnet key but the deployment is Arc mainnet"},
		{"unknown key prefix", &fakeRegistrar{env: ""}, "testnet", nil, false, "cannot tell which Circle environment"},
		{"key rejected", &fakeRegistrar{env: "testnet", pingErr: errors.New("HTTP 401")}, "testnet", nil, false, "nothing was written"},
		{"open directory", &fakeRegistrar{env: "testnet"}, "testnet", func(d string) { _ = os.MkdirAll(d, 0o755); _ = os.Chmod(d, 0o755) }, false, "readable by other users"},
		{"flag without a secret", &fakeRegistrar{env: "testnet"}, "testnet", nil, true, "there is no secret"},
		{"readable secret file", &fakeRegistrar{env: "testnet"}, "testnet", func(d string) {
			_ = os.MkdirAll(d, 0o700)
			_ = os.WriteFile(filepath.Join(d, "entity-secret.hex"), []byte(patternHex), 0o644)
			_ = os.Chmod(filepath.Join(d, "entity-secret.hex"), 0o644)
		}, true, "chmod 600"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := newDir(t)
			if tc.prep != nil {
				tc.prep(dir)
			}
			err := setupSecret(ctx, &out, tc.reg, tc.net, dir, tc.flag, entropy())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if len(tc.reg.registered) != 0 {
				t.Fatalf("something was registered")
			}
			if tc.prep == nil {
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Fatalf("the directory was created before the refusal")
				}
			}
		})
	}
}

// Circle refusing (4xx) means not registered; anything else (timeout, dropped connection,
// 5xx) may have registered it, and the operator is told to check before rerunning. Either
// way no recovery file is left behind and the secret is kept.
func TestSecret_RefusedVersusUnknownRegistration(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"4xx", &circlewallet.HTTPError{Status: 409, Code: 156015, Message: "entity secret has already been set"}, "Circle refused it"},
		{"5xx", &circlewallet.HTTPError{Status: 503, Message: "unavailable"}, "registration state UNKNOWN"},
		{"transport", errors.New("context deadline exceeded"), "registration state UNKNOWN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newDir(t)
			reg := &fakeRegistrar{env: "testnet", regErr: tc.err}
			err := setupSecret(context.Background(), &bytes.Buffer{}, reg, "testnet", dir, false, entropy())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if _, err := os.Stat(filepath.Join(dir, "recovery-file.dat")); !os.IsNotExist(err) {
				t.Fatalf("an empty recovery file was left behind")
			}
			if _, err := os.Stat(filepath.Join(dir, "entity-secret.hex")); err != nil {
				t.Fatalf("the secret was not kept: %v", err)
			}
		})
	}
}

// A directory the owner cannot write is found before anything is registered.
func TestSecret_UnwritableDirectoryIsFoundBeforeRegistering(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := newDir(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "entity-secret.hex"), []byte(patternHex+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	reg := &fakeRegistrar{env: "testnet"}
	err := setupSecret(context.Background(), &bytes.Buffer{}, reg, "testnet", dir, true, entropy())
	if err == nil || !strings.Contains(err.Error(), "nothing was registered") || len(reg.registered) != 0 {
		t.Fatalf("err = %v, registered %d", err, len(reg.registered))
	}
}

// A FIFO where the secret should be is refused instead of blocking the read forever.
func TestSecret_RefusesANonRegularSecretFile(t *testing.T) {
	dir := newDir(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "entity-secret.hex"), 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	reg := &fakeRegistrar{env: "testnet"}
	err := setupSecret(context.Background(), &bytes.Buffer{}, reg, "testnet", dir, true, entropy())
	if err == nil || !strings.Contains(err.Error(), "not a regular file") || len(reg.registered) != 0 {
		t.Fatalf("err = %v, registered %d", err, len(reg.registered))
	}
}

// An existing file is never replaced.
func TestWritePrivate_NeverOverwrites(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	if err := writePrivate(p, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := writePrivate(p, []byte("y")); err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("overwrite: %v", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "x" {
		t.Fatalf("the existing file was changed")
	}
}

func TestUSDCString(t *testing.T) {
	for in, want := range map[int64]string{0: "0.000000", 1: "0.000001", 12_510_400: "12.510400", 1_000_000: "1.000000"} {
		if got := usdcString(big.NewInt(in)); got != want {
			t.Errorf("usdcString(%d) = %s, want %s", in, got, want)
		}
	}
}

//go:build linux

package lowport

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

const ubuntuNFTPrivilegedTestEnvironment = "HARBOR_PRIVILEGED_LOWPORT_TEST"

// TestUbuntuNFTTableParserAcceptsOnlyTheCanonicalRuleset pins native ownership and exactness.
func TestUbuntuNFTTableParserAcceptsOnlyTheCanonicalRuleset(t *testing.T) {
	request := ubuntuNFTTestRequest(t)
	exact, err := parseUbuntuNFTTable(request, []byte(ubuntuNFTListedTable(request)))
	if err != nil {
		t.Fatalf("parseUbuntuNFTTable(exact) error = %v", err)
	}
	if !exact.TableExact || !exact.RulesExact || exact.TableAmbiguous || exact.RulesAmbiguous {
		t.Fatalf("exact snapshot = %#v", exact)
	}
	for _, mutation := range []struct {
		name  string
		apply func(string) string
	}{
		{name: "owner", apply: func(value string) string { return strings.Replace(value, ubuntuNFTOwnerComment(request), "foreign", 1) }},
		{name: "pool", apply: func(value string) string {
			return strings.Replace(value, request.LoopbackPool().String(), "127.88.0.0/24", 1)
		}},
		{name: "port", apply: func(value string) string { return strings.Replace(value, ":25001", ":26001", 1) }},
		{name: "extra rule", apply: func(value string) string {
			return strings.Replace(value, "\t}\n}", "\t\tip daddr 127.0.0.1 tcp dport 1 drop\n\t}\n}", 1)
		}},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			snapshot, err := parseUbuntuNFTTable(request, []byte(mutation.apply(ubuntuNFTListedTable(request))))
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.TableExact && snapshot.RulesExact {
				t.Fatalf("mutated snapshot remained exact: %#v", snapshot)
			}
		})
	}
}

// TestUbuntuNFTRulesetContainsOnlyTheSelectedPoolAndUpstreams proves no caller-selected nft syntax enters the transaction.
func TestUbuntuNFTRulesetContainsOnlyTheSelectedPoolAndUpstreams(t *testing.T) {
	request := ubuntuNFTTestRequest(t)
	ruleset := ubuntuNFTRuleset(request)
	for _, expected := range []string{
		"table inet " + ubuntuNFTTableName,
		"ip daddr " + request.LoopbackPool().String() + " tcp dport 80 redirect to :" + strconv.Itoa(int(request.HTTPUpstream().Port())),
		"ip daddr " + request.LoopbackPool().String() + " tcp dport 443 redirect to :" + strconv.Itoa(int(request.HTTPSUpstream().Port())),
		ubuntuNFTOwnerComment(request),
	} {
		if !strings.Contains(ruleset, expected) {
			t.Fatalf("ruleset lacks %q:\n%s", expected, ruleset)
		}
	}
}

// TestUbuntuNFTTableListParserBoundsAndFindsOnlyTheFixedNamespace covers discovery before mutation.
func TestUbuntuNFTTableListParserBoundsAndFindsOnlyTheFixedNamespace(t *testing.T) {
	content := `{"nftables":[{"metainfo":{"json_schema_version":1}},{"table":{"family":"ip","name":"foreign"}},{"table":{"family":"inet","name":"goforj_harbor"}}]}`
	count, err := countUbuntuNFTTables([]byte(content))
	if err != nil || count != 1 {
		t.Fatalf("countUbuntuNFTTables() = (%d, %v)", count, err)
	}
	if _, err := countUbuntuNFTTables([]byte(content + `{}`)); err == nil {
		t.Fatal("countUbuntuNFTTables() accepted trailing data")
	}
}

// TestUbuntuNFTNativeLifecycle proves the fixed Ubuntu command surface creates and removes only Harbor's table.
func TestUbuntuNFTNativeLifecycle(t *testing.T) {
	if os.Getenv(ubuntuNFTPrivilegedTestEnvironment) != "1" {
		t.Skipf("set %s=1 on a disposable Ubuntu network namespace", ubuntuNFTPrivilegedTestEnvironment)
	}
	request := ubuntuNFTTestRequest(t)
	adapter, err := New()
	if err != nil {
		t.Fatal(err)
	}
	before, err := adapter.Observe(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	state, err := before.State()
	if err != nil || state != StateAbsent {
		t.Fatalf("initial state = %q, error = %v", state, err)
	}
	ensured, err := adapter.EnsureIfObserved(t.Context(), request, fingerprintValidated(before))
	if err != nil {
		t.Fatalf("EnsureIfObserved() error = %v", err)
	}
	t.Cleanup(func() {
		after := ensured.After
		if _, releaseErr := adapter.ReleaseIfObserved(context.Background(), request, fingerprintValidated(after)); releaseErr != nil {
			t.Errorf("cleanup ReleaseIfObserved() error = %v", releaseErr)
		}
	})
	state, err = ensured.After.State()
	if err != nil || state != StateExact {
		t.Fatalf("ensured state = %q, error = %v", state, err)
	}
	if err := proveUbuntuNFTRedirect(request); err != nil {
		t.Fatalf("prove redirected HTTP listener: %v", err)
	}
	released, err := adapter.ReleaseIfObserved(t.Context(), request, fingerprintValidated(ensured.After))
	if err != nil {
		t.Fatalf("ReleaseIfObserved() error = %v: %v", err, errors.Unwrap(err))
	}
	ensured.After = released.After
	state, err = released.After.State()
	if err != nil || state != StateAbsent {
		t.Fatalf("released state = %q, error = %v", state, err)
	}
}

// proveUbuntuNFTRedirect sends one byte through port 80 and requires it at the policy-selected high listener.
func proveUbuntuNFTRedirect(request Request) error {
	listener, err := net.Listen("tcp4", request.HTTPUpstream().String())
	if err != nil {
		return err
	}
	defer listener.Close()
	serverResult := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverResult <- acceptErr
			return
		}
		defer connection.Close()
		var value [1]byte
		if _, readErr := io.ReadFull(connection, value[:]); readErr != nil {
			serverResult <- readErr
			return
		}
		if value[0] != 0x5a {
			serverResult <- errors.New("redirected listener received the wrong payload")
			return
		}
		_, writeErr := connection.Write([]byte{0xa5})
		serverResult <- writeErr
	}()
	target := netip.AddrPortFrom(request.LoopbackPool().Addr().Next(), 80)
	connection, err := net.DialTimeout("tcp4", target.String(), time.Second)
	if err != nil {
		return err
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(time.Second)); err != nil {
		return err
	}
	if _, err := connection.Write([]byte{0x5a}); err != nil {
		return err
	}
	var response [1]byte
	if _, err := io.ReadFull(connection, response[:]); err != nil {
		return err
	}
	if response[0] != 0xa5 {
		return errors.New("redirected listener returned the wrong payload")
	}
	return <-serverResult
}

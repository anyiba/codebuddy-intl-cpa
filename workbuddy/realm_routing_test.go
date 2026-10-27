package main

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func realmPoolFixtures(t *testing.T) {
	t.Helper()
	files := []pluginapi.HostAuthFileEntry{
		testAuthEntry("idx-cn-1", "uid-cn-1", false),
		testAuthEntry("idx-cn-2", "uid-cn-2", false),
		testAuthEntry("idx-gl-1", "uid-gl-1", false),
	}
	creds := map[string]*storedAuth{
		"idx-cn-1": {Auth: storedTokens{Domain: "https://www.codebuddy.cn"}, Account: storedAccount{UID: "uid-cn-1"}},
		"idx-cn-2": {Auth: storedTokens{Domain: "https://www.codebuddy.cn"}, Account: storedAccount{UID: "uid-cn-2"}},
		"idx-gl-1": {Auth: storedTokens{Domain: "https://www.workbuddy.ai"}, Account: storedAccount{UID: "uid-gl-1"}},
	}
	stubAuthSources(t, files, creds)
}

// Grep target: a known routed realm keeps its whole pool, never the other.
func TestBuildFailoverPool_OwnRealmPoolNotWidened(t *testing.T) {
	realmPoolFixtures(t)
	cn := buildFailoverPool("uid-cn-1").ordered(nil)
	if len(cn) != 2 || cn[1].AuthID != "idx-cn-2" {
		t.Fatalf("CN pool = %+v, want idx-cn-1 + idx-cn-2", cn)
	}
	gl := buildFailoverPool("uid-gl-1").ordered(nil)
	if len(gl) != 1 || gl[0].AuthID != "idx-gl-1" {
		t.Fatalf("Global pool = %+v, want only idx-gl-1", gl)
	}
}

// A routed account with no usable storage comes from the whole realm pool,
// decided by the model prefix, not from that one account alone.
func TestBuildFailoverPool_UnroutablePrimaryKeepsRealmPool(t *testing.T) {
	realmPoolFixtures(t)
	// An unresolved routed peer is not the executor's snapshot, so it is
	// dropped; the CN peers remain.
	cands := buildBareFailoverPool("uid-cn-1", "cb/x").ordered([]byte(`{"auth":{"accessToken":"x"}}`))
	if len(cands) != 2 {
		t.Fatalf("candidates = %d (%+v), want both CN accounts", len(cands), cands)
	}
	for _, c := range cands {
		if !isCNAuth(c.Auth) {
			t.Fatalf("Global account leaked into a CN-prefixed pool: %+v", c)
		}
	}
	// The same pool without any storage keeps the realm narrowing: the
	// synthesized primary carries no auth, so it is dropped, not executed.
	if got := buildBareFailoverPool("uid-cn-1", "cb/x").ordered(nil); len(got) != 2 {
		t.Fatalf("pool without storage = %+v, want both CN accounts", got)
	}
}

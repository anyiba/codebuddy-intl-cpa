package main

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func poolFixtures(t *testing.T) {
	t.Helper()
	files := []pluginapi.HostAuthFileEntry{
		testAuthEntry("idx-cn-1", "uid-cn-1", false),
		testAuthEntry("idx-gl-1", "uid-gl-1", false),
		testAuthEntry("idx-gl-2", "uid-gl-2", false),
	}
	creds := map[string]*storedAuth{
		"idx-cn-1": {Auth: storedTokens{Domain: "https://www.codebuddy.cn", AccessToken: "cn-token"}, Account: storedAccount{UID: "uid-cn-1"}},
		"idx-gl-1": {Auth: storedTokens{Domain: "https://www.workbuddy.ai", AccessToken: "gl-token"}, Account: storedAccount{UID: "uid-gl-1"}},
		"idx-gl-2": {Auth: storedTokens{Domain: "https://www.workbuddy.ai", AccessToken: "gl-token"}, Account: storedAccount{UID: "uid-gl-2"}},
	}
	stubAuthSources(t, files, creds)
	off := modelPoolEnabled.Load()
	t.Cleanup(func() { modelPoolEnabled.Store(off) })
}

// The switch decides whether a CN-prefixed model reaches Global accounts.
func TestModelPool_SwitchControlsCrossRealm(t *testing.T) {
	poolFixtures(t)
	modelPoolEnabled.Store(false)
	off := buildRequestFailoverPool2(t, "cn/deepseek-v4-pro", "uid-cn-1")
	for _, c := range off {
		if !isCNAuth(c.Auth) {
			t.Fatalf("pool off: Global account in a CN pool: %+v", c)
		}
	}

	modelPoolEnabled.Store(true)
	on := buildRequestFailoverPool2(t, "cn/deepseek-v4-pro", "uid-cn-1")
	realms := map[string]int{}
	for _, c := range on {
		realms[accountRegion(c.Auth)]++
	}
	if realms[realmCN] != 1 || realms[realmGlobal] != 2 {
		t.Fatalf("pool on: realms = %v, want 1 CN + 2 Global", realms)
	}
}

// A model only one realm serves never reaches the other one.
func TestModelPool_SkipsRealmsWithoutTheModel(t *testing.T) {
	poolFixtures(t)
	modelPoolEnabled.Store(true)
	// Static catalogs: every ID in one realm's list must be served by that
	// realm; pick one the other realm does not list and check the filter.
	cnOnly := ""
	for _, m := range wbCNModels() {
		if m.ID != "" && !catalogHas(realmGlobal, m.ID) {
			cnOnly = m.ID
			break
		}
	}
	if cnOnly == "" {
		t.Fatal("fixture: no CN-only static model left to test with")
	}
	cands := buildRequestFailoverPool2(t, cnOnly, "uid-gl-1")
	if len(cands) == 0 {
		t.Fatalf("%s must still reach the CN accounts", cnOnly)
	}
	for _, c := range cands {
		if !isCNAuth(c.Auth) {
			t.Fatalf("CN-only model reached a Global account: %+v", c)
		}
	}
	if err := validateModelPool("cn/definitely-not-a-model"); err == nil {
		t.Fatal("an unknown model must be rejected with the pool on")
	}
	if status, _ := upstreamStatusOf(validateModelPool("cn/definitely-not-a-model")); status != 400 {
		t.Fatalf("rejection status = %d, want 400", status)
	}
	if err := validateModelPool("cn/deepseek-v4-pro"); err != nil {
		t.Fatalf("known model rejected: %v", err)
	}
}

func buildRequestFailoverPool2(t *testing.T, model, routed string) []*failoverCandidate {
	t.Helper()
	pool, storage, err := buildRequestFailoverPool(model, routed, nil)
	if err != nil {
		t.Fatal(err)
	}
	return applyModelPoolCandidates(model, pool.ordered(storage))
}

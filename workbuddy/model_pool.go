// model_pool.go implements the optional cross-realm model pool. When the
// panel switch is on, one model ID (prefixed or not) is served by every enabled
// account of BOTH realms as one pool: `cn/deepseek-v4.1-flash` may be answered
// by a Global account, and a Global-prefixed id by a CN account. Off (the
// default) keeps the original behaviour: a prefix binds a request to its realm.
//
// Per attempt the upstream model ID is resolved against the realm of the
// account that will send it, and a candidate is skipped when its own realm
// does not know the model — an ID only one realm serves never reaches the
// other. Rate-limit cooldowns stay scoped to that per-account model.
package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var modelPoolEnabled atomic.Bool

// applyModelPoolPreference restores the persisted cross-realm pool switch on
// every register/reconfigure; missing state means off.
func applyModelPoolPreference() {
	if st, err := loadState(); err == nil {
		modelPoolEnabled.Store(st.ModelPool != nil && *st.ModelPool)
		return
	}
	modelPoolEnabled.Store(false)
}

// validateModelPool checks a requested model against the pool before the
// executor picks a candidate (nil = fine, or the pool is off).
func validateModelPool(model string) error {
	if !modelPoolEnabled.Load() {
		return nil
	}
	_, err := modelPoolRealmOf(model)
	return err
}

// modelPoolRealmOf returns the realm this model lives in, or an error when no
// realm serves it (with the pool on, a typo or an unknown alias must not reach
// the upstream on either realm).
func modelPoolRealmOf(model string) (string, error) {
	for _, realm := range []string{realmGlobal, realmCN} {
		if candidateCanServe(model, realm) {
			return realm, nil
		}
	}
	return "", &upstreamStatusError{status: http.StatusBadRequest, body: "model " + truncateRedacted(model, 80) + " is not served by any CodeBuddy account"}
}

// modelPoolRealmKey is the session-affinity realm. With the pool on there is
// one shared affinity group; a binding to an account that stops being eligible
// is simply replaced by the next successful admission.
func modelPoolRealmKey(sa *storedAuth) string {
	if modelPoolEnabled.Load() {
		return ""
	}
	return accountRegion(sa)
}

// applyModelPoolCandidates resolves RateModel against each candidate's own
// realm and drops candidates whose realm does not serve the model. It only runs
// when the pool is on, so the default path is unchanged.
func applyModelPoolCandidates(model string, cands []*failoverCandidate) []*failoverCandidate {
	out := make([]*failoverCandidate, 0, len(cands))
	for _, c := range cands {
		if c == nil {
			continue
		}
		if c.Auth == nil {
			// The executor's own storage snapshot may be unresolved here; its
			// attempt re-checks the model against the real realm.
			out = append(out, c)
			continue
		}
		if !modelPoolServes(model, c.Auth) {
			continue
		}
		upstream := modelPoolAttemptModel(model, c.Auth)
		if upstream == "" {
			continue
		}
		c.RateModel = upstream
		out = append(out, c)
	}
	return out
}

// poolCandidates returns the executor's candidate list. With the pool on, each
// candidate's RateModel becomes the ID its own realm will send, and a realm
// that does not serve the model is dropped here so the shared failover loop
// stays realm-agnostic.
func poolCandidates(model string, cands []*failoverCandidate) []*failoverCandidate {
	if !modelPoolEnabled.Load() {
		return cands
	}
	return applyModelPoolCandidates(model, cands)
}

// modelPoolAttemptModel returns the upstream ID cand should send for the
// requested model, or "" when cand's realm cannot serve it. Only the upstream
// ID is realm-dependent; the account identity is not, so every candidate falls
// back to the same advertised ID when nothing matches.
func modelPoolAttemptModel(requested string, sa *storedAuth) string {
	if sa == nil {
		return ""
	}
	return resolveUpstreamModel(requested, accountRegion(sa), nil)
}

// modelPoolServes reports whether the candidate's own realm has the model. A
// candidate without a resolvable realm is kept: the attempt re-checks it.
func modelPoolServes(model string, sa *storedAuth) bool {
	return sa == nil || candidateCanServe(model, accountRegion(sa))
}

// candidateCanServe reports whether realm has the model in its catalog.
func candidateCanServe(model, realm string) bool {
	bare := strings.TrimSpace(model)
	if bare == "" {
		return false
	}
	for _, r := range []string{realmGlobal, realmCN} {
		if id, prefixed := stripDynamicPrefix(model, r); prefixed {
			bare = id
			break
		}
	}
	return catalogModelHas(realm, bare)
}

// handleModelPoolConfig reads or sets the switch (GET/POST
// /modelpool/config, body {"enabled": bool}).
func handleModelPoolConfig(req pluginapi.ManagementRequest) map[string]any {
	if req.Method == http.MethodGet {
		return map[string]any{"enabled": modelPoolEnabled.Load()}
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.Unmarshal(req.Body, &body); err != nil || body.Enabled == nil {
		return map[string]any{"error": `expected {"enabled": true|false}`}
	}
	persistent := setModelPoolSetting(*body.Enabled) == nil
	return map[string]any{"enabled": *body.Enabled, "persistent": persistent}
}

// setModelPoolSetting flips the switch for new requests immediately and
// persists the choice; a failed write only costs a re-toggle after restart.
func setModelPoolSetting(on bool) error {
	modelPoolEnabled.Store(on)
	return mutateState(func(st *wbStateFile) error {
		st.ModelPool = cloneBoolPtr(&on)
		return nil
	})
}

// catalogModelHas reports whether the realm actually emits the model (its own
// static or dynamic list), unlike catalogHas, which falls back to the CN list
// for any unknown ID.
func catalogModelHas(realm, id string) bool {
	for _, info := range mmEmissionModels(nil, realm, "") {
		if info.ID == id {
			return true
		}
	}
	return false
}

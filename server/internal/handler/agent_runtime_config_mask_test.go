package handler

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestSanitizeRuntimeConfigForAgentResponseAllowlist(t *testing.T) {
	t.Parallel()

	const secret = "sentinel-runtime-config-secret"
	raw := []byte(`{
		"mode":"gateway",
		"gateway":{
			"host":"` + secret + `",
			"port":18789,
			"token":"` + secret + `",
			"tls":true,
			"Authorization":"Bearer ` + secret + `"
		},
		"api_key":"` + secret + `",
		"headers":{"X-Token":"` + secret + `"},
		"env":{"TOKEN":"` + secret + `"},
		"unknown":{"nested":"` + secret + `"}
	}`)

	got, hasConfig, redacted, keyCount := sanitizeRuntimeConfigForAgentResponse(raw)
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal projection: %v", err)
	}
	if bytes.Contains(encoded, []byte(secret)) {
		t.Fatalf("runtime_config projection leaked sentinel: %s", encoded)
	}
	if !hasConfig || !redacted || keyCount != 6 {
		t.Fatalf("metadata = has:%v redacted:%v keys:%d, want true/true/6", hasConfig, redacted, keyCount)
	}

	var want any
	if err := json.Unmarshal([]byte(`{
		"mode":"gateway",
		"gateway":{"host":"****","port":18789,"token":"****","tls":true},
		"_redacted":"****"
	}`), &want); err != nil {
		t.Fatalf("unmarshal expected: %v", err)
	}
	if gotEncoded, _ := json.Marshal(got); !jsonBytesEqual(gotEncoded, mustJSONMarshal(t, want)) {
		t.Fatalf("projection = %s", gotEncoded)
	}
}

func TestSanitizeRuntimeConfigForAgentResponseSafeAndEmpty(t *testing.T) {
	t.Parallel()

	safe, hasConfig, redacted, keyCount := sanitizeRuntimeConfigForAgentResponse(
		[]byte(`{"mode":"local","gateway":{"port":18789,"tls":false}}`),
	)
	if !hasConfig || redacted || keyCount != 2 {
		t.Fatalf("safe metadata = has:%v redacted:%v keys:%d", hasConfig, redacted, keyCount)
	}
	if _, present := safe[runtimeConfigRedactionMarker]; present {
		t.Fatalf("safe config unexpectedly marked redacted: %#v", safe)
	}

	for _, raw := range [][]byte{nil, {}, []byte(`null`), []byte(`{}`)} {
		got, has, wasRedacted, count := sanitizeRuntimeConfigForAgentResponse(raw)
		if len(got) != 0 || has || wasRedacted || count != 0 {
			t.Fatalf("empty %q = %#v, %v/%v/%d", raw, got, has, wasRedacted, count)
		}
	}
}

func TestSanitizeRuntimeConfigForAgentResponseFailsClosed(t *testing.T) {
	t.Parallel()

	const secret = "sentinel-malformed-runtime-secret"
	tests := [][]byte{
		[]byte(`{"token":"` + secret + `","token":"overwritten"}`),
		[]byte(`{"gateway":`),
		[]byte(`["` + secret + `"]`),
	}
	for _, raw := range tests {
		got, has, redacted, _ := sanitizeRuntimeConfigForAgentResponse(raw)
		encoded, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("marshal projection: %v", err)
		}
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("failed-closed projection leaked sentinel: %s", encoded)
		}
		if !has || !redacted || got[runtimeConfigRedactionMarker] != envSentinel {
			t.Fatalf("failed-closed projection = %#v, has:%v redacted:%v", got, has, redacted)
		}
	}
}

func TestRuntimeConfigPublicProjectionIsPreservedOnBlindWriteback(t *testing.T) {
	t.Parallel()

	projected := map[string]any{
		"mode": "gateway",
		"gateway": map[string]any{
			"host":  envSentinel,
			"token": envSentinel,
		},
		runtimeConfigRedactionMarker: envSentinel,
	}
	if !runtimeConfigContainsPublicProjection(projected) {
		t.Fatal("current public projection must be detected at the write boundary")
	}
	if runtimeConfigContainsPublicProjection(map[string]any{
		"mode": "gateway",
		"gateway": map[string]any{
			"host": "explicit-host",
		},
	}) {
		t.Fatal("complete explicit replacement must remain writable")
	}
}

func TestPreserveLegacyMaskedGatewayToken(t *testing.T) {
	t.Parallel()

	persisted := []byte(`{"mode":"gateway","gateway":{"token":"real-secret","host":"gw.internal"}}`)
	incoming := map[string]any{
		"mode": "gateway",
		"gateway": map[string]any{
			"host":  "gw.internal",
			"port":  float64(18789),
			"token": runtimeConfigLegacyGatewayTokenMask,
		},
	}
	if !runtimeConfigContainsLegacyGatewayMask(incoming) {
		t.Fatal("legacy gateway token mask must be recognized")
	}
	preserveLegacyMaskedGatewayToken(incoming, persisted)
	if got := incoming["gateway"].(map[string]any)["token"]; got != "real-secret" {
		t.Fatalf("token should be restored from persisted row, got %v", got)
	}

	explicit := map[string]any{"gateway": map[string]any{"token": "rotated-secret"}}
	preserveLegacyMaskedGatewayToken(explicit, persisted)
	if got := explicit["gateway"].(map[string]any)["token"]; got != "rotated-secret" {
		t.Fatalf("explicit replacement must win, got %v", got)
	}

	missing := map[string]any{"gateway": map[string]any{"token": runtimeConfigLegacyGatewayTokenMask}}
	preserveLegacyMaskedGatewayToken(missing, []byte(`{"gateway":{"host":"gw.internal"}}`))
	if _, present := missing["gateway"].(map[string]any)["token"]; present {
		t.Fatal("legacy placeholder must be dropped when no stored token exists")
	}
}

func jsonBytesEqual(left, right []byte) bool {
	var leftValue any
	var rightValue any
	return json.Unmarshal(left, &leftValue) == nil &&
		json.Unmarshal(right, &rightValue) == nil &&
		jsonEqual(leftValue, rightValue)
}

func jsonEqual(left, right any) bool {
	return string(mustMarshalJSON(left)) == string(mustMarshalJSON(right))
}

func mustMarshalJSON(value any) []byte {
	data, _ := json.Marshal(value)
	return data
}

func mustJSONMarshal(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	return data
}

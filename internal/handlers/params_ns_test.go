package handlers

import "testing"

func TestNamespaceParams(t *testing.T) {
	vs := []VariantIn{
		{Key: "control"},
		{Key: "a", Params: map[string]any{"tiktokshop": map[string]any{"search": map[string]any{"x": 1}}}},
	}
	if msg := namespaceParams("tiktokshop", vs); msg != "" {
		t.Fatal(msg)
	}
	if _, ok := vs[0].Params["tiktokshop"].(map[string]any); !ok {
		t.Errorf("empty params should get the wrapper: %v", vs[0].Params)
	}
	for _, bad := range []map[string]any{
		{"search": map[string]any{}},                                    // not wrapped
		{"tiktokshop": map[string]any{}, "other": 1},                    // extra top-level key
		{"tiktokshop": "x"},                                             // not an object
		{"tokopedia": map[string]any{"search": map[string]any{"x": 1}}}, // another platform's key
	} {
		if msg := namespaceParams("tiktokshop", []VariantIn{{Key: "v", Params: bad}}); msg == "" {
			t.Errorf("accepted %v", bad)
		}
	}
}

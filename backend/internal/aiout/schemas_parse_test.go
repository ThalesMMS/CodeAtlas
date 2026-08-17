package aiout

import (
	"encoding/json"
	"testing"
)

func TestEmbeddedSchemasAreValidJSON(t *testing.T) {
	t.Parallel()
	for name, schema := range map[string]json.RawMessage{
		"explanation": ExplanationSchema(),
		"codemap":     CodemapSchema(),
		"wiki":        WikiSchema(),
		"wiki-plan":   WikiPlanSchema(),
	} {
		var v any
		if err := json.Unmarshal(schema, &v); err != nil {
			t.Fatalf("%s schema is invalid JSON: %v", name, err)
		}
	}
}

func TestProviderSchemasAvoidUnsupportedUniqueItemsKeyword(t *testing.T) {
	t.Parallel()
	for name, schema := range map[string]json.RawMessage{
		"explanation": ExplanationSchema(),
		"codemap":     CodemapSchema(),
		"wiki":        WikiSchema(),
		"wiki-plan":   WikiPlanSchema(),
	} {
		var value any
		if err := json.Unmarshal(schema, &value); err != nil {
			t.Fatalf("%s schema is invalid JSON: %v", name, err)
		}
		if schemaContainsKeyword(value, "uniqueItems") {
			t.Fatalf("%s schema contains provider-incompatible uniqueItems", name)
		}
	}
}

func TestWikiSchemaRequiresReasonForLimitations(t *testing.T) {
	t.Parallel()
	var schema map[string]any
	if err := json.Unmarshal(WikiSchema(), &schema); err != nil {
		t.Fatal(err)
	}
	defs, ok := schema["$defs"].(map[string]any)
	if !ok {
		t.Fatalf("wiki schema $defs = %#v", schema["$defs"])
	}
	limitation, ok := defs["wikiLimitation"].(map[string]any)
	if !ok {
		t.Fatalf("wiki schema wikiLimitation = %#v", defs["wikiLimitation"])
	}
	required, _ := limitation["required"].([]any)
	if !containsSchemaString(required, "reason") {
		t.Fatalf("wikiLimitation required = %#v, want reason", required)
	}
	properties, _ := limitation["properties"].(map[string]any)
	reason, _ := properties["reason"].(map[string]any)
	if reason["minLength"] != float64(1) {
		t.Fatalf("wikiLimitation reason = %#v, want minLength 1", reason)
	}
}

func TestWikiSchemaForPageConstrainsManifestSlugs(t *testing.T) {
	t.Parallel()
	allowed := []string{"overview", "6-testing"}
	var schema map[string]any
	if err := json.Unmarshal(WikiSchemaForPage([]string{"ev:1"}, allowed), &schema); err != nil {
		t.Fatal(err)
	}
	properties := schema["properties"].(map[string]any)
	relatedPages := properties["relatedPages"].(map[string]any)
	items := relatedPages["items"].(map[string]any)
	values, ok := items["enum"].([]any)
	if !ok || len(values) != len(allowed) {
		t.Fatalf("relatedPages enum = %#v, want %#v", items["enum"], allowed)
	}
	for index, want := range allowed {
		if values[index] != want {
			t.Fatalf("relatedPages enum[%d] = %#v, want %q", index, values[index], want)
		}
	}
}

func TestWikiSchemaForPageRequiresEmptyRelatedListWhenNoTargetExists(t *testing.T) {
	t.Parallel()
	var schema map[string]any
	if err := json.Unmarshal(WikiSchemaForPage([]string{"ev:1"}, nil), &schema); err != nil {
		t.Fatal(err)
	}
	properties := schema["properties"].(map[string]any)
	relatedPages := properties["relatedPages"].(map[string]any)
	if relatedPages["maxItems"] != float64(0) {
		t.Fatalf("relatedPages maxItems = %#v, want 0", relatedPages["maxItems"])
	}
	items := relatedPages["items"].(map[string]any)
	if _, exists := items["enum"]; exists {
		t.Fatalf("relatedPages items = %#v, empty enum is not valid JSON Schema", items)
	}
}

func TestWikiSchemaForPageAvoidsInvalidEmptyEvidenceEnum(t *testing.T) {
	t.Parallel()
	var schema map[string]any
	if err := json.Unmarshal(WikiSchemaForPage(nil, []string{"overview"}), &schema); err != nil {
		t.Fatal(err)
	}
	definitions := schema["$defs"].(map[string]any)
	claim := definitions["claim"].(map[string]any)
	properties := claim["properties"].(map[string]any)
	evidenceIDs := properties["evidenceIds"].(map[string]any)
	if evidenceIDs["maxItems"] != float64(0) {
		t.Fatalf("claim evidenceIds maxItems = %#v, want 0", evidenceIDs["maxItems"])
	}
	items := evidenceIDs["items"].(map[string]any)
	if _, exists := items["enum"]; exists {
		t.Fatalf("claim evidenceIds items = %#v, empty enum is not valid JSON Schema", items)
	}
}

func TestWikiPlanSchemaForPathsConstrainsPlannerAuthoredValues(t *testing.T) {
	t.Parallel()
	allowed := []string{"cmd/api/main.go", "internal/order/service.go"}
	var schema map[string]any
	if err := json.Unmarshal(WikiPlanSchemaForPaths(allowed), &schema); err != nil {
		t.Fatal(err)
	}
	properties := schema["properties"].(map[string]any)
	pages := properties["pages"].(map[string]any)
	page := pages["items"].(map[string]any)
	pageProperties := page["properties"].(map[string]any)
	scope := pageProperties["scopePaths"].(map[string]any)
	items := scope["items"].(map[string]any)
	values := items["enum"].([]any)
	if len(values) != 2 || values[0] != allowed[0] || values[1] != allowed[1] {
		t.Fatalf("scope path enum = %#v, want %#v", values, allowed)
	}
	if pageProperties["title"].(map[string]any)["minLength"] != float64(1) {
		t.Fatalf("title schema = %#v, want minLength 1", pageProperties["title"])
	}
	if pageProperties["slug"].(map[string]any)["pattern"] != `^[a-z0-9]+(?:[.-][a-z0-9]+)*$` {
		t.Fatalf("slug schema = %#v", pageProperties["slug"])
	}
	if pageProperties["parentSlug"].(map[string]any)["pattern"] != `^(?:|[a-z0-9]+(?:[.-][a-z0-9]+)*)$` {
		t.Fatalf("parentSlug schema = %#v", pageProperties["parentSlug"])
	}
}

func TestWikiPlanSchemaForPathsRequiresEmptyScopesWhenInventoryIsEmpty(t *testing.T) {
	t.Parallel()
	var schema map[string]any
	if err := json.Unmarshal(WikiPlanSchemaForPaths(nil), &schema); err != nil {
		t.Fatal(err)
	}
	properties := schema["properties"].(map[string]any)
	pages := properties["pages"].(map[string]any)
	page := pages["items"].(map[string]any)
	pageProperties := page["properties"].(map[string]any)
	scope := pageProperties["scopePaths"].(map[string]any)
	if scope["maxItems"] != float64(0) {
		t.Fatalf("scope maxItems = %#v, want 0", scope["maxItems"])
	}
	items := scope["items"].(map[string]any)
	if _, exists := items["enum"]; exists {
		t.Fatalf("empty inventory produced invalid enum: %#v", items)
	}
}

func containsSchemaString(values []any, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func schemaContainsKeyword(value any, keyword string) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == keyword || schemaContainsKeyword(child, keyword) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if schemaContainsKeyword(child, keyword) {
				return true
			}
		}
	}
	return false
}

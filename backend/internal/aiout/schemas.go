package aiout

import "encoding/json"

// The schemas below are self-contained (no cross-file $ref) so they can be sent
// directly as response_format/json_schema. They mirror the Go types in this
// package, which remain the source of truth; docs/schemas holds the published
// copies. A provider that rejects json_schema degrades to prompt-enforced JSON,
// after which the same local validator runs — so these need only be a best-effort
// constraint, not the security boundary.

const claimDefs = `
"claim":{"type":"object","additionalProperties":false,"required":["text","evidenceIds"],"properties":{"text":{"type":"string"},"evidenceIds":{"type":"array","items":{"type":"string"}}}},
"inference":{"type":"object","additionalProperties":false,"required":["text","evidenceIds","confidence"],"properties":{"text":{"type":"string"},"evidenceIds":{"type":"array","items":{"type":"string"}},"confidence":{"type":"number"}}},
"uncertainty":{"type":"object","additionalProperties":false,"required":["text"],"properties":{"text":{"type":"string"},"reason":{"type":"string"},"evidenceIds":{"type":"array","items":{"type":"string"}}}}`

const wikiLimitationDef = `
"wikiLimitation":{"type":"object","additionalProperties":false,"required":["text","reason"],"properties":{"text":{"type":"string","minLength":1},"reason":{"type":"string","minLength":1},"evidenceIds":{"type":"array","items":{"type":"string"}}}}`

const explanationSchema = `{
"type":"object","additionalProperties":false,
"required":["schemaVersion","summary","observations","inferences","uncertainties","changeImpact"],
"properties":{
"schemaVersion":{"const":"explanation/v2"},
"summary":{"type":"string"},
"codeEvidenceIds":{"type":"array","maxItems":3,"items":{"type":"string"}},
"observations":{"type":"array","items":{"$ref":"#/$defs/claim"}},
"inferences":{"type":"array","items":{"$ref":"#/$defs/inference"}},
"uncertainties":{"type":"array","items":{"$ref":"#/$defs/uncertainty"}},
"changeImpact":{"type":"array","items":{"$ref":"#/$defs/claim"}}},
"$defs":{` + claimDefs + `}}`

const codemapSchema = `{
"type":"object","additionalProperties":false,
"required":["schemaVersion","title","overview","motivation","details","trace","flows","claims","inferences","uncertainties"],
"properties":{
"schemaVersion":{"const":"codemap-narrative/v2"},
"title":{"type":"string"},
"overview":{"type":"string","minLength":96,"maxLength":4000},
"motivation":{"type":"string","minLength":180,"maxLength":4000},
"details":{"type":"string","minLength":320,"maxLength":4000},
"trace":{"type":"array","items":{"type":"string"}},
"flows":{"type":"array","maxItems":8,"items":{"type":"object","additionalProperties":false,"required":["title","entryNodeId","summary","motivation","details","steps"],"properties":{"title":{"type":"string"},"entryNodeId":{"type":"string"},"summary":{"type":"string","minLength":12,"maxLength":400},"motivation":{"type":"string","minLength":80,"maxLength":2500},"details":{"type":"string","minLength":120,"maxLength":3500},"steps":{"type":"array","maxItems":16,"items":{"type":"object","additionalProperties":false,"required":["label","nodeId","text","anchorText","notes"],"properties":{"label":{"type":"string"},"nodeId":{"type":"string"},"text":{"type":"string"},"anchorText":{"type":"string","maxLength":240},"notes":{"type":"array","maxItems":4,"items":{"type":"string","minLength":1,"maxLength":200}}}}}}}},
"claims":{"type":"array","items":{"$ref":"#/$defs/claim"}},
"inferences":{"type":"array","items":{"$ref":"#/$defs/inference"}},
"uncertainties":{"type":"array","items":{"$ref":"#/$defs/uncertainty"}}},
"$defs":{` + claimDefs + `}}`

const wikiSchema = `{
"type":"object","additionalProperties":false,
"required":["schemaVersion","title","sections","relatedPages","inferences","limitations"],
"properties":{
"schemaVersion":{"const":"wiki-page/v4"},
"title":{"type":"string"},
"sections":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["heading","claims"],"properties":{"heading":{"type":"string"},"claims":{"type":"array","items":{"$ref":"#/$defs/claim"}},"codeEvidenceIds":{"type":"array","maxItems":3,"items":{"type":"string"}},"tables":{"type":"array","maxItems":6,"items":{"type":"object","additionalProperties":false,"required":["kind","columns","rows","evidenceIds"],"properties":{"kind":{"const":"table"},"columns":{"type":"array","minItems":1,"maxItems":6,"items":{"type":"string"}},"rows":{"type":"array","maxItems":20,"items":{"type":"array","items":{"type":"string"}}},"evidenceIds":{"type":"array","minItems":1,"maxItems":8,"items":{"type":"string"}}}}}}}},
"relatedPages":{"type":"array","maxItems":12,"items":{"type":"string"}},
"inferences":{"type":"array","items":{"$ref":"#/$defs/inference"}},
"limitations":{"type":"array","items":{"$ref":"#/$defs/wikiLimitation"}}},
"$defs":{` + claimDefs + `,` + wikiLimitationDef + `}}`

const wikiPlanSchema = `{
"type":"object","additionalProperties":false,
"required":["schemaVersion","pages"],
"properties":{
"schemaVersion":{"const":"wiki-plan/v1"},
"pages":{"type":"array","minItems":1,"maxItems":25,"items":{"type":"object","additionalProperties":false,"required":["slug","title","parentSlug","scopePaths","archetype"],"properties":{"slug":{"type":"string"},"title":{"type":"string"},"parentSlug":{"type":"string"},"scopePaths":{"type":"array","minItems":1,"items":{"type":"string"}},"archetype":{"enum":["overview","getting-started","architecture-overview","module","layer","frontend","testing","glossary"]}}}}}
}`

// ExplanationSchema is the json_schema for Hover/See More output.
func ExplanationSchema() json.RawMessage { return json.RawMessage(explanationSchema) }

// CodemapSchema is the json_schema for the Codemap narrative output.
func CodemapSchema() json.RawMessage { return json.RawMessage(codemapSchema) }

// WikiSchema is the json_schema for a DeepWiki page output.
func WikiSchema() json.RawMessage { return json.RawMessage(wikiSchema) }

// WikiSchemaForPage returns the DeepWiki schema constrained to the current
// ContextPack and manifest. The static schema documents the reusable output
// shape, while each request narrows every model-authored reference to values the
// backend can safely resolve.
func WikiSchemaForPage(allowedEvidence, allowedRelated []string) json.RawMessage {
	var schema map[string]any
	if err := json.Unmarshal(WikiSchema(), &schema); err != nil {
		panic("aiout: embedded wiki schema is invalid: " + err.Error())
	}
	properties := mustSchemaObject(schema["properties"], "properties")
	definitions := mustSchemaObject(schema["$defs"], "$defs")
	for _, definitionName := range []string{"claim", "inference", "uncertainty", "wikiLimitation"} {
		definition := mustSchemaObject(definitions[definitionName], "$defs."+definitionName)
		definitionProperties := mustSchemaObject(definition["properties"], "$defs."+definitionName+".properties")
		evidenceIDs := mustSchemaObject(definitionProperties["evidenceIds"], "$defs."+definitionName+".properties.evidenceIds")
		constrainStringArray(evidenceIDs, allowedEvidence)
	}
	sections := mustSchemaObject(properties["sections"], "properties.sections")
	// Density floor for generated pages: at least two sections, and claim text
	// long enough to be a documentation paragraph rather than a label. The
	// local validator stays lenient; these bounds steer schema-enforcing
	// providers.
	sections["minItems"] = 2
	claimDefinition := mustSchemaObject(definitions["claim"], "$defs.claim")
	claimProperties := mustSchemaObject(claimDefinition["properties"], "$defs.claim.properties")
	claimText := mustSchemaObject(claimProperties["text"], "$defs.claim.properties.text")
	claimText["minLength"] = 40
	sectionItems := mustSchemaObject(sections["items"], "properties.sections.items")
	sectionProperties := mustSchemaObject(sectionItems["properties"], "properties.sections.items.properties")
	codeEvidenceIDs := mustSchemaObject(sectionProperties["codeEvidenceIds"], "properties.sections.items.properties.codeEvidenceIds")
	constrainStringArray(codeEvidenceIDs, allowedEvidence)
	tables := mustSchemaObject(sectionProperties["tables"], "properties.sections.items.properties.tables")
	tableItems := mustSchemaObject(tables["items"], "properties.sections.items.properties.tables.items")
	tableProperties := mustSchemaObject(tableItems["properties"], "properties.sections.items.properties.tables.items.properties")
	tableEvidenceIDs := mustSchemaObject(tableProperties["evidenceIds"], "properties.sections.items.properties.tables.items.properties.evidenceIds")
	constrainStringArray(tableEvidenceIDs, allowedEvidence)

	relatedPages := mustSchemaObject(properties["relatedPages"], "properties.relatedPages")
	constrainStringArray(relatedPages, allowedRelated)
	encoded, err := json.Marshal(schema)
	if err != nil {
		panic("aiout: failed to encode constrained wiki schema: " + err.Error())
	}
	return encoded
}

func constrainStringArray(array map[string]any, allowed []string) {
	items := map[string]any{"type": "string"}
	if len(allowed) == 0 {
		delete(array, "minItems")
		array["maxItems"] = 0
	} else {
		items["enum"] = append([]string(nil), allowed...)
	}
	array["items"] = items
}

func mustSchemaObject(value any, name string) map[string]any {
	object, ok := value.(map[string]any)
	if !ok {
		panic("aiout: embedded wiki schema has no " + name + " object")
	}
	return object
}

// WikiPlanSchema is the response schema for the bounded DeepWiki planner call.
func WikiPlanSchema() json.RawMessage { return json.RawMessage(wikiPlanSchema) }

// WikiPlanSchemaForPaths constrains planner-authored paths and identifiers to
// the current repository inventory while preserving the reusable WikiPlan v1
// shape. Semantic hierarchy validation remains authoritative in the service.
func WikiPlanSchemaForPaths(allowedPaths []string) json.RawMessage {
	var schema map[string]any
	if err := json.Unmarshal(WikiPlanSchema(), &schema); err != nil {
		panic("aiout: embedded wiki-plan schema is invalid: " + err.Error())
	}
	properties := mustSchemaObject(schema["properties"], "properties")
	pages := mustSchemaObject(properties["pages"], "properties.pages")
	page := mustSchemaObject(pages["items"], "properties.pages.items")
	pageProperties := mustSchemaObject(page["properties"], "properties.pages.items.properties")
	mustSchemaObject(pageProperties["slug"], "properties.pages.items.properties.slug")["pattern"] = `^[a-z0-9]+(?:[.-][a-z0-9]+)*$`
	mustSchemaObject(pageProperties["title"], "properties.pages.items.properties.title")["minLength"] = 1
	mustSchemaObject(pageProperties["parentSlug"], "properties.pages.items.properties.parentSlug")["pattern"] = `^(?:|[a-z0-9]+(?:[.-][a-z0-9]+)*)$`
	constrainStringArray(mustSchemaObject(pageProperties["scopePaths"], "properties.pages.items.properties.scopePaths"), allowedPaths)
	encoded, err := json.Marshal(schema)
	if err != nil {
		panic("aiout: failed to encode constrained wiki-plan schema: " + err.Error())
	}
	return encoded
}

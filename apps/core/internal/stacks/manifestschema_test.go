package stacks

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// The schema names exactly the fields the Go types decode, minus the legacy
// ones a version 8 manifest refuses, so a field added to Manifest without
// the schema (or the other way round) fails here.
func TestManifestSchemaMatchesTheTypes(t *testing.T) {
	var root map[string]any
	if err := json.Unmarshal(ManifestSchema, &root); err != nil {
		t.Fatal(err)
	}
	defs := root["definitions"].(map[string]any)
	props := func(node any) map[string]any { return node.(map[string]any)["properties"].(map[string]any) }
	health := props(root)["health"]
	backupFolder := defs["backupFolder"]
	cases := []struct {
		typ    any
		node   any
		legacy []string
	}{
		{Manifest{}, root, []string{"storage", "secrets"}},
		{Route{}, defs["route"], nil},
		{Variable{}, defs["variable"], nil},
		{Egress{}, defs["egress"], nil},
		{Port{}, defs["port"], nil},
		{Health{}, health, nil},
		{HTTPProbe{}, props(health)["http"], nil},
		{Backup{}, props(root)["backup"], nil},
		{BackupDir{}, backupFolder, nil},
		{BackupIgnore{}, props(backupFolder)["exclude"].(map[string]any)["items"], nil},
		{AddedFoldersHint{}, props(root)["added_folders"], nil},
	}
	for _, c := range cases {
		rt := reflect.TypeOf(c.typ)
		var fields []string
		for i := range rt.NumField() {
			name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
			if name != "" && name != "-" && !slices.Contains(c.legacy, name) {
				fields = append(fields, name)
			}
		}
		var schema []string
		for k := range props(c.node) {
			schema = append(schema, k)
		}
		slices.Sort(fields)
		slices.Sort(schema)
		if !slices.Equal(fields, schema) {
			t.Errorf("%s: the type decodes %v, the schema names %v", rt.Name(), fields, schema)
		}
		if c.node.(map[string]any)["additionalProperties"] != false {
			t.Errorf("%s: the schema allows unknown fields; ParseManifest refuses them", rt.Name())
		}
	}
}

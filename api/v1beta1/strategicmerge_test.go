package v1beta1

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/util/dump"
	strategicmergepatch "k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/utils/ptr"
)

func TestStrategicMergePatchUsingLookupPatchMeta(t *testing.T) {
	tests := []struct {
		name       string
		schema     JSONSchemaProps
		original   map[string]interface{}
		patch      map[string]interface{}
		wantResult map[string]interface{}
	}{
		// object

		{
			name: "Set a field in plain struct",
			schema: JSONSchemaProps{
				Type: "integer",
			},
			original: map[string]interface{}{},
			patch: map[string]interface{}{
				"foo": 2,
			},
			wantResult: map[string]interface{}{
				"foo": 2,
			},
		},
		{
			name: "Set a field in a nested struct",
			schema: JSONSchemaProps{
				Type: "object",
				Properties: map[string]JSONSchemaProps{
					"knownProperty": {
						Type: "integer",
					},
				},
			},
			original: map[string]interface{}{},
			patch: map[string]interface{}{
				"foo": map[string]interface{}{
					"knownProperty": 2,
				},
			},
			wantResult: map[string]interface{}{
				"foo": map[string]interface{}{
					"knownProperty": 2,
				},
			},
		},
		{
			name: "Set a field in a nested struct",
			schema: JSONSchemaProps{
				Type: "object",
				Properties: map[string]JSONSchemaProps{
					"knownProperty": {
						Type: "integer",
					},
				},
			},
			original: map[string]interface{}{
				"foo": map[string]interface{}{},
			},
			patch: map[string]interface{}{
				"foo": map[string]interface{}{
					"knownProperty": 2,
				},
			},
			wantResult: map[string]interface{}{
				"foo": map[string]interface{}{
					"knownProperty": 2,
				},
			},
		},
		{
			name: "Change a field in plain struct",
			schema: JSONSchemaProps{
				Type: "integer",
			},
			original: map[string]interface{}{
				"foo": 1,
			},
			patch: map[string]interface{}{
				"foo": 2,
			},
			wantResult: map[string]interface{}{
				"foo": 2,
			},
		},
		{
			name: "Change a field in a nested struct",
			schema: JSONSchemaProps{
				Type: "object",
				Properties: map[string]JSONSchemaProps{
					"knownProperty": {
						Type: "integer",
					},
				},
			},
			original: map[string]interface{}{
				"foo": map[string]interface{}{
					"knownProperty": 1,
				},
			},
			patch: map[string]interface{}{
				"foo": map[string]interface{}{
					"knownProperty": 2,
				},
			},
			wantResult: map[string]interface{}{
				"foo": map[string]interface{}{
					"knownProperty": 2,
				},
			},
		},
		{
			name: "Delete a field in plain struct",
			schema: JSONSchemaProps{
				Type: "integer",
			},
			original: map[string]interface{}{
				"foo": 1,
				"bar": 2,
			},
			patch: map[string]interface{}{
				"foo": nil,
			},
			wantResult: map[string]interface{}{
				"bar": 2,
			},
		},
		{
			name: "Delete all fields in plain struct",
			schema: JSONSchemaProps{
				Type: "integer",
			},
			original: map[string]interface{}{
				"foo": 1,
				"bar": 2,
			},
			patch: map[string]interface{}{
				"$patch": "delete",
			},
			wantResult: map[string]interface{}{},
		},
		{
			name: "delete a field in a nested struct",
			schema: JSONSchemaProps{
				Type: "object",
				Properties: map[string]JSONSchemaProps{
					"knownProperty": {
						Type: "integer",
					},
					"knownProperty2": {
						Type: "integer",
					},
				},
			},
			original: map[string]interface{}{
				"foo": map[string]interface{}{
					"knownProperty":  1,
					"knownProperty2": 2,
				},
			},
			patch: map[string]interface{}{
				"foo": map[string]interface{}{
					"knownProperty": nil,
				},
			},
			wantResult: map[string]interface{}{
				"foo": map[string]interface{}{
					"knownProperty2": 2,
				},
			},
		},
		{
			name: "delete a nested struct entirely",
			schema: JSONSchemaProps{
				Type: "object",
				Properties: map[string]JSONSchemaProps{
					"knownProperty": {
						Type: "integer",
					},
				},
			},
			original: map[string]interface{}{
				"foo": map[string]interface{}{
					"knownProperty": 1,
				},
			},
			patch: map[string]interface{}{
				"foo": nil,
			},
			wantResult: map[string]interface{}{},
		},
		{
			name: "$retainKeys in plain struct",
			schema: JSONSchemaProps{
				Type: "integer",
			},
			original: map[string]interface{}{
				"foo": 1,
				"bar": 2,
			},
			patch: map[string]interface{}{
				"$retainKeys": []interface{}{
					"foo",
					"baz",
				},
				"baz": 3,
			},
			wantResult: map[string]interface{}{
				"foo": 1,
				"baz": 3,
			},
		},

		// atomic lists

		{
			name: "Patch replaces all item in a list with atomic behaviour",
			schema: JSONSchemaProps{
				Type: "object",
				Properties: map[string]JSONSchemaProps{
					"knownProperty": {
						Type: "array",
						Items: &JSONSchemaProps{
							Type: "object",
							Properties: map[string]JSONSchemaProps{
								"key": {
									Type: "string",
								},
								"value": {
									Type: "string",
								},
							},
						},
					},
				},
			},
			original: map[string]interface{}{
				"foo": map[string]interface{}{
					"knownProperty": []interface{}{
						map[string]interface{}{
							"key":   "key1",
							"value": "value1",
						},
						map[string]interface{}{
							"key":   "key2",
							"value": "value2",
						},
					},
				},
			},
			patch: map[string]interface{}{
				"foo": map[string]interface{}{
					"knownProperty": []interface{}{
						map[string]interface{}{
							"key":   "key3",
							"value": "value3",
						},
						map[string]interface{}{
							"key":   "key2",
							"value": "value2-changed",
						},
					},
				},
			},
			wantResult: map[string]interface{}{
				"foo": map[string]interface{}{
					"knownProperty": []interface{}{
						map[string]interface{}{
							"key":   "key3",
							"value": "value3",
						},
						map[string]interface{}{
							"key":   "key2",
							"value": "value2-changed",
						},
					},
				},
			},
		},

		// MapLists

		{
			name: "Add an and change items into a MapList",
			schema: JSONSchemaProps{
				Type: "object",
				Properties: map[string]JSONSchemaProps{
					"knownProperty": {
						Type: "array",
						Items: &JSONSchemaProps{
							Type: "object",
							Properties: map[string]JSONSchemaProps{
								"key": {
									Type: "string",
								},
								"value": {
									Type: "string",
								},
							},
						},
						XPatchStrategy: ptr.To("merge"),
						XPatchMergeKey: ptr.To("key"),
						XListType:      ptr.To("map"),
						XListMapKey:    ptr.To("key"),
					},
				},
			},
			original: map[string]interface{}{
				"foo": map[string]interface{}{
					"knownProperty": []interface{}{
						map[string]interface{}{
							"key":   "key1",
							"value": "value1",
						},
						map[string]interface{}{
							"key":   "key2",
							"value": "value2",
						},
					},
				},
			},
			patch: map[string]interface{}{
				"foo": map[string]interface{}{
					"knownProperty": []interface{}{
						map[string]interface{}{
							"key":   "key3",
							"value": "value3",
						},
						map[string]interface{}{
							"key":   "key2",
							"value": "value2-changed",
						},
					},
				},
			},
			wantResult: map[string]interface{}{
				"foo": map[string]interface{}{
					"knownProperty": []interface{}{
						map[string]interface{}{
							"key":   "key3",
							"value": "value3",
						},
						map[string]interface{}{
							"key":   "key1",
							"value": "value1",
						},
						map[string]interface{}{
							"key":   "key2",
							"value": "value2-changed",
						},
					},
				},
			},
		},
		{
			name: "Delete an item from a MapList",
			schema: JSONSchemaProps{
				Type: "object",
				Properties: map[string]JSONSchemaProps{
					"knownProperty": {
						Type: "array",
						Items: &JSONSchemaProps{
							Type: "object",
							Properties: map[string]JSONSchemaProps{
								"key": {
									Type: "string",
								},
								"value": {
									Type: "string",
								},
							},
						},
						XPatchStrategy: ptr.To("merge"),
						XPatchMergeKey: ptr.To("key"),
						XListType:      ptr.To("map"),
						XListMapKey:    ptr.To("key"),
					},
				},
			},
			original: map[string]interface{}{
				"foo": map[string]interface{}{
					"knownProperty": []interface{}{
						map[string]interface{}{
							"key":   "key1",
							"value": "value1",
						},
						map[string]interface{}{
							"key":   "key2",
							"value": "value2",
						},
					},
				},
			},
			patch: map[string]interface{}{
				"foo": map[string]interface{}{
					"knownProperty": []interface{}{
						map[string]interface{}{
							"key":   "key3",
							"value": "value3",
						},
						map[string]interface{}{
							"key":    "key2",
							"$patch": "delete",
						},
					},
				},
			},
			wantResult: map[string]interface{}{
				"foo": map[string]interface{}{
					"knownProperty": []interface{}{
						map[string]interface{}{
							"key":   "key3",
							"value": "value3",
						},
						map[string]interface{}{
							"key":   "key1",
							"value": "value1",
						},
					},
				},
			},
		},
		{
			name: "Change order of items in a MapList",
			schema: JSONSchemaProps{
				Type: "object",
				Properties: map[string]JSONSchemaProps{
					"knownProperty": {
						Type: "array",
						Items: &JSONSchemaProps{
							Type: "object",
							Properties: map[string]JSONSchemaProps{
								"key": {
									Type: "string",
								},
								"value": {
									Type: "string",
								},
							},
						},
						XPatchStrategy: ptr.To("merge"),
						XPatchMergeKey: ptr.To("key"),
						XListType:      ptr.To("map"),
						XListMapKey:    ptr.To("key"),
					},
				},
			},
			original: map[string]interface{}{
				"foo": map[string]interface{}{
					"knownProperty": []interface{}{
						map[string]interface{}{
							"key":   "key1",
							"value": "value1",
						},
						map[string]interface{}{
							"key":   "key2",
							"value": "value2",
						},
					},
				},
			},
			patch: map[string]interface{}{
				"foo": map[string]interface{}{
					"$setElementOrder/knownProperty": []interface{}{
						map[string]interface{}{
							"key": "key1",
						},
						map[string]interface{}{
							"key": "key2",
						},
						map[string]interface{}{
							"key": "key3",
						},
					},
					"knownProperty": []interface{}{ // also editing; caveat order of items should match the order defined above
						map[string]interface{}{
							"key":   "key2",
							"value": "value2-changed",
						},
						map[string]interface{}{
							"key":   "key3",
							"value": "value3",
						},
					},
				},
			},
			wantResult: map[string]interface{}{
				"foo": map[string]interface{}{
					"knownProperty": []interface{}{
						map[string]interface{}{
							"key":   "key1",
							"value": "value1",
						},
						map[string]interface{}{
							"key":   "key2",
							"value": "value2-changed",
						},
						map[string]interface{}{
							"key":   "key3",
							"value": "value3",
						},
					},
				},
			},
		},

		// Set

		{
			name: "Add an and change items into a set",
			schema: JSONSchemaProps{
				Type: "object",
				Properties: map[string]JSONSchemaProps{
					"knownProperty": {
						Type: "array",
						Items: &JSONSchemaProps{
							Type: "string",
						},
						XPatchStrategy: ptr.To("merge"),
						XListType:      ptr.To("set"),
					},
				},
			},
			original: map[string]interface{}{
				"foo": map[string]interface{}{
					"knownProperty": []interface{}{
						"a",
						"b",
					},
				},
			},
			patch: map[string]interface{}{
				"foo": map[string]interface{}{
					"knownProperty": []interface{}{
						"b",
						"b",
						"c",
					},
				},
			},
			wantResult: map[string]interface{}{
				"foo": map[string]interface{}{
					"knownProperty": []interface{}{
						"a",
						"b", // duplicate b is dropped
						"c",
					},
				},
			},
		},
		{
			name: "Delete items from a set",
			schema: JSONSchemaProps{
				Type: "object",
				Properties: map[string]JSONSchemaProps{
					"knownProperty": {
						Type: "array",
						Items: &JSONSchemaProps{
							Type: "string",
						},
						XPatchStrategy: ptr.To("merge"),
						XListType:      ptr.To("set"),
					},
				},
			},
			original: map[string]interface{}{
				"foo": map[string]interface{}{
					"knownProperty": []interface{}{
						"a",
						"b",
						"c",
					},
				},
			},
			patch: map[string]interface{}{
				"foo": map[string]interface{}{
					"$deleteFromPrimitiveList/knownProperty": []interface{}{
						"c",
					},
				},
			},
			wantResult: map[string]interface{}{
				"foo": map[string]interface{}{
					"knownProperty": []interface{}{
						"a",
						"b",
					},
				},
			},
		},
		{
			name: "Change order of items in a set",
			schema: JSONSchemaProps{
				Type: "object",
				Properties: map[string]JSONSchemaProps{
					"knownProperty": {
						Type: "array",
						Items: &JSONSchemaProps{
							Type: "string",
						},
						XPatchStrategy: ptr.To("merge"),
						XListType:      ptr.To("set"),
					},
				},
			},
			original: map[string]interface{}{
				"foo": map[string]interface{}{
					"knownProperty": []interface{}{
						"a",
						"b",
					},
				},
			},
			patch: map[string]interface{}{
				"foo": map[string]interface{}{
					"$setElementOrder/knownProperty": []interface{}{
						"c",
						"b",
						"a",
					},
					"knownProperty": []interface{}{ // also editing; caveat order of items should match the order defined above
						"c",
						"a",
					},
				},
			},
			wantResult: map[string]interface{}{
				"foo": map[string]interface{}{
					"knownProperty": []interface{}{
						"c",
						"b",
						"a",
					},
				},
			},
		},

		// XPreserveUnknownFields
		{
			name: "Set an XPreserveUnknownField",
			schema: JSONSchemaProps{
				Type: "object",
				// Preserves fields for the current object (in this case unknownProperty).
				XPreserveUnknownFields: true,
			},
			original: map[string]interface{}{
				"foo": map[string]interface{}{},
			},
			patch: map[string]interface{}{
				"foo": map[string]interface{}{
					"scalar": 1,
					"map": map[string]interface{}{
						"foo": 1,
					},
					"list": []interface{}{
						1,
						2,
						3,
					},
				},
			},
			wantResult: map[string]interface{}{
				"foo": map[string]interface{}{
					"scalar": 1,
					"map": map[string]interface{}{
						"foo": 1,
					},
					"list": []interface{}{
						1,
						2,
						3,
					},
				},
			},
		},
		{
			name: "Patch an existing XPreserveUnknownField",
			schema: JSONSchemaProps{
				Type: "object",
				// Preserves fields for the current object (in this case unknownProperty).
				XPreserveUnknownFields: true,
			},
			original: map[string]interface{}{
				"foo": map[string]interface{}{
					"scalar": 1,
					"map": map[string]interface{}{
						"foo": 1,
					},
					"list": []interface{}{
						1,
						2,
						3,
					},
				},
			},
			patch: map[string]interface{}{
				"foo": map[string]interface{}{
					"scalar": 2,
					"map": map[string]interface{}{
						"foo": 2,
					},
					"list": []interface{}{
						1,
						2,
					},
				},
			},
			wantResult: map[string]interface{}{
				"foo": map[string]interface{}{
					"scalar": 2,
					"map": map[string]interface{}{
						"foo": 2,
					},
					"list": []interface{}{
						1,
						2,
					},
				},
			},
		},
		{
			name: "Use directives with XPreserveUnknownField",
			schema: JSONSchemaProps{
				Type: "object",
				// Preserves fields for the current object (in this case unknownProperty).
				XPreserveUnknownFields: true,
			},
			original: map[string]interface{}{
				"foo": map[string]interface{}{
					"scalar": 1,
					"map": map[string]interface{}{
						"foo": 1,
					},
					"list": []interface{}{
						1,
						2,
						3,
					},
				},
			},
			patch: map[string]interface{}{
				"foo": map[string]interface{}{
					"$patch": "delete",
				},
			},
			wantResult: map[string]interface{}{
				"foo": map[string]interface{}{},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := testObjectToJSONOrFail(t, tt.original)
			patch := testObjectToJSONOrFail(t, tt.patch)
			wantResult := testObjectToJSONOrFail(t, tt.wantResult)

			schema := NewPatchMetaForVariable("foo", &tt.schema)
			result, err := strategicmergepatch.StrategicMergePatchUsingLookupPatchMeta(original, patch, schema)
			if err != nil {
				t.Errorf("StrategicMergePatchUsingLookupPatchMeta() error = %v, wantErr nil", err)
				return
			}

			if !reflect.DeepEqual(result, wantResult) {
				t.Errorf("StrategicMergePatchUsingLookupPatchMeta() result = %v, wantResult %v", result, wantResult)
			}
		})
	}
}

func TestPatchMetaForVariable(t *testing.T) {
	t.Run("LookupPatchMetadataForStruct", func(t *testing.T) {
		tests := []struct {
			name                string
			schema              JSONSchemaProps
			wantPatchStrategies []string
			wantPatchMergeKey   string
			wantErr             bool
		}{
			{
				name: "",
				schema: JSONSchemaProps{
					Type: "integer",
				},
				wantPatchStrategies: []string{},
				wantPatchMergeKey:   "",
				wantErr:             false,
			},
			{
				name: "",
				schema: JSONSchemaProps{
					Type:           "integer",
					XPatchMergeKey: ptr.To("Name"),
					XPatchStrategy: ptr.To("merge,retainKeys,replace"), // TODO: test what do they mean
				},
				wantPatchMergeKey:   "Name",
				wantPatchStrategies: []string{"merge", "retainKeys", "replace"},
				wantErr:             false,
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				x := NewPatchMetaForVariable("foo", &tt.schema)
				gotLookupPatchMeta, gotPatchMeta, err := x.LookupPatchMetadataForStruct("foo")

				if (err != nil) != tt.wantErr {
					t.Errorf("PatchMetaForVariable() error = %v, wantErr %v", err, tt.wantErr)
				}
				if gotLookupPatchMeta.Name() != "foo" {
					t.Errorf("PatchMetaForVariable() LookupPatchMeta.Name = %v, wantName %v", gotLookupPatchMeta.Name(), "foo")
				}

				if !reflect.DeepEqual(gotPatchMeta.GetPatchStrategies(), tt.wantPatchStrategies) {
					t.Errorf("PatchMetaForVariable() PatchMeta.GetPatchStrategies = %v, wantPatchStrategies %v", err, tt.wantPatchStrategies)
				}
				if !reflect.DeepEqual(gotPatchMeta.GetPatchMergeKey(), tt.wantPatchMergeKey) {
					t.Errorf("PatchMetaForVariable() PatchMeta.GetPatchMergeKey = %v, wantPatchStrategies %v", err, tt.wantPatchMergeKey)
				}
			})
		}
	})
	t.Run("LookupPatchMetadataForSlice", func(t *testing.T) {
		tests := []struct {
			name                string
			schema              JSONSchemaProps
			wantPatchStrategies []string
			wantPatchMergeKey   string
			wantErr             bool
		}{
			{
				name: "",
				schema: JSONSchemaProps{
					Type: "array",
					Items: &JSONSchemaProps{
						Type: "string",
					},
				},
				wantPatchStrategies: []string{},
				wantPatchMergeKey:   "",
				wantErr:             false,
			},
			{
				name: "",
				schema: JSONSchemaProps{
					Type: "array",
					Items: &JSONSchemaProps{
						Type: "string",
					},
					XPatchMergeKey: ptr.To("Name"),
					XPatchStrategy: ptr.To("merge,retainKeys,replace"), // TODO: test what do they mean
				},
				wantPatchMergeKey:   "Name",
				wantPatchStrategies: []string{"merge", "retainKeys", "replace"},
				wantErr:             false,
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				x := NewPatchMetaForVariable("foo", &tt.schema)
				gotLookupPatchMeta, gotPatchMeta, err := x.LookupPatchMetadataForSlice("foo")

				if (err != nil) != tt.wantErr {
					t.Errorf("PatchMetaForVariable() error = %v, wantErr %v", err, tt.wantErr)
				}
				if gotLookupPatchMeta.Name() != "foo[]" {
					t.Errorf("PatchMetaForVariable() LookupPatchMeta.Name = %v, wantName %v", gotLookupPatchMeta.Name(), "foo[]")
				}
				if !reflect.DeepEqual(gotPatchMeta.GetPatchStrategies(), tt.wantPatchStrategies) {
					t.Errorf("PatchMetaForVariable() PatchMeta.GetPatchStrategies = %v, wantPatchStrategies %v", err, tt.wantPatchStrategies)
				}
				if !reflect.DeepEqual(gotPatchMeta.GetPatchMergeKey(), tt.wantPatchMergeKey) {
					t.Errorf("PatchMetaForVariable() PatchMeta.GetPatchMergeKey = %v, wantPatchStrategies %v", err, tt.wantPatchMergeKey)
				}
			})
		}
	})
}

func testObjectToJSONOrFail(t *testing.T, o map[string]interface{}) []byte {
	if o == nil {
		return nil
	}

	j, err := toJSON(o)
	if err != nil {
		t.Error(err)
	}
	return j
}

func toJSON(v interface{}) ([]byte, error) {
	j, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("json marshal failed: %v\n%v\n", err, dump.Pretty(v))
	}

	return j, nil
}

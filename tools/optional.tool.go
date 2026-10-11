package tools

import "encoding/json"

// Optional is a JSON field that tells "absent" apart from "null", which a plain
// pointer cannot: both decode to nil. PATCH-style handlers need the difference
// (JSON Merge Patch, RFC 7396): an absent key leaves the column alone, null
// clears it, a value sets it.
//
//	var body struct{ CPULimit tools.Optional[float64] `json:"cpu_limit"` }
//	body.CPULimit.Set   // the key was present
//	body.CPULimit.Value // nil when it was null
type Optional[T any] struct {
	Set   bool
	Value *T
}

// UnmarshalJSON runs only when the key is present, which is what records Set.
func (o *Optional[T]) UnmarshalJSON(data []byte) error {
	o.Set = true
	if string(data) == "null" {
		o.Value = nil
		return nil
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	o.Value = &v
	return nil
}

// Clear reports whether the field was explicitly set to null.
func (o Optional[T]) Clear() bool { return o.Set && o.Value == nil }

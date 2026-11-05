package module

import (
	"encoding/json"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Numbers (Plan 7 §5.7.4): attribute values and vars may be integers, read as int64, or finite
// floats, read as float64. Every decoder refuses a float that strconv.ParseFloat cannot read in
// range or that is not finite, whatever its spelling.

// The reasons a number refuses.
const (
	nonFiniteReason = "NaN and infinity are not supported"
	beyond64Reason  = "a number beyond 64 bits is not supported; quote it to make it a string"
)

// floatSpelling is a plain scalar yaml.v3 resolves as a float when strconv.ParseFloat reads it
// (resolve.go yamlStyleFloat); one ParseFloat refuses, such as 1e999, it leaves a string.
var floatSpelling = regexp.MustCompile(`^[-+]?(\.[0-9]+|[0-9]+(\.[0-9]*)?)([eE][-+]?[0-9]+)?$`)

// yamlInt reads n, a scalar tagged !!int, as an int64, else returns why it refuses.
func yamlInt(n *yaml.Node) (int64, string) {
	var v int64
	if n.Decode(&v) != nil {
		if _, whole := new(big.Int).SetString(n.Value, 0); !whole {
			return 0, "not a whole number; remove the !!int tag to make it a string"
		}
		return 0, beyond64Reason
	}
	return v, ""
}

// yamlFloat reads n, a scalar tagged !!float, as a finite float64, else returns why it refuses.
// A plain integer too large for int64, which yaml.v3 reads as a float, refuses as one.
func yamlFloat(n *yaml.Node) (float64, string) {
	var f float64
	switch err := n.Decode(&f); {
	case err != nil && floatSpelling.MatchString(n.Value):
		return 0, nonFiniteReason // well formed, out of range
	case err != nil:
		return 0, "not a number; remove the !!float tag to make it a string"
	case math.IsNaN(f) || math.IsInf(f, 0):
		return 0, nonFiniteReason
	case n.Style&yaml.TaggedStyle == 0 && !strings.ContainsAny(n.Value, ".eE"):
		return 0, beyond64Reason
	}
	return f, ""
}

// outOfRange reports whether n, a scalar tagged !!str, is a plain number yaml.v3 left a string
// because strconv.ParseFloat refuses it out of range, such as 1e999.
func outOfRange(n *yaml.Node) bool {
	if n.Style != 0 || !floatSpelling.MatchString(n.Value) {
		return false
	}
	_, err := strconv.ParseFloat(n.Value, 64)
	return err != nil
}

// DecodedNumber reads a number encoding/json decoded with UseNumber from a file lola wrote but
// anyone may edit: a float64, finite, when it is written with a point or an exponent (Vars writes
// every float so), else an int64. ok is false for any other number.
func DecodedNumber(n json.Number) (v any, ok bool) {
	if !strings.ContainsAny(string(n), ".eE") {
		i, err := n.Int64()
		return i, err == nil
	}
	f, err := strconv.ParseFloat(string(n), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, false
	}
	return f, true
}

// jsonNumber reads a JSONC attribute's number: an int64 when written without a fraction or an
// exponent, else a finite float64; otherwise it returns why it refuses.
func jsonNumber(n json.Number) (any, string) {
	if !strings.ContainsAny(n.String(), ".eE") {
		i, err := n.Int64()
		if err != nil {
			return nil, beyond64Reason
		}
		return i, ""
	}
	f, err := strconv.ParseFloat(n.String(), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, nonFiniteReason
	}
	return f, ""
}

// floatText is f in its shortest form that reads back as the same float64, with a point or an
// exponent, so 1.0 never reads back as the integer 1, nor -0.0 as 0. It is valid YAML and JSON.
func floatText(f float64) string {
	v := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(v, ".e") {
		v += ".0"
	}
	return v
}

// FloatNode is the YAML node of a float var or attribute that reads back as the same float64.
func FloatNode(f float64) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: floatText(f)}
}

// Vars is a set of vars as lola records them in JSON (lock.json's rendered_with, trust.json): each
// float written with a point or an exponent (floatText), so 2.0 and 2 stay two values, which
// DecodedNumber reads back as written (Plan 7 R-7-50).
type Vars map[string]any

// MarshalJSON writes v as a JSON object, its floats kind-preserving.
func (v Vars) MarshalJSON() ([]byte, error) {
	if v == nil {
		return []byte("null"), nil
	}
	out := make(map[string]any, len(v))
	for k, x := range v {
		if f, isFloat := x.(float64); isFloat {
			x = json.Number(floatText(f))
		}
		out[k] = x
	}
	return json.Marshal(out)
}

package contract

//go:generate go run ../../cmd/schemagen ../..

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Decode rejects duplicate object keys and trailing JSON before validation.
func decode(b []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	v, e := value(d)
	if e != nil {
		return nil, e
	}
	if _, e = d.Token(); e != io.EOF {
		return nil, fmt.Errorf("trailing JSON")
	}
	return v, nil
}
func value(d *json.Decoder) (any, error) {
	t, e := d.Token()
	if e != nil {
		return nil, e
	}
	switch t {
	case json.Delim('{'):
		m := map[string]any{}
		for d.More() {
			k, e := d.Token()
			if e != nil {
				return nil, e
			}
			s := k.(string)
			if _, ok := m[s]; ok {
				return nil, fmt.Errorf("duplicate field %q", s)
			}
			v, e := value(d)
			if e != nil {
				return nil, e
			}
			m[s] = v
		}
		_, e = d.Token()
		return m, e
	case json.Delim('['):
		a := []any{}
		for d.More() {
			v, e := value(d)
			if e != nil {
				return nil, e
			}
			a = append(a, v)
		}
		_, e = d.Token()
		return a, e
	}
	return t, nil
}

var literalCache sync.Map

func jsonValue(s string) any {
	if v, ok := literalCache.Load(s); ok {
		return v
	}
	v, e := decode([]byte(s))
	if e != nil {
		panic(e)
	}
	literalCache.Store(s, v)
	return v
}
func equal(a, b any) bool {
	if n, ok := a.(json.Number); ok {
		other, ok := b.(json.Number)
		return ok && compareNumbers(n, other) == 0
	}
	switch a := a.(type) {
	case map[string]any:
		other, ok := b.(map[string]any)
		if !ok || len(a) != len(other) {
			return false
		}
		for key, v := range a {
			x, exists := other[key]
			if !exists || !equal(v, x) {
				return false
			}
		}
		return true
	case []any:
		other, ok := b.([]any)
		if !ok || len(a) != len(other) {
			return false
		}
		for i, v := range a {
			if !equal(v, other[i]) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(a, b)
}

// Compare decimal JSON values without float rounding or expanding large exponents.
func decimal(n json.Number) (negative bool, digits string, exponent *big.Int) {
	s := n.String()
	negative = strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	mantissa, power, found := strings.Cut(strings.ToLower(s), "e")
	exponent = new(big.Int)
	if found {
		exponent.SetString(power, 10)
	}
	whole, fraction, _ := strings.Cut(mantissa, ".")
	exponent.Sub(exponent, big.NewInt(int64(len(fraction))))
	digits = strings.TrimLeft(whole+fraction, "0")
	if digits == "" {
		return false, "0", new(big.Int)
	}
	trimmed := strings.TrimRight(digits, "0")
	exponent.Add(exponent, big.NewInt(int64(len(digits)-len(trimmed))))
	return negative, trimmed, exponent
}
func compareNumbers(a, b json.Number) int {
	negA, digitsA, expA := decimal(a)
	negB, digitsB, expB := decimal(b)
	if negA != negB {
		if negA {
			return -1
		}
		return 1
	}
	comparison := 0
	if digitsA == "0" || digitsB == "0" {
		if digitsA != digitsB {
			if digitsA == "0" {
				comparison = -1
			} else {
				comparison = 1
			}
		}
	} else {
		scaleA := new(big.Int).Add(expA, big.NewInt(int64(len(digitsA))))
		scaleB := new(big.Int).Add(expB, big.NewInt(int64(len(digitsB))))
		comparison = scaleA.Cmp(scaleB)
		if comparison == 0 {
			width := max(len(digitsA), len(digitsB))
			comparison = strings.Compare(digitsA+strings.Repeat("0", width-len(digitsA)), digitsB+strings.Repeat("0", width-len(digitsB)))
		}
	}
	if negA {
		return -comparison
	}
	return comparison
}
func in(v any, a []any) bool {
	for _, x := range a {
		if equal(v, x) {
			return true
		}
	}
	return false
}
func hasType(v, t any) bool {
	if a, ok := t.([]any); ok {
		for _, x := range a {
			if hasType(v, x) {
				return true
			}
		}
		return false
	}
	switch t {
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "number":
		_, ok := v.(json.Number)
		return ok
	case "integer":
		n, ok := v.(json.Number)
		if !ok {
			return false
		}
		_, _, exponent := decimal(n)
		return exponent.Sign() >= 0
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "null":
		return v == nil
	}
	return false
}

var patterns sync.Map

func constraint(v any, p, k string, x any) error {
	s, isString := v.(string)
	n, isNumber := v.(json.Number)
	bad := false
	switch k {
	case "minLength":
		limit, _ := x.(json.Number).Int64()
		bad = isString && int64(utf8.RuneCountInString(s)) < limit
	case "maxLength":
		limit, _ := x.(json.Number).Int64()
		bad = isString && int64(utf8.RuneCountInString(s)) > limit
	case "minimum":
		bad = isNumber && compareNumbers(n, x.(json.Number)) < 0
	case "maximum":
		bad = isNumber && compareNumbers(n, x.(json.Number)) > 0
	case "pattern":
		if isString {
			pat := x.(string)
			r, ok := patterns.Load(pat)
			if !ok {
				r = regexp.MustCompile(pat)
				patterns.Store(pat, r)
			}
			bad = !r.(*regexp.Regexp).MatchString(s)
		}
	case "format":
		if isString {
			switch x {
			case "date-time":
				_, e := time.Parse(time.RFC3339, s)
				bad = e != nil
			case "uri":
				u, e := url.Parse(s)
				bad = e != nil || u.Scheme == ""
			default:
				return fmt.Errorf("unsupported format %v", x)
			}
		}
	}
	if bad {
		return fmt.Errorf("%s: fails %s (%v)", strings.TrimSuffix(p, "/"), k, x)
	}
	return nil
}

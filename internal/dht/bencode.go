// Package dht speaks BitTorrent's mainline DHT (BEP 5): a serverless way to
// find the peers that talk about the same network, by announcing a peer
// and looking it up by an identifier both sides derive from the network's
// name alone.
package dht

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
)

// bencode is what the DHT says: strings, integers, lists and dictionaries.
// Decoded values are string, int64, []any and map[string]any.

// errTruncated is returned for a value that ends too early.
var errTruncated = errors.New("bencode: truncated")

// encode appends the bencoding of v.
func encode(dst []byte, v any) ([]byte, error) {
	switch x := v.(type) {
	case string:
		dst = strconv.AppendInt(dst, int64(len(x)), 10)
		dst = append(dst, ':')
		return append(dst, x...), nil
	case []byte:
		dst = strconv.AppendInt(dst, int64(len(x)), 10)
		dst = append(dst, ':')
		return append(dst, x...), nil
	case int:
		return encode(dst, int64(x))
	case int64:
		dst = append(dst, 'i')
		dst = strconv.AppendInt(dst, x, 10)
		return append(dst, 'e'), nil
	case bool:
		if x {
			return encode(dst, int64(1))
		}
		return encode(dst, int64(0))
	case []any:
		dst = append(dst, 'l')
		for _, item := range x {
			var err error
			if dst, err = encode(dst, item); err != nil {
				return nil, err
			}
		}
		return append(dst, 'e'), nil
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		dst = append(dst, 'd')
		for _, k := range keys {
			dst = append(dst, strconv.Itoa(len(k))...)
			dst = append(dst, ':')
			dst = append(dst, k...)
			var err error
			if dst, err = encode(dst, x[k]); err != nil {
				return nil, err
			}
		}
		return append(dst, 'e'), nil
	}
	return nil, fmt.Errorf("bencode: cannot encode %T", v)
}

// decode reads one value from b, returning it and where it ends.
func decode(b []byte) (any, int, error) {
	if len(b) == 0 {
		return nil, 0, errTruncated
	}
	switch {
	case b[0] == 'i':
		end := 1
		for end < len(b) && b[end] != 'e' {
			end++
		}
		if end >= len(b) {
			return nil, 0, errTruncated
		}
		n, err := strconv.ParseInt(string(b[1:end]), 10, 64)
		if err != nil {
			return nil, 0, fmt.Errorf("bencode: integer: %w", err)
		}
		return n, end + 1, nil
	case b[0] == 'l':
		var out []any
		i := 1
		for {
			if i >= len(b) {
				return nil, 0, errTruncated
			}
			if b[i] == 'e' {
				return out, i + 1, nil
			}
			v, n, err := decode(b[i:])
			if err != nil {
				return nil, 0, err
			}
			out = append(out, v)
			i += n
		}
	case b[0] == 'd':
		out := map[string]any{}
		i := 1
		for {
			if i >= len(b) {
				return nil, 0, errTruncated
			}
			if b[i] == 'e' {
				return out, i + 1, nil
			}
			k, n, err := decode(b[i:])
			if err != nil {
				return nil, 0, err
			}
			i += n
			key, ok := k.(string)
			if !ok {
				return nil, 0, errors.New("bencode: dictionary key is not a string")
			}
			v, n, err := decode(b[i:])
			if err != nil {
				return nil, 0, err
			}
			i += n
			out[key] = v
		}
	default:
		colon := -1
		for i := 0; i < len(b) && i < 21; i++ {
			if b[i] == ':' {
				colon = i
				break
			}
			if b[i] < '0' || b[i] > '9' {
				return nil, 0, fmt.Errorf("bencode: unexpected %q", b[0])
			}
		}
		if colon < 0 {
			return nil, 0, errTruncated
		}
		n, err := strconv.Atoi(string(b[:colon]))
		if err != nil {
			return nil, 0, err
		}
		if n < 0 || colon+1+n > len(b) {
			return nil, 0, errTruncated
		}
		return string(b[colon+1 : colon+1+n]), colon + 1 + n, nil
	}
}

// decodeAll reads a whole message, refusing trailing bytes.
func decodeAll(b []byte) (any, error) {
	v, n, err := decode(b)
	if err != nil {
		return nil, err
	}
	if n != len(b) {
		return nil, errors.New("bencode: trailing bytes")
	}
	return v, nil
}

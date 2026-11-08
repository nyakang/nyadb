package solr

import (
	"encoding/binary"
	"math/bits"
	"strconv"
	"strings"
	"unicode/utf8"
)

// compositeHash follows Solr 9.9 CompositeIdRouter.KeyParser, including its
// trailing separator compatibility. Invalid bit allocations are unsupported;
// reproducing Java's masked shifts for malformed keys would hide a routing bug.
func compositeHash(key string) (int32, error) {
	if !utf8.ValidString(key) {
		return 0, ErrInvalid
	}
	i := strings.IndexByte(key, '!')
	if i < 0 {
		return int32(murmur32([]byte(key))), nil
	}
	parts := []string{key[:i]}
	if i < len(key)-1 {
		j := strings.IndexByte(key[i+1:], '!')
		if j < 0 {
			parts = append(parts, key[i+1:])
		} else {
			j += i + 1
			if j < len(key)-1 {
				parts = append(parts, key[i+1:j], key[j+1:])
			} else if i < j-1 {
				parts = append(parts, key[i+1:j])
			}
		}
	}
	if strings.HasSuffix(key, "!") && len(parts) < 3 {
		parts = append(parts, "")
	}
	widths := []int{16, 0}
	if len(parts) == 3 {
		widths = []int{8, 8}
	}
	for n := 0; n < len(parts)-1; n++ {
		if slash := strings.IndexByte(parts[n], '/'); slash > 0 {
			s := parts[n][slash+1:]
			width := 0
			if s != "" {
				for _, ch := range s {
					if ch < '0' || ch > '9' {
						return 0, ErrInvalid
					}
				}
				var err error
				width, err = strconv.Atoi(s)
				if err != nil || width > 32 {
					return 0, ErrInvalid
				}
			}
			widths[n], parts[n] = width, parts[n][:slash]
		}
	}
	if widths[0]+widths[1] > 32 {
		return 0, ErrInvalid
	}
	top := func(n int) uint32 {
		if n == 0 {
			return 0
		}
		return ^uint32(0) << (32 - n)
	}
	masks := []uint32{top(widths[0]), ^top(widths[0])}
	if len(parts) == 3 {
		masks = []uint32{top(widths[0]), top(widths[0]+widths[1]) ^ top(widths[0]), ^top(widths[0] + widths[1])}
	}
	var hash uint32
	for n, part := range parts {
		hash |= murmur32([]byte(part)) & masks[n]
	}
	return int32(hash), nil
}

// Solr's CharSequence hash encodes valid Unicode as UTF-8, with seed zero.
func murmur32(data []byte) uint32 {
	var h uint32
	mix := func(k uint32) uint32 {
		return bits.RotateLeft32(k*0xcc9e2d51, 15) * 0x1b873593
	}
	n := len(data) &^ 3
	for i := 0; i < n; i += 4 {
		h ^= mix(binary.LittleEndian.Uint32(data[i : i+4]))
		h = bits.RotateLeft32(h, 13)*5 + 0xe6546b64
	}
	var tail uint32
	for i := len(data) - 1; i >= n; i-- {
		tail = tail<<8 | uint32(data[i])
	}
	h ^= mix(tail)
	h ^= uint32(len(data))
	h ^= h >> 16
	h *= 0x85ebca6b
	h ^= h >> 13
	h *= 0xc2b2ae35
	h ^= h >> 16
	return h
}

type shardRange struct{ min, max int32 }

func parseShardRange(text string) (shardRange, error) {
	parts := strings.Split(text, "-")
	if len(parts) != 2 {
		return shardRange{}, ErrProtocol
	}
	lo, e := strconv.ParseUint(parts[0], 16, 32)
	hi, f := strconv.ParseUint(parts[1], 16, 32)
	if e != nil || f != nil || int32(lo) > int32(hi) {
		return shardRange{}, ErrProtocol
	}
	return shardRange{int32(lo), int32(hi)}, nil
}

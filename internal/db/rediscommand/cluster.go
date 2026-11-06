package rediscommand

import (
	"strconv"
	"strings"
	"sync"
)

// Slot follows Redis CRC16/XMODEM and its first nonempty {...} hash tag rule.
func Slot(key string) int {
	if start := strings.IndexByte(key, '{'); start >= 0 {
		if end := strings.IndexByte(key[start+1:], '}'); end > 0 {
			key = key[start+1 : start+1+end]
		}
	}
	var crc uint16
	for i := range len(key) {
		crc ^= uint16(key[i]) << 8
		for range 8 {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return int(crc & 16383)
}

var tagsOnce sync.Once
var slotTags [16384]string

// SlotTag provides an immutable synthetic tag for temporary keys, including
// binary keys or keys with no usable tag. No caller receives the shared table.
func SlotTag(slot int) string {
	if slot < 0 || slot >= len(slotTags) {
		return ""
	}
	tagsOnce.Do(func() {
		remaining := len(slotTags)
		for i := 0; remaining > 0; i++ {
			tag := strconv.Itoa(i)
			s := Slot(tag)
			if slotTags[s] == "" {
				slotTags[s] = tag
				remaining--
			}
		}
	})
	return slotTags[slot]
}

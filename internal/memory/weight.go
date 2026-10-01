package memory

import (
	"reflect"
	"unsafe"
)

// Weight conservatively charges backing storage and map overhead. It is an
// admission estimate, not an allocator statistic, and v must be immutable or
// protected by its owner's lock while traversed.
func Weight(v any) int64 {
	seen := map[allocation]charge{}
	return weight(reflect.ValueOf(v), seen, true)
}

type allocation struct {
	ptr uintptr
	typ reflect.Type
}

type charge struct {
	bytes  int64
	length int
}

func weight(v reflect.Value, seen map[allocation]charge, inline bool) int64 {
	if !v.IsValid() {
		return 0
	}
	var n int64
	if inline {
		n = int64(v.Type().Size())
	}
	visit := func(ptr uintptr) bool {
		key := allocation{ptr, v.Type()}
		if ptr == 0 || seen[key].length != 0 {
			return false
		}
		seen[key] = charge{length: -1}
		return true
	}
	switch v.Kind() {
	case reflect.String:
		s := v.String()
		key := allocation{uintptr(unsafe.Pointer(unsafe.StringData(s))), v.Type()}
		// Growing partials may share a prefix allocation. Charge the largest
		// retained length, independent of the order the graph is traversed.
		if bytes := int64(len(s)); bytes > seen[key].bytes {
			n += bytes - seen[key].bytes
			seen[key] = charge{bytes: bytes}
		}
	case reflect.Pointer:
		if !v.IsNil() && visit(v.Pointer()) {
			n += weight(v.Elem(), seen, true)
		}
	case reflect.Interface:
		if !v.IsNil() {
			n += weight(v.Elem(), seen, true)
		}
	case reflect.Slice:
		key := allocation{v.Pointer(), v.Type()}
		old := seen[key]
		bytes := int64(v.Cap()) * int64(v.Type().Elem().Size())
		n += max(0, bytes-old.bytes)
		// Register before descent for cycles, and visit newly exposed elements
		// when a later alias includes more of the same backing slice.
		seen[key] = charge{bytes: max(bytes, old.bytes), length: max(v.Len(), old.length)}
		for i := old.length; i < v.Len(); i++ {
			n += weight(v.Index(i), seen, false)
		}
	case reflect.Map:
		if !v.IsNil() && visit(uintptr(v.UnsafePointer())) {
			// Why: a decoded JSON map owns a table as well as keys/values.
			// Charging only JSON bytes grossly undercounts schema-heavy histories.
			n += 64 + int64(v.Len())*(64+int64(v.Type().Key().Size()+v.Type().Elem().Size()))
			iter := v.MapRange()
			for iter.Next() {
				n += weight(iter.Key(), seen, false) + weight(iter.Value(), seen, false)
			}
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			n += weight(v.Field(i), seen, false)
		}
	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			n += weight(v.Index(i), seen, false)
		}
	}
	return n
}

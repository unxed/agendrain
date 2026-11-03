package drain

// Schema-driven protobuf decoding for Cursor Agent CLI records. The tables in
// cursor_agent_schema.go are generated from the CLI bundle's own descriptors
// (scripts/cursor_agent_schema.py). Field names, numbers, oneofs and enum names
// come from that schema, never from guessing at sample data.

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

type caField struct {
	num      int
	name     string
	typ      string // scalar, "M:Name", "E:Name"
	repeated bool
	isMap    bool
	key, val string
}

type caMessage struct {
	name   string
	fields map[int]*caField
}

var (
	caSchemaOnce sync.Once
	caMessages   map[string]*caMessage
	caEnumNames  map[string]map[uint64]string
)

func caLoadSchema() {
	caSchemaOnce.Do(func() {
		caMessages = map[string]*caMessage{}
		for name, spec := range cursorAgentSchema {
			m := &caMessage{name: name, fields: map[int]*caField{}}
			for _, item := range strings.Fields(spec) {
				parts := strings.Split(item, ":")
				if len(parts) < 3 {
					continue
				}
				num, err := strconv.Atoi(parts[0])
				if err != nil {
					continue
				}
				f := &caField{num: num, name: parts[1]}
				rest := strings.Join(parts[2:], ":")
				switch {
				case strings.HasPrefix(rest, "map<"):
					end := strings.IndexByte(rest, '>')
					inner := rest[4:end]
					comma := strings.IndexByte(inner, ',')
					f.isMap, f.key, f.val = true, inner[:comma], inner[comma+1:]
				case strings.HasPrefix(rest, "M:") || strings.HasPrefix(rest, "E:"):
					f.typ = rest[:2]
					tail := rest[2:]
					if i := strings.IndexByte(tail, ':'); i >= 0 {
						tail = tail[:i]
					}
					if strings.HasSuffix(tail, "*") {
						f.repeated = true
						tail = strings.TrimSuffix(tail, "*")
					}
					f.typ += tail
				default:
					typ := rest
					if i := strings.IndexByte(typ, ':'); i >= 0 {
						typ = typ[:i]
					}
					if strings.HasSuffix(typ, "*") {
						f.repeated = true
						typ = strings.TrimSuffix(typ, "*")
					}
					f.typ = typ
				}
				m.fields[num] = f
			}
			caMessages[name] = m
		}
		caEnumNames = map[string]map[uint64]string{}
		for name, spec := range cursorAgentEnums {
			values := map[uint64]string{}
			for _, item := range strings.Fields(spec) {
				if i := strings.IndexByte(item, '='); i > 0 {
					if n, err := strconv.ParseUint(item[:i], 10, 64); err == nil {
						values[n] = item[i+1:]
					}
				}
			}
			caEnumNames[name] = values
		}
	})
}

// caAttachment is a binary field value retained as a ZIP asset.
type caAttachment struct {
	path string
	data []byte
}

type caDecoder struct {
	r           *cursorAgentReader
	attachments []caAttachment
	skip        map[string]bool // "Message.field" names that are never exported
}

const caValueMessage = "google.protobuf.Value"

func (d *caDecoder) unknown(message string, num int) {
	d.r.b.warn(fmt.Sprintf("Cursor Agent %s has field %d that is not in the verified 2026.10.01-e373342 schema; raw value retained as _unknown_fields", message, num))
}

// message decodes data as the named schema message into a JSON-ready object.
func (d *caDecoder) message(name string, data []byte, depth int) (obj, error) {
	if depth > 64 {
		return nil, fmt.Errorf("Cursor Agent protobuf message nesting exceeds 64")
	}
	if name == caValueMessage {
		v, err := d.value(data, depth)
		return obj{"value": v}, err
	}
	caLoadSchema()
	schema := caMessages[name]
	if schema == nil {
		return nil, fmt.Errorf("Cursor Agent schema has no message %q", name)
	}
	p, err := d.r.proto(data)
	if err != nil {
		return nil, err
	}
	nums := make([]int, 0, len(p))
	for n := range p {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	out := obj{}
	unknown := obj{}
	for _, num := range nums {
		values := p[num]
		f := schema.fields[num]
		if f != nil && d.skip[name+"."+f.name] {
			continue
		}
		if f == nil {
			d.unknown(name, num)
			unknown[strconv.Itoa(num)] = d.rawValues(name, num, values)
			continue
		}
		v, ok, err := d.fieldValue(name, f, values, depth)
		if err != nil {
			return nil, err
		}
		if !ok {
			d.r.b.warn(fmt.Sprintf("Cursor Agent %s.%s has an unexpected wire type; raw value retained as _unknown_fields", name, f.name))
			unknown[strconv.Itoa(num)] = d.rawValues(name, num, values)
			continue
		}
		out[f.name] = v
	}
	if len(unknown) > 0 {
		out["_unknown_fields"] = unknown
	}
	return out, nil
}

func (d *caDecoder) rawValues(message string, num int, values []cursorAgentWire) any {
	var out []any
	for _, v := range values {
		switch v.kind {
		case 0:
			out = append(out, v.num)
		case 2:
			if utf8.Valid(v.data) && !strings.ContainsRune(string(v.data), 0) {
				out = append(out, string(v.data))
			} else if len(v.data) <= 256 {
				out = append(out, "hex:"+hex.EncodeToString(v.data))
			} else {
				out = append(out, d.bytesValue(fmt.Sprintf("%s.%d", message, num), v.data))
			}
		default:
			out = append(out, "hex:"+hex.EncodeToString(v.data))
		}
	}
	return out
}

func (d *caDecoder) bytesValue(path string, data []byte) any {
	if utf8.Valid(data) && !strings.ContainsRune(string(data), 0) {
		return string(data)
	}
	d.attachments = append(d.attachments, caAttachment{path: path, data: data})
	return obj{"binary_bytes": len(data), "asset": d.r.b.asset(data)}
}

func (d *caDecoder) fieldValue(message string, f *caField, values []cursorAgentWire, depth int) (any, bool, error) {
	if f.isMap {
		out := obj{}
		for _, entry := range values {
			if entry.kind != 2 {
				return nil, false, nil
			}
			p, err := d.r.proto(entry.data)
			if err != nil {
				return nil, false, err
			}
			var key any = ""
			if kv := p[1]; len(kv) > 0 {
				key, _, err = d.single(message, &caField{name: f.name, typ: f.key}, kv[len(kv)-1], depth)
				if err != nil {
					return nil, false, err
				}
			}
			var val any
			if vv := p[2]; len(vv) > 0 {
				var ok bool
				val, ok, err = d.single(message, &caField{name: f.name + "." + "value", typ: f.val}, vv[len(vv)-1], depth)
				if err != nil || !ok {
					return nil, ok, err
				}
			}
			out[fmt.Sprint(key)] = val
		}
		return out, true, nil
	}
	if !f.repeated {
		v, ok, err := d.single(message, f, values[len(values)-1], depth)
		return v, ok, err
	}
	var out []any
	for _, w := range values {
		// Packed scalar encoding.
		if w.kind == 2 && f.typ != "string" && f.typ != "bytes" && !strings.HasPrefix(f.typ, "M:") {
			items, ok := d.packed(f.typ, w.data)
			if !ok {
				return nil, false, nil
			}
			out = append(out, items...)
			continue
		}
		v, ok, err := d.single(message, f, w, depth)
		if err != nil || !ok {
			return nil, ok, err
		}
		out = append(out, v)
	}
	return out, true, nil
}

func (d *caDecoder) packed(typ string, data []byte) ([]any, bool) {
	var out []any
	for len(data) > 0 {
		switch typ {
		case "double", "float":
			size := 8
			if typ == "float" {
				size = 4
			}
			if len(data) < size {
				return nil, false
			}
			out = append(out, caFixed(typ, data[:size]))
			data = data[size:]
		default:
			n, count := binary.Uvarint(data)
			if count <= 0 {
				return nil, false
			}
			v, ok := d.varint(typ, n)
			if !ok {
				return nil, false
			}
			out = append(out, v)
			data = data[count:]
		}
	}
	return out, true
}

func caFixed(typ string, data []byte) any {
	if typ == "float" {
		return math.Float32frombits(binary.LittleEndian.Uint32(data))
	}
	return math.Float64frombits(binary.LittleEndian.Uint64(data))
}

func (d *caDecoder) varint(typ string, n uint64) (any, bool) {
	switch typ {
	case "bool":
		return n != 0, true
	case "int32":
		return int32(n), true
	case "int64":
		return int64(n), true
	case "uint32":
		return uint32(n), true
	case "uint64":
		return n, true
	case "sint32":
		return int32(uint32(n>>1) ^ -uint32(n&1)), true
	}
	if strings.HasPrefix(typ, "E:") {
		caLoadSchema()
		if name, ok := caEnumNames[typ[2:]][n]; ok {
			return name, true
		}
		d.r.b.warn(fmt.Sprintf("Cursor Agent enum %s has value %d that is not in the verified schema; number retained", typ[2:], n))
		return n, true
	}
	return nil, false
}

func (d *caDecoder) single(message string, f *caField, w cursorAgentWire, depth int) (any, bool, error) {
	switch {
	case strings.HasPrefix(f.typ, "M:"):
		if w.kind != 2 {
			return nil, false, nil
		}
		name := f.typ[2:]
		if name == caValueMessage {
			v, err := d.value(w.data, depth+1)
			return v, true, err
		}
		v, err := d.message(name, w.data, depth+1)
		return v, true, err
	case strings.HasPrefix(f.typ, "E:"), f.typ == "bool", f.typ == "int32", f.typ == "int64", f.typ == "uint32", f.typ == "uint64", f.typ == "sint32":
		if w.kind != 0 {
			return nil, false, nil
		}
		v, ok := d.varint(f.typ, w.num)
		return v, ok, nil
	case f.typ == "double":
		if w.kind != 1 {
			return nil, false, nil
		}
		return caFixed(f.typ, w.data), true, nil
	case f.typ == "float":
		if w.kind != 5 {
			return nil, false, nil
		}
		return caFixed(f.typ, w.data), true, nil
	case f.typ == "string":
		if w.kind != 2 {
			return nil, false, nil
		}
		if !utf8.Valid(w.data) {
			return d.bytesValue(message+"."+f.name, w.data), true, nil
		}
		return string(w.data), true, nil
	case f.typ == "bytes":
		if w.kind != 2 {
			return nil, false, nil
		}
		if strings.HasSuffix(f.name, "blob_id") || strings.HasSuffix(f.name, "blob_ids") {
			return "blob:" + hex.EncodeToString(w.data), true, nil
		}
		return d.bytesValue(message+"."+f.name, w.data), true, nil
	}
	return nil, false, nil
}

// value decodes google.protobuf.Value into plain JSON.
func (d *caDecoder) value(data []byte, depth int) (any, error) {
	if depth > 64 {
		return nil, fmt.Errorf("Cursor Agent protobuf message nesting exceeds 64")
	}
	p, err := d.r.proto(data)
	if err != nil {
		return nil, err
	}
	switch {
	case len(p[2]) > 0 && p[2][0].kind == 1:
		return math.Float64frombits(binary.LittleEndian.Uint64(p[2][0].data)), nil
	case len(p[3]) > 0:
		return string(p.data(3)), nil
	case len(p[4]) > 0:
		return p.number(4) != 0, nil
	case len(p[5]) > 0:
		st, err := d.r.proto(p.data(5))
		if err != nil {
			return nil, err
		}
		out := obj{}
		for _, entry := range st[1] {
			e, err := d.r.proto(entry.data)
			if err != nil {
				return nil, err
			}
			var v any
			if raw := e.data(2); len(e[2]) > 0 {
				if v, err = d.value(raw, depth+1); err != nil {
					return nil, err
				}
			}
			out[e.text(1)] = v
		}
		return out, nil
	case len(p[6]) > 0:
		list, err := d.r.proto(p.data(6))
		if err != nil {
			return nil, err
		}
		out := []any{}
		for _, item := range list[1] {
			v, err := d.value(item.data, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	}
	return nil, nil
}

// caKnown warns about field numbers of an already hand-decoded message that
// are absent from the verified schema.
func (r *cursorAgentReader) caKnown(message string, p cursorAgentProto) {
	caLoadSchema()
	schema := caMessages[message]
	for num := range p {
		if schema != nil && schema.fields[num] == nil {
			r.b.warn(fmt.Sprintf("Cursor Agent %s has field %d that is not in the verified 2026.10.01-e373342 schema; it is not decoded", message, num))
		}
	}
}

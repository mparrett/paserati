package perfdata

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// The v2 on-disk format partitions a snapshot by machine profile instead of
// carrying one flat `machine`. Ratios only normalize within a CPU model — the
// calibration anchor drifts across tiers relative to the memory-bound workloads
// it normalizes — so numbers from two tiers are not comparable and must never be
// merged. v1 forced one snapshot per commit, which meant a re-run on a different
// tier OVERWROTE the previous tier's measurement; keying by machine lets the same
// commit hold one snapshot per tier instead.
//
// v1 (Baseline) is still the format bench-ratchet writes. These types exist so a
// migration can convert a v1 corpus without re-measuring anything.

// MachineBaseline is one machine profile's self-contained baseline: the host that
// captured it, its calibration anchor, and the per-benchmark metrics. All pairing
// and comparison happens within a single MachineBaseline.
type MachineBaseline struct {
	CapturedAt    string                    `json:"captured_at"`
	CapturedAtSHA string                    `json:"captured_at_sha"`
	Machine       Machine                   `json:"machine"`
	Anchor        Anchor                    `json:"anchor"`
	Benchmarks    map[string]BenchmarkEntry `json:"benchmarks"`
}

// BaselineV2 maps a machine-profile key (see MachineKey) to that profile's data.
type BaselineV2 struct {
	Version  int                        `json:"version"`
	Machines map[string]MachineBaseline `json:"machines"`
}

// MachineKey is the partition key for a machine profile: architecture plus CPU
// model, the two attributes that determine relative benchmark cost. GoVersion and
// NumCPU are deliberately NOT in the key — a Go upgrade or a differently-
// provisioned host shouldn't fragment the profile — they're surfaced on mismatch
// instead. Matches let-go's key so converted data is directly comparable.
func MachineKey(m Machine) string {
	return m.Arch + "/" + m.CPUModel
}

// MachineSlug is MachineKey rendered filename-safe: lowercased, with every run of
// non-alphanumerics collapsed to a single dash. Snapshot filenames carry it so two
// tiers capturing the same commit land on distinct names rather than overwriting.
//
// Byte-identical to the shell derivation in let-go's perf-timeline.yml
// (`tr 'A-Z' 'a-z' | tr -c 'a-z0-9' '-' | sed -E 's/-+/-/g; s/^-+//; s/-+$//'`),
// so a corpus converted here and one captured there use the same names.
func MachineSlug(m Machine) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(m.Arch + "-" + m.CPUModel) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		case !prevDash:
			b.WriteByte('-')
			prevDash = true
		}
	}
	if s := strings.Trim(b.String(), "-"); s != "" {
		return s
	}
	return "unknown"
}

// ToMachineBaseline reinterprets a v1 snapshot as one machine profile. Every field
// carries over unchanged — this is a restructure, not a recomputation.
func (b Baseline) ToMachineBaseline() MachineBaseline {
	return MachineBaseline{
		CapturedAt:    b.CapturedAt,
		CapturedAtSHA: b.CapturedAtSHA,
		Machine:       b.Machine,
		Anchor:        b.Anchor,
		Benchmarks:    b.Benchmarks,
	}
}

// ToV2 wraps a v1 snapshot as a single-profile v2 snapshot.
func (b Baseline) ToV2() BaselineV2 {
	return BaselineV2{
		Version:  2,
		Machines: map[string]MachineBaseline{MachineKey(b.Machine): b.ToMachineBaseline()},
	}
}

// ConvertV1ToV2 restructures a v1 snapshot into v2 WITHOUT reserializing any
// value. Each field is carried across as a verbatim json.RawMessage and the
// original key order is preserved, so numbers keep their exact source formatting
// and fields the source omitted stay omitted.
//
// Round-tripping through the typed structs instead would be semantically
// equivalent but not textually so: `samples` entries written by the workflow's jq
// step omit allocs_per_op/bytes_per_op, and Go would re-emit them as explicit
// zeros. Only whitespace changes here, which makes "the payload is unchanged" a
// property you can check with a diff rather than a claim you have to trust.
func ConvertV1ToV2(raw []byte) ([]byte, Machine, error) {
	var probe struct {
		Machine Machine `json:"machine"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, Machine{}, err
	}

	fields, err := objectFields(raw)
	if err != nil {
		return nil, Machine{}, err
	}

	var inner bytes.Buffer
	inner.WriteByte('{')
	first := true
	for _, f := range fields {
		if f.key == "version" { // the version moves to the outer document
			continue
		}
		if !first {
			inner.WriteByte(',')
		}
		first = false
		k, _ := json.Marshal(f.key)
		inner.Write(k)
		inner.WriteByte(':')
		inner.Write(f.value)
	}
	inner.WriteByte('}')

	key, _ := json.Marshal(MachineKey(probe.Machine))
	var doc bytes.Buffer
	doc.WriteString(`{"version":2,"machines":{`)
	doc.Write(key)
	doc.WriteByte(':')
	doc.Write(inner.Bytes())
	doc.WriteString(`}}`)

	var pretty bytes.Buffer
	if err := json.Indent(&pretty, doc.Bytes(), "", "  "); err != nil {
		return nil, Machine{}, err
	}
	pretty.WriteByte('\n')
	return pretty.Bytes(), probe.Machine, nil
}

type jsonField struct {
	key   string
	value json.RawMessage
}

// objectFields decodes a JSON object's top level into ordered key/raw-value pairs.
// encoding/json unmarshals objects into maps, which lose order; the token stream
// keeps it.
func objectFields(raw []byte) ([]jsonField, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("expected a JSON object, got %v", tok)
	}
	var out []jsonField
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := kt.(string)
		if !ok {
			return nil, fmt.Errorf("expected an object key, got %v", kt)
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		out = append(out, jsonField{key: key, value: v})
	}
	return out, nil
}

// IsV2 reports whether raw JSON is already in the machine-partitioned format.
// Detection is by the presence of a non-empty `machines` object rather than by the
// version number, so a file with a stale or missing version still classifies
// correctly. This is what makes migration idempotent: an already-v2 file is
// recognized and left byte-for-byte alone rather than re-marshalled.
func IsV2(raw []byte) bool {
	var probe struct {
		Machines map[string]json.RawMessage `json:"machines"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	return len(probe.Machines) > 0
}

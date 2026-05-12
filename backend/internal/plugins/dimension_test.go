package plugins

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestDimension_UnmarshalNumeric(t *testing.T) {
	var d Dimension
	if err := json.Unmarshal([]byte(`320`), &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if d.Auto {
		t.Errorf("Auto = true, want false for numeric value")
	}
	if d.Px != 320 {
		t.Errorf("Px = %d, want 320", d.Px)
	}
}

func TestDimension_UnmarshalAuto(t *testing.T) {
	var d Dimension
	if err := json.Unmarshal([]byte(`"auto"`), &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !d.Auto {
		t.Errorf("Auto = false, want true for \"auto\"")
	}
	if d.Px != 0 {
		t.Errorf("Px = %d, want 0 when auto", d.Px)
	}
}

func TestDimension_UnmarshalRejectsOtherStrings(t *testing.T) {
	var d Dimension
	err := json.Unmarshal([]byte(`"fluid"`), &d)
	if err == nil {
		t.Errorf("expected error for unknown string, got nil (d=%+v)", d)
	}
}

func TestDimension_UnmarshalRejectsNegative(t *testing.T) {
	var d Dimension
	err := json.Unmarshal([]byte(`-50`), &d)
	if err == nil {
		t.Errorf("expected error for negative pixel value, got nil (d=%+v)", d)
	}
}

func TestDimension_MarshalNumeric(t *testing.T) {
	d := Dimension{Px: 320}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != `320` {
		t.Errorf("marshaled = %s, want 320", b)
	}
}

func TestDimension_MarshalAuto(t *testing.T) {
	d := Dimension{Auto: true}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != `"auto"` {
		t.Errorf("marshaled = %s, want \"auto\"", b)
	}
}

func TestDimension_RoundTripAuto(t *testing.T) {
	src := `"auto"`
	var d Dimension
	if err := json.Unmarshal([]byte(src), &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(out) != src {
		t.Errorf("round-trip = %s, want %s", out, src)
	}
}

// OverlayConfig must accept "auto" for width/height in manifest JSON and
// expose it via the Auto flag on the parsed Dimension. This is the
// integration point host code (aggregator, edit mode) reads.
func TestOverlayConfig_ParsesAutoWidth(t *testing.T) {
	raw := []byte(`{"file":"o.html","width":"auto","height":120,"anchor":"top-left"}`)
	var oc OverlayConfig
	if err := json.Unmarshal(raw, &oc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !oc.Width.Auto {
		t.Errorf("Width.Auto = false, want true")
	}
	if oc.Height.Auto {
		t.Errorf("Height.Auto = true, want false")
	}
	if oc.Height.Px != 120 {
		t.Errorf("Height.Px = %d, want 120", oc.Height.Px)
	}
}

// Auto must round-trip as the literal string "auto" in the marshaled
// wire format so the frontend (overlay.html, edit mode) can read it
// verbatim. Numeric values must round-trip as integers.
func TestOverlayConfig_RoundTripWireFormat(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`{"width":"auto","height":120}`, `"width":"auto","height":120`},
		{`{"width":320,"height":"auto"}`, `"width":320,"height":"auto"`},
		{`{"width":"auto","height":"auto"}`, `"width":"auto","height":"auto"`},
		{`{"width":320,"height":120}`, `"width":320,"height":120`},
	}
	for _, tc := range cases {
		var oc OverlayConfig
		if err := json.Unmarshal([]byte(tc.in), &oc); err != nil {
			t.Errorf("unmarshal %s: %v", tc.in, err)
			continue
		}
		out, err := json.Marshal(oc)
		if err != nil {
			t.Errorf("marshal: %v", err)
			continue
		}
		if !bytes.Contains(out, []byte(tc.want)) {
			t.Errorf("input %s: marshaled %s, want fragment %s", tc.in, out, tc.want)
		}
	}
}

// max_width / max_height clamp content-driven growth so an auto-sized
// widget can't run away and cover the stream. min_width / min_height
// keep an empty body from collapsing to zero. All four are optional;
// absent means "no clamp" on the host side.
func TestOverlayConfig_ParsesMinMax(t *testing.T) {
	raw := []byte(`{"file":"o.html","width":"auto","height":"auto","anchor":"top-left","min_width":80,"max_width":600,"min_height":40,"max_height":400}`)
	var oc OverlayConfig
	if err := json.Unmarshal(raw, &oc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if oc.MinWidth != 80 || oc.MaxWidth != 600 {
		t.Errorf("min/max width = %d/%d, want 80/600", oc.MinWidth, oc.MaxWidth)
	}
	if oc.MinHeight != 40 || oc.MaxHeight != 400 {
		t.Errorf("min/max height = %d/%d, want 40/400", oc.MinHeight, oc.MaxHeight)
	}
}

func TestOverlayConfig_MinMaxOmittedDefaultsToZero(t *testing.T) {
	raw := []byte(`{"file":"o.html","width":320,"height":120,"anchor":"top-left"}`)
	var oc OverlayConfig
	if err := json.Unmarshal(raw, &oc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if oc.MinWidth != 0 || oc.MaxWidth != 0 || oc.MinHeight != 0 || oc.MaxHeight != 0 {
		t.Errorf("expected zero min/max when omitted, got %+v", oc)
	}
}

func TestOverlayConfig_NumericWidthsStillParse(t *testing.T) {
	raw := []byte(`{"file":"o.html","width":320,"height":240,"anchor":"top-left"}`)
	var oc OverlayConfig
	if err := json.Unmarshal(raw, &oc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if oc.Width.Auto || oc.Height.Auto {
		t.Errorf("Auto flags set on numeric widths: w=%+v h=%+v", oc.Width, oc.Height)
	}
	if oc.Width.Px != 320 || oc.Height.Px != 240 {
		t.Errorf("Px values wrong: w=%d h=%d, want 320/240", oc.Width.Px, oc.Height.Px)
	}
}

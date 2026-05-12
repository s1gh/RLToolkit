package overrides

import (
	"encoding/json"
	"testing"

	"rl-toolkit/backend/internal/plugins"
)

// User-side override of width/height to "auto" means "switch this axis
// to content-driven sizing", overriding whatever the manifest declared.
// User can also override "auto" back to a fixed pixel count.
func TestOverride_AcceptsAutoDimension(t *testing.T) {
	raw := []byte(`{"width":"auto","height":120}`)
	var o Override
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if o.Width == nil || !o.Width.Auto {
		t.Errorf("Width = %+v, want non-nil with Auto=true", o.Width)
	}
	if o.Height == nil || o.Height.Auto || o.Height.Px != 120 {
		t.Errorf("Height = %+v, want non-nil Px=120", o.Height)
	}
}

func TestOverride_NumericDimensionStillWorks(t *testing.T) {
	raw := []byte(`{"width":320,"height":240}`)
	var o Override
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if o.Width == nil || o.Width.Auto || o.Width.Px != 320 {
		t.Errorf("Width = %+v, want non-nil Px=320", o.Width)
	}
}

func TestOverride_ValidateRejectsTooLargeAuto(t *testing.T) {
	// "auto" itself has no pixel count, so size validation must skip
	// the upper-bound check for auto values rather than reject them.
	auto := plugins.Dimension{Auto: true}
	o := Override{Width: &auto}
	if err := o.Validate(); err != nil {
		t.Errorf("auto width rejected: %v", err)
	}
}

func TestOverride_ValidateStillRejectsOversizePx(t *testing.T) {
	huge := plugins.Dimension{Px: 99999}
	o := Override{Width: &huge}
	if err := o.Validate(); err == nil {
		t.Error("expected validation error for oversize Px width")
	}
}

// User opts in to a manifest-declared max. Editor PUTs max_width=N when
// dragging the resize handle on an auto axis.
func TestOverride_AcceptsMaxFields(t *testing.T) {
	raw := []byte(`{"max_width":500,"max_height":300,"min_width":80,"min_height":40}`)
	var o Override
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if o.MaxWidth == nil || *o.MaxWidth != 500 {
		t.Errorf("MaxWidth = %v, want 500", o.MaxWidth)
	}
	if o.MaxHeight == nil || *o.MaxHeight != 300 {
		t.Errorf("MaxHeight = %v, want 300", o.MaxHeight)
	}
	if o.MinWidth == nil || *o.MinWidth != 80 {
		t.Errorf("MinWidth = %v, want 80", o.MinWidth)
	}
	if o.MinHeight == nil || *o.MinHeight != 40 {
		t.Errorf("MinHeight = %v, want 40", o.MinHeight)
	}
}

func TestOverride_PartialMergePreservesDimensionFields(t *testing.T) {
	s, _ := New(t.TempDir())
	auto := plugins.Dimension{Auto: true}
	if _, err := s.MergeOne("p1", Override{Width: &auto}); err != nil {
		t.Fatal(err)
	}
	max := 600
	merged, err := s.MergeOne("p1", Override{MaxWidth: &max})
	if err != nil {
		t.Fatal(err)
	}
	if merged.Width == nil || !merged.Width.Auto {
		t.Errorf("partial merge dropped Width: %+v", merged)
	}
	if merged.MaxWidth == nil || *merged.MaxWidth != 600 {
		t.Errorf("MaxWidth = %v, want 600", merged.MaxWidth)
	}
}

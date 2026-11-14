package authoring

import (
	"math"
	"strings"
)

// Present selects semantic rows without changing layout identities, legacy
// element keys, canvas sources, or the full live-text customization source
func Present(view Display, content *Presentation, width, height int) Display {
	wide := width > height*13/10
	if content == nil || content.Kind == "" || width <= 0 || height <= 0 || width > 16384 || height > 16384 {
		return view
	}
	primary, label, detail := content.Primary, content.Label, content.Detail
	if label == primary {
		label = ""
	}
	if detail == label || detail == primary {
		detail = ""
	}
	view.Feedback = CloneFeedback(view.Feedback)
	field := func(value string, rect [4]int, size float64) map[string]any {
		if value == "" {
			rect[2], rect[3] = 0, 0
		}
		return map[string]any{"value": value, "rect": rect, "font": map[string]any{"size": size, "fit": true}}
	}
	if wide {
		icon := min(width*24/100, height*70/100)
		view.Feedback["icon"] = map[string]any{"value": view.Image, "rect": [4]int{4, (100 - icon*100/height) / 2, icon * 200 / width, icon * 100 / height}}
		view.Feedback["line0"] = field(primary, [4]int{58, 8, 138, 48}, 32)
		view.Feedback["line1"] = field(label, [4]int{58, 58, 138, 18}, 13)
		view.Feedback["line2"] = field(detail, [4]int{58, 78, 138, 16}, 11)
	} else {
		view.Feedback["icon"] = map[string]any{"value": view.Image, "rect": [4]int{76, 2, 48, 24}}
		view.Feedback["line0"] = field(primary, [4]int{6, 28, 188, 40}, 30)
		view.Feedback["line1"] = field(label, [4]int{6, 70, 188, 14}, 11)
		view.Feedback["line2"] = field(detail, [4]int{6, 85, 188, 11}, 9)
	}
	if content.Kind == "command" && primary == "" {
		if wide {
			icon := min(width*36/100, height*80/100)
			view.Feedback["icon"] = map[string]any{"value": view.Image, "rect": [4]int{4, (100 - icon*100/height) / 2, icon * 200 / width, icon * 100 / height}}
			view.Feedback["line1"] = field(label, [4]int{82, 20, 114, 60}, 24)
		} else {
			view.Feedback["icon"] = map[string]any{"value": view.Image, "rect": [4]int{45, 5, 110, 55}}
			view.Feedback["line1"] = field(label, [4]int{6, 67, 188, 26}, 16)
		}
	}
	view.Feedback["indicator"] = map[string]any{"rect": [4]int{0, 0, 0, 0}}
	if content.ShowProgress && !math.IsNaN(content.Progress) && !math.IsInf(content.Progress, 0) {
		view.Feedback["indicator"] = map[string]any{"value": math.Max(0, math.Min(100, content.Progress)), "rect": [4]int{6, 97, 188, 3}}
	}
	return view
}

// ConciseStatus is a safe default for a connector that still returns legacy
// text. Drivers may supply richer content through optional Presentation below
func ConciseStatus(text string) Presentation {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	p := Presentation{Kind: "value"}
	if len(lines) > 0 {
		p.Primary = lines[0]
	}
	if len(lines) > 1 {
		p.Label = lines[1]
	}
	if len(lines) > 2 {
		p.Detail = lines[2]
	}
	if strings.HasPrefix(text, "Unavailable") {
		return Presentation{Kind: "command", Label: "Unavailable"}
	}
	return p
}

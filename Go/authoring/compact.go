package authoring

// CompactDisplay gives a semantic value and label more room on small square keys
// It preserves the full title, saved element identities, and the input snapshot
func CompactDisplay(view Display, width, height int) Display {
	view.Feedback = CloneFeedback(view.Feedback)
	if width <= 0 || height <= 0 || height >= 80 || width > height*13/10 {
		return view
	}
	primary, _ := view.Feedback["line0"].(map[string]any)
	label, _ := view.Feedback["line1"].(map[string]any)
	icon, _ := view.Feedback["icon"].(map[string]any)
	if primary["value"] == "" || primary["value"] == nil || label == nil || icon == nil {
		return view
	}
	view.Feedback["line2"] = map[string]any{"rect": [4]int{}}
	size := min(width, height) * 37 / 100
	icon["rect"] = [4]int{(width - size) * 100 / width, 2, size * 200 / width, size * 100 / height}
	primary["rect"] = [4]int{7, 40, 186, 34}
	label["rect"] = [4]int{7, 75, 186, 21}
	label["font"] = map[string]any{"size": 19, "fit": true}
	return view
}

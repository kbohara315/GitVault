package desktop

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"gitvault/internal/secrets"
)

const (
	secretInputFontSize = 14
	secretInputLeft     = 12
	secretInputTop      = 10
	secretInputWidth    = 560
)

// secretDraft owns the editable credential only as bytes. MyGo's native text
// callbacks carry transient strings, but this client never stores or returns
// the credential text: native queries receive a same-length mask instead.
type secretDraft struct {
	value             []byte
	readClipboard     func() string
	selection         ui.TextInputRange
	selectionReversed bool
	selectionAnchor   int
	pointerSelecting  bool
}

var _ ui.TextInputClient = (*secretDraft)(nil)

func (d *secretDraft) Len() int { return len(d.value) }

func (d *secretDraft) Empty() bool { return len(d.value) == 0 }

func (d *secretDraft) Clear() {
	clear(d.value)
	d.value = nil
	d.selection = ui.TextInputRange{}
	d.selectionReversed = false
	d.selectionAnchor = 0
	d.pointerSelecting = false
}

// CopyBytes returns an owned copy for one explicit save command.
func (d *secretDraft) CopyBytes() []byte { return append([]byte(nil), d.value...) }

// Mask returns display-only glyphs; the returned string contains no credential
// bytes. Long drafts render a window around the caret to keep layout bounded.
func (d *secretDraft) Mask() string {
	start, end := d.displayRange()
	return strings.Repeat("•", end-start)
}

func (d *secretDraft) TextForRange(r ui.TextInputRange) (string, ui.TextInputRange) {
	start, end := d.clampRange(r)
	return strings.Repeat("•", end-start), ui.TextInputRange{Start: start, End: end}
}

func (d *secretDraft) Selection() ui.TextInputSelection {
	start, end := d.clampRange(d.selection)
	return ui.TextInputSelection{Range: ui.TextInputRange{Start: start, End: end}, Reversed: d.selectionReversed}
}

func (*secretDraft) MarkedRange() (ui.TextInputRange, bool) {
	return ui.TextInputRange{}, false
}

func (d *secretDraft) ReplaceText(r *ui.TextInputRange, text string) {
	start, end := d.selection.Start, d.selection.End
	if r != nil {
		start, end = r.Start, r.End
	}
	start, end = d.clampRange(ui.TextInputRange{Start: start, End: end})
	if !visibleTokenText(text) {
		return
	}
	incoming := []byte(text)
	defer clear(incoming)
	if len(d.value)-(end-start)+len(incoming) > secrets.MaxValueBytes {
		return
	}
	next := make([]byte, len(d.value)-(end-start)+len(incoming))
	copy(next, d.value[:start])
	copy(next[start:], incoming)
	copy(next[start+len(incoming):], d.value[end:])
	clear(d.value)
	d.value = next
	cursor := start + len(incoming)
	d.setSelection(cursor, cursor)
}

// Marked text is intentionally not retained. The native field disables IME
// composition; any committed text arrives through ReplaceText.
func (*secretDraft) SetMarkedText(*ui.TextInputRange, string, ui.TextInputRange) {}

func (*secretDraft) UnmarkText() {}

func (d *secretDraft) BoundsForRange(r ui.TextInputRange) (ui.Rect, ui.TextInputRange, bool) {
	windowStart, windowEnd := d.displayRange()
	start, end := d.clampRange(r)
	if start < windowStart || end > windowEnd {
		return ui.Rect{}, ui.TextInputRange{}, false
	}
	layout := d.displayLayout(windowStart, windowEnd)
	first := layout.Caret(start - windowStart)
	last := layout.Caret(end - windowStart)
	left := min(first.X, last.X)
	width := max(float32(1), max(first.X, last.X)-left)
	return ui.Rect{X: secretInputLeft + left, Y: secretInputTop, W: width, H: max(first.H, last.H)}, ui.TextInputRange{Start: start, End: end}, true
}

func (d *secretDraft) IndexForPoint(p ui.Point) (int, bool) {
	start, end := d.displayRange()
	layout := d.displayLayout(start, end)
	index := layout.IndexAt(ui.Point{X: max(float32(0), p.X-secretInputLeft), Y: 0})
	return min(end, start+index), true
}

// HandleInput supplies editing commands that TextInputClient deliberately
// leaves to its application-owned selection and editing policy.
func (d *secretDraft) HandleInput(ev ui.InputEvent) bool {
	switch ev.Kind {
	case ui.InputPointerDown:
		if ev.Button != 0 {
			return false
		}
		index, _ := d.IndexForPoint(ui.Point{X: ev.X, Y: ev.Y})
		if ev.Clicks > 1 {
			d.setSelection(0, len(d.value))
			d.pointerSelecting = false
			return true
		}
		d.selectionAnchor = index
		d.pointerSelecting = true
		d.setSelection(index, index)
		return true
	case ui.InputPointerMove:
		if d.pointerSelecting && ev.Button == 0 {
			index, _ := d.IndexForPoint(ui.Point{X: ev.X, Y: ev.Y})
			d.setSelection(d.selectionAnchor, index)
			return true
		}
	case ui.InputPointerUp:
		if d.pointerSelecting {
			index, _ := d.IndexForPoint(ui.Point{X: ev.X, Y: ev.Y})
			d.setSelection(d.selectionAnchor, index)
			d.pointerSelecting = false
			return true
		}
	case ui.InputSelect:
		d.setSelection(ev.Start, ev.End)
		return true
	case ui.InputCommand:
		switch ev.Text {
		case "selectAll":
			d.setSelection(0, len(d.value))
			return true
		case "copy", "undo", "redo":
			// Copy receives only the mask through TextForRange. Consume it anyway
			// so password-field copy semantics are explicit and predictable.
			return true
		case "cut", "delete":
			d.deleteSelectionOrNext(false)
			return true
		case "paste", "pasteAndMatchStyle":
			if d.readClipboard != nil {
				clipboard := strings.TrimSpace(d.readClipboard())
				d.ReplaceText(nil, clipboard)
			}
			return true
		}
	case ui.InputKeyDown:
		shift := ev.Mods&ui.Shift != 0
		switch {
		case ev.Key == ui.KeyV && ev.Mods&(ui.Ctrl|ui.Cmd) != 0:
			if d.readClipboard != nil {
				clipboard := strings.TrimSpace(d.readClipboard())
				d.ReplaceText(nil, clipboard)
			}
			return true
		case ev.Key == ui.KeyHome || ev.Key == ui.KeyLeft && ev.Mods&ui.Cmd != 0:
			d.moveCaret(0, shift)
			return true
		case ev.Key == ui.KeyEnd || ev.Key == ui.KeyRight && ev.Mods&ui.Cmd != 0:
			d.moveCaret(len(d.value), shift)
			return true
		case ev.Key == ui.KeyBackspace:
			d.deleteSelectionOrNext(true)
			return true
		case ev.Key == ui.KeyDelete:
			d.deleteSelectionOrNext(false)
			return true
		case ev.Key == ui.KeyLeft:
			caret := d.caret()
			if !shift && d.selection.Start != d.selection.End {
				caret = d.selection.Start
			} else if caret > 0 {
				caret--
			}
			d.moveCaret(caret, shift)
			return true
		case ev.Key == ui.KeyRight:
			caret := d.caret()
			if !shift && d.selection.Start != d.selection.End {
				caret = d.selection.End
			} else if caret < len(d.value) {
				caret++
			}
			d.moveCaret(caret, shift)
			return true
		case ev.Key == ui.KeyA && ev.Mods&ui.Cmd != 0:
			d.setSelection(0, len(d.value))
			return true
		case ev.Key == ui.KeyC && ev.Mods&ui.Cmd != 0:
			return true
		case ev.Key == ui.KeyX && ev.Mods&ui.Cmd != 0:
			d.deleteSelectionOrNext(false)
			return true
		}
	}
	return false
}

func (d *secretDraft) deleteSelectionOrNext(backward bool) {
	start, end := d.clampRange(d.selection)
	if start == end {
		if backward {
			start = max(0, start-1)
		} else {
			end = min(len(d.value), end+1)
		}
	}
	r := ui.TextInputRange{Start: start, End: end}
	d.ReplaceText(&r, "")
}

func (d *secretDraft) caret() int {
	if d.selectionReversed {
		return d.selection.Start
	}
	return d.selection.End
}

func (d *secretDraft) moveCaret(caret int, extend bool) {
	if extend {
		anchor := d.selectionAnchor
		if d.selection.Start == d.selection.End {
			anchor = d.caret()
		}
		d.setSelection(anchor, caret)
		return
	}
	d.setSelection(caret, caret)
}

func (d *secretDraft) setSelection(anchor, caret int) {
	anchor = max(0, min(len(d.value), anchor))
	caret = max(0, min(len(d.value), caret))
	d.selectionAnchor = anchor
	d.selectionReversed = caret < anchor
	d.selection = ui.TextInputRange{Start: min(anchor, caret), End: max(anchor, caret)}
}

func (d *secretDraft) displayRange() (int, int) {
	const visible = 52
	length := len(d.value)
	if length <= visible {
		return 0, length
	}
	caret := d.caret()
	start := max(0, caret-visible+2)
	start = min(start, length-visible)
	return start, start + visible
}

func (d *secretDraft) displayLayout(start, end int) *ui.TextLayout {
	mask := strings.Repeat("•", end-start)
	return ui.ShapeText(mask, ui.Font{Size: secretInputFontSize}, 0)
}

func (d *secretDraft) displayCaret() (float32, float32) {
	start, end := d.displayRange()
	layout := d.displayLayout(start, end)
	caret := d.caret()
	position := layout.Caret(caret - start)
	return position.X, position.H
}

func (d *secretDraft) clampRange(r ui.TextInputRange) (int, int) {
	start := max(0, min(len(d.value), r.Start))
	end := max(start, min(len(d.value), r.End))
	return start, end
}

func visibleTokenText(text string) bool {
	if len(text) > secrets.MaxValueBytes {
		return false
	}
	for i := 0; i < len(text); i++ {
		if text[i] < 33 || text[i] > 126 {
			return false
		}
	}
	return true
}

package line

func NewEditor() Editor {
	editor := &lineEditor{
		suggestionDisplay:  newSuggestionDisplay(),
		suggestionManager:  newSuggestionManager(),
		keyCallbackMachine: newKeyCallbackMachine(),
		currentSpans: spans{
			spansStarting: map[uint32]map[uint32]Style{},
			spansEnding:   map[uint32]map[uint32]Style{},
		},
		state:                inputStateFree,
		previousFreeState:    inputStateFree,
		enableSignalHandling: true,
	}
	editor.getTerminalSize()
	editor.suggestionDisplay.setVTSize(editor.numLines, editor.numColumns)
	return editor
}

func MakeXtermColor(color XtermColor) Color {
	return Color{
		IsXterm:  true,
		HasValue: true,
		Xterm8:   color,
	}
}

type Completion struct {
	Text                      string
	TrailingTrivia            string
	DisplayTrivia             string
	Style                     Style
	StartIndex                uint32
	InputOffset               uint32
	StaticOffset              uint32
	InvariantOffset           uint32
	AllowCommitWithoutListing bool

	textView           []rune
	trailingTriviaView []rune
	displayTriviaView  []rune
}

const (
	ModifierShift = 1
	ModifierAlt   = 2
	ModifierCtrl  = 4
)

type key struct {
	modifiers int
	key       uint32
}

type KeybindingCallback func([]key, Editor) bool
type TabCompletionHandler func(editor Editor) []Completion
type PasteHandler func(pastedData string, editor Editor)

type KeyBinding struct {
	keys    []key
	binding KeybindingCallback
}

type MaskedChar struct {
	Position       uint32
	OriginalLength uint32
	MaskedLength   uint32
}

type LineMetrics struct {
	MaskedChars []MaskedChar
	Length      uint32
}

type StringMetrics struct {
	LineMetrics   []LineMetrics
	TotalLength   uint32
	MaxLineLength uint32
}

type SpanMode int

const (
	SpanModeByte SpanMode = iota
	SpanModeRune
)

type Span struct {
	Start uint32
	End   uint32
	Mode  SpanMode
}

type XtermColor int

const (
	XtermColorBlack XtermColor = iota
	XtermColorRed
	XtermColorGreen
	XtermColorYellow
	XtermColorBlue
	XtermColorMagenta
	XtermColorCyan
	XtermColorWhite
	XtermColorUnchanged
	XtermColorDefault
)

type Color struct {
	R uint8
	G uint8
	B uint8

	Xterm8  XtermColor
	IsXterm bool

	HasValue bool
}

type Hyperlink string

type Style struct {
	ForegroundColor Color
	BackgroundColor Color
	Bold            bool
	Italic          bool
	Underline       bool
	Hyperlink       Hyperlink
}

var StyleReset = Style{
	ForegroundColor: Color{
		Xterm8:   XtermColorDefault,
		IsXterm:  true,
		HasValue: true,
	},
	BackgroundColor: Color{
		Xterm8:   XtermColorDefault,
		IsXterm:  true,
		HasValue: true,
	},
	Hyperlink: "",
}

type Winsize struct {
	Row uint16
	Col uint16
}

type Editor interface {
	Initialize()
	GetLine(prompt string) (string, error)

	AddToHistory(line string)
	LoadHistory(path string) error
	SaveHistory(path string) error

	RegisterKeybinding(keys []key, binding KeybindingCallback)
	ActualRenderedStringMetrics(line string) StringMetrics

	SetTabCompletionHandler(handler TabCompletionHandler)
	SetPasteHandler(handler PasteHandler)
	SetInterruptHandler(handler func())
	SetRefreshHandler(handler func(editor Editor))

	Line() string
	LineUpTo(n uint32) string

	SetPrompt(prompt string)

	NumLines() uint32

	InsertString(str string)
	InsertChar(ch rune)

	Stylize(span Span, style Style)
	StripStyles()

	TransformSuggestionOffsets(invariant uint32, static uint32, mode SpanMode) (uint32, uint32)

	TerminalSize() Winsize

	Finish()
	Reset()
	IsEditing() bool
}

type searchOffsetState int

const (
	searchOffsetStateUnbiased searchOffsetState = iota
	searchOffsetStateForwards
	searchOffsetStateBackwards
)

type tabDirection int

const (
	tabDirectionForward tabDirection = iota
	tabDirectionBackward
)

type historyEntry struct {
	entry     string
	timestamp int64
}

type inputState int

const (
	inputStateFree inputState = iota
	inputStateVerbatim
	inputStatePaste
	inputStateGotEscape
	inputStateCSIExpectParameter
	inputStateCSIExpectIntermediate
	inputStateCSIExpectFinal
)

type spans struct {
	spansStarting map[uint32]map[uint32]Style
	spansEnding   map[uint32]map[uint32]Style
}

type keyCallbackMachine interface {
	registerInputCallback([]key, KeybindingCallback)
	keyPressed(key, Editor)
	interrupted(Editor)
	shouldProcessLastPressedKey() bool
}

type suggestionDisplay interface {
	display(suggestionManager)
	redisplay(manager suggestionManager, lines uint32, columns uint32)
	cleanup() bool
	finish()
	setInitialPromptLines(uint32)
	setVTSize(uint32, uint32)
	setOrigin(uint32, uint32)
	originRow() uint32
}

type iterationDecision int

const (
	iterationDecisionContinue iterationDecision = iota
	iterationDecisionBreak
)

type completionMode int

const (
	completionModeDontComplete completionMode = iota
	completionModeCompletePrefix
	completionModeShowSuggestions
	completionModeCycleSuggestions
)

type completionAttemptResult struct {
	newCompletionMode                 completionMode
	newCursorOffset                   uint32
	offsetStartToRemove               uint32
	offsetEndToRemove                 uint32
	staticOffsetFromCursor            uint32
	insert                            []rune
	styleToApply                      Style
	hasStyleToApply                   bool
	avoidCommittingToSingleSuggestion bool
}

type suggestionManager interface {
	setSuggestions([]Completion)
	setCurrentSuggestionInitiationIndex(uint32)
	count() uint32
	displayLength() uint32
	startIndex() uint32
	nextIndex() uint32
	setStartIndex(uint32)

	forEachSuggestion(func(*Completion, uint32) iterationDecision) uint32

	attemptCompletion(mode completionMode, initiationStartIndex uint32) completionAttemptResult

	next()
	previous()

	suggest() *Completion
	currentSuggestion() *Completion
	isCurrentSuggestionComplete() bool

	reset()
}

func (m *LineMetrics) TotalLength(offset int64) uint32 {
	length := m.Length
	for _, maskedChar := range m.MaskedChars {
		if offset < 0 || maskedChar.Position <= uint32(offset) {
			length -= maskedChar.OriginalLength
			length += maskedChar.MaskedLength
		}
	}
	return length
}

func (m *StringMetrics) LinesWithAddition(offset *StringMetrics, columnWidth uint32) uint32 {
	lines := uint32(0)
	for _, line := range m.LineMetrics[:len(m.LineMetrics)-1] {
		lines += (line.TotalLength(-1) + columnWidth) / columnWidth
	}

	last := m.LineMetrics[len(m.LineMetrics)-1].TotalLength(-1)
	last += offset.LineMetrics[0].TotalLength(-1)
	lines += (last + columnWidth) / columnWidth

	for _, line := range offset.LineMetrics[1:] {
		lines += (line.TotalLength(-1) + columnWidth) / columnWidth
	}

	return lines
}

func (m *StringMetrics) OffsetWithAddition(offset *StringMetrics, columnWidth uint32) uint32 {
	if len(offset.LineMetrics) > 1 {
		return offset.LineMetrics[len(offset.LineMetrics)-1].TotalLength(-1) % columnWidth
	}

	last := m.LineMetrics[len(m.LineMetrics)-1].TotalLength(-1)
	last += offset.LineMetrics[0].TotalLength(-1)
	return last % columnWidth
}

func (m *StringMetrics) Reset() {
	m.LineMetrics = nil
	m.TotalLength = 0
	m.MaxLineLength = 0
	m.LineMetrics = append(m.LineMetrics, LineMetrics{})
}

func (s *Style) IsEmpty() bool {
	return !s.ForegroundColor.HasValue &&
		!s.BackgroundColor.HasValue &&
		!s.Bold &&
		!s.Italic &&
		!s.Underline &&
		len(s.Hyperlink) == 0
}

func (s *Style) UnifyWith(other Style) {
	s.BackgroundColor = other.BackgroundColor
	s.ForegroundColor = other.ForegroundColor
	s.Bold = s.Bold || other.Bold
	s.Italic = s.Italic || other.Italic
	s.Underline = s.Underline || other.Underline
	s.Hyperlink = other.Hyperlink
}

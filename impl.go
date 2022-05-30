package line

import (
	"bufio"
	"bytes"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

type maskEntry struct {
	start uint32
	mask  *Mask
}

type lineEditor struct {
	finish                 bool
	searchEditor           *lineEditor
	isSearching            bool
	resetBufferOnSearchEnd bool
	searchOffset           uint32
	searchOffsetState      searchOffsetState
	preSearchCursor        uint32
	preSearchBuffer        []rune
	pasteBuffer            []rune

	buffer         []rune
	pendingChars   []byte
	incompleteData []byte
	inputError     error
	returnedLine   string

	cursor                            uint32
	drawnCursor                       uint32
	drawnEndOfLineOffset              uint32
	inlineSearchCursor                uint32
	charsTouchedInTheMiddle           uint32
	timesTabPressed                   uint32
	numColumns                        uint32
	numLines                          uint32
	previousNumColumns                uint32
	extraForwardLines                 uint32
	cachedPromptMetrics               StringMetrics
	oldPromptMetrics                  StringMetrics
	cachedBufferMetrics               StringMetrics
	promptLinesAtSuggestionInitiation uint32
	cachedPromptValid                 bool

	originRow               uint32
	originColumn            uint32
	hasOriginResetScheduled bool

	suggestionDisplay              suggestionDisplay
	rememberedSuggestionStaticData []rune

	newPrompt string

	suggestionManager suggestionManager

	alwaysRefresh bool

	tabDirection tabDirection

	keyCallbackMachine keyCallbackMachine

	termios                                unix.Termios
	defaultTermios                         unix.Termios
	wasInterrupted                         bool
	previousInterruptWasHandledAsInterrupt bool
	wasResized                             bool

	history         []historyEntry
	historyCursor   uint32
	historyCapacity uint32
	historyDirty    bool

	state             inputState
	previousFreeState inputState

	drawnSpans   spans
	currentSpans spans

	initialized   bool
	refreshNeeded bool

	isEditing                bool
	prohibitInputProcessing  bool
	haveUnprocessedReadEvent bool

	loopChan   chan loopExitCode
	laterChan  chan laterEventCode
	signalChan chan os.Signal

	onInterruptHandled   func()
	tabCompletionHandler TabCompletionHandler
	pasteHandler         PasteHandler
	onRefresh            func(editor Editor)

	enableSignalHandling bool

	currentMasks []maskEntry

	inInterruptHandler              bool
	interruptHandlerRequestedFinish bool

	allowPanics          bool
	enableBracketedPaste bool
}

type loopExitCode int
type laterEventCode int

const (
	loopExitCodeExit loopExitCode = iota
	loopExitCodeRetry
)

const (
	laterEventCodeHandleResizeEventFalse laterEventCode = iota
	laterEventCodeHandleResizeEventTrue
	laterEventCodeTryUpdateOnce
)

func (l *lineEditor) getTerminalSize() {
	winsize, _ := unix.IoctlGetWinsize(unix.Stdout, unix.TIOCGWINSZ)
	if winsize.Col == 0 || winsize.Row == 0 {
		fd, err := unix.Open("/dev/tty", unix.O_RDONLY, 0)
		if err == nil {
			winsize, _ = unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
			_ = unix.Close(fd)
		}
	}

	l.numColumns = uint32(winsize.Col)
	l.numLines = uint32(winsize.Row)
}

func editorInternal(fn func(editor *lineEditor)) func([]key, Editor) bool {
	return func(_ []key, editor Editor) bool {
		fn(editor.(*lineEditor))
		return false
	}
}

func (l *lineEditor) setDefaultKeybinds() {
	l.RegisterKeybinding([]key{{key: ctrl('N')}}, editorInternal(searchForwards))
	l.RegisterKeybinding([]key{{key: ctrl('P')}}, editorInternal(searchBackwards))
	l.RegisterKeybinding([]key{{key: ctrl('A')}}, editorInternal(goHome))
	l.RegisterKeybinding([]key{{key: ctrl('B')}}, editorInternal(cursorLeftCharacter))
	l.RegisterKeybinding([]key{{key: ctrl('D')}}, editorInternal(eraseCharacterForwards))
	l.RegisterKeybinding([]key{{key: ctrl('E')}}, editorInternal(goEnd))
	l.RegisterKeybinding([]key{{key: ctrl('F')}}, editorInternal(cursorRightCharacter))
	// ^H: ctrl('H') = \b
	l.RegisterKeybinding([]key{{key: ctrl('H')}}, editorInternal(eraseCharacterBackwards))
	// DEL, Some terminals send this instead of ^H
	l.RegisterKeybinding([]key{{key: 127}}, editorInternal(eraseCharacterBackwards))
	l.RegisterKeybinding([]key{{key: ctrl('K')}}, editorInternal(eraseToEnd))
	l.RegisterKeybinding([]key{{key: ctrl('L')}}, editorInternal(clearScreen))
	l.RegisterKeybinding([]key{{key: ctrl('R')}}, editorInternal(enterSearch))
	l.RegisterKeybinding([]key{{key: ctrl('T')}}, editorInternal(transposeCharacters))
	l.RegisterKeybinding([]key{{key: '\n'}}, editorInternal(finish))

	l.RegisterKeybinding([]key{{key: ctrl('X')}, {key: ctrl('E')}}, editorInternal(editInExternalEditor))

	// ^[.: alt-.: insert last arg of previous command (similar to `!$` in shells)
	l.RegisterKeybinding([]key{{key: '.', modifiers: ModifierAlt}}, editorInternal(insertLastWords))

	l.RegisterKeybinding([]key{{key: 'b', modifiers: ModifierAlt}}, editorInternal(cursorLeftCharacter))
	l.RegisterKeybinding([]key{{key: 'f', modifiers: ModifierAlt}}, editorInternal(cursorRightCharacter))
	// ^[^H: alt-backspace: backward delete word
	l.RegisterKeybinding([]key{{key: '\b', modifiers: ModifierAlt}}, editorInternal(eraseAlnumWordBackwards))
	l.RegisterKeybinding([]key{{key: 'd', modifiers: ModifierAlt}}, editorInternal(eraseAlnumWordForwards))
	l.RegisterKeybinding([]key{{key: 'c', modifiers: ModifierAlt}}, editorInternal(capitalizeWord))
	l.RegisterKeybinding([]key{{key: 'l', modifiers: ModifierAlt}}, editorInternal(lowercaseWord))
	l.RegisterKeybinding([]key{{key: 'u', modifiers: ModifierAlt}}, editorInternal(uppercaseWord))
	l.RegisterKeybinding([]key{{key: 't', modifiers: ModifierAlt}}, editorInternal(transposeWords))

	l.RegisterKeybinding([]key{{key: uint32(l.termios.Cc[syscall.VWERASE])}}, editorInternal(eraseWordBackwards))
	l.RegisterKeybinding([]key{{key: uint32(l.termios.Cc[syscall.VKILL])}}, editorInternal(killLine))
	l.RegisterKeybinding([]key{{key: uint32(l.termios.Cc[syscall.VERASE])}}, editorInternal(eraseCharacterBackwards))
}

func (l *lineEditor) handleInterruptEvent() {
	l.wasInterrupted = false
	l.previousInterruptWasHandledAsInterrupt = false

	l.keyCallbackMachine.interrupted(l)
	if !l.keyCallbackMachine.shouldProcessLastPressedKey() {
		return
	}

	l.previousInterruptWasHandledAsInterrupt = true

	_, _ = os.Stderr.Write([]byte("^C"))

	if l.onInterruptHandled != nil {
		l.inInterruptHandler = true
		l.interruptHandlerRequestedFinish = false

		l.onInterruptHandled()

		l.inInterruptHandler = false
	}

	if l.interruptHandlerRequestedFinish {
		return
	}

	l.buffer = make([]rune, 0)
	l.charsTouchedInTheMiddle = 0
	l.cursor = 0

	l.Finish()
}

func (l *lineEditor) cursorLine() uint32 {
	cursor := l.drawnCursor
	if cursor > l.cursor {
		cursor = l.cursor
	}
	metrics := l.actualRenderedStringMetricsImpl(string(l.buffer[:cursor]), l.currentMasks)
	return l.CurrentPromptMetrics().LinesWithAddition(&metrics, l.numColumns)
}

func (l *lineEditor) offsetInLine() uint32 {
	cursor := l.drawnCursor
	if cursor > l.cursor {
		cursor = l.cursor
	}
	metrics := l.actualRenderedStringMetricsImpl(string(l.buffer[:cursor]), l.currentMasks)
	return l.CurrentPromptMetrics().OffsetWithAddition(&metrics, l.numColumns)
}

func (l *lineEditor) ensureFreeLinesFromOrigin(count uint32) {
	if count > l.numLines {
		// It's hopeless...
		if l.allowPanics {
			panic("ensureFreeLinesFromOrigin: count > l.numLines")
		} else {
			count = l.numLines
		}
	}

	if l.originRow+count <= l.numLines {
		return
	}

	diff := l.originRow + count - l.numLines - 1
	os.Stderr.Write([]byte(fmt.Sprintf("\x1b[%dS", diff)))
	l.originRow -= diff
	l.refreshNeeded = false
	l.charsTouchedInTheMiddle = 0
}

func (l *lineEditor) repositionCursor(stream io.Writer, toEnd bool) {
	cursor := l.cursor
	savedCursor := cursor
	if toEnd {
		cursor = uint32(len(l.buffer))
	}

	l.cursor = cursor
	l.drawnCursor = cursor

	line := l.cursorLine() - 1
	column := l.offsetInLine()

	l.ensureFreeLinesFromOrigin(line)

	vtMoveAbsolute(line+l.originRow, column+l.originColumn, stream)

	l.cursor = savedCursor
}

func (l *lineEditor) restore() {
	_ = setTermios(&l.defaultTermios)
	if l.enableBracketedPaste {
		os.Stderr.Write([]byte("\x1b[?2004l"))
	}
	l.initialized = false
}

func (l *lineEditor) setOrigin(quitOnError bool) bool {
	row, col, err := l.vtDSR()
	if err == nil {
		l.setOriginValue(row, col)
		return true
	}
	if quitOnError && err != nil {
		l.inputError = err
		l.Finish()
	}
	return false
}

func (l *lineEditor) setOriginValue(row uint32, col uint32) {
	l.originRow = row
	l.originColumn = col
	l.suggestionDisplay.setOrigin(row, col)
}

func (l *lineEditor) vtDSR() (uint32, uint32, error) {
	buf := make([]byte, 16)
	moreJunkToRead := false
	readFds := unix.FdSet{}
	readFds.Set(unix.Stdin)
	timeout := unix.Timeval{}

	for {
		moreJunkToRead = false
		_, _ = unix.Select(1, &readFds, nil, nil, &timeout)
		if readFds.IsSet(unix.Stdin) {
			nread, err := unix.Read(unix.Stdin, buf)
			if err != nil && err != unix.EINTR {
				l.inputError = err
				l.Finish()
				break
			}
			if nread == 0 {
				break
			}

			l.incompleteData = append(l.incompleteData, buf[:nread]...)
			moreJunkToRead = true
		}
		if !moreJunkToRead {
			break
		}
	}

	if l.inputError != nil {
		return 0, 0, l.inputError
	}

	_, _ = os.Stderr.WriteString("\x1b[6n")

	const (
		Free = iota
		SawEsc
		SawBracket
		InFirstCoordinate
		SawSemicolon
		InSecondCoordinate
		SawR
	)

	state := Free
	hasError := false
	coordinateBuffer := bytes.NewBuffer(nil)
	row := uint32(1)
	col := uint32(1)

	for {
		if state == SawR {
			break
		}
		c := make([]byte, 1)
		nread, err := os.Stdin.Read(c)
		if err != nil {
			continue
		}

		if nread == 0 {
			break
		}

		switch state {
		case Free:
			if c[0] == '\x1b' {
				state = SawEsc
				continue
			}
			l.incompleteData = append(l.incompleteData, c...)
			continue
		case SawEsc:
			if c[0] == '[' {
				state = SawBracket
				continue
			}
			l.incompleteData = append(l.incompleteData, c...)
			continue
		case SawBracket:
			if c[0] >= '0' && c[0] <= '9' {
				state = InFirstCoordinate
				coordinateBuffer.Write(c)
				continue
			}
			l.incompleteData = append(l.incompleteData, c...)
			continue
		case InFirstCoordinate:
			if c[0] >= '0' && c[0] <= '9' {
				coordinateBuffer.Write(c)
				continue
			}
			if c[0] == ';' {
				parsedRow, err := strconv.Atoi(string(coordinateBuffer.Bytes()))
				if err != nil {
					hasError = true
				}
				row = uint32(parsedRow)
				coordinateBuffer.Reset()
				state = SawSemicolon
				continue
			}
			l.incompleteData = append(l.incompleteData, c...)
			continue
		case SawSemicolon:
			if c[0] >= '0' && c[0] <= '9' {
				state = InSecondCoordinate
				coordinateBuffer.Write(c)
				continue
			}
			l.incompleteData = append(l.incompleteData, c...)
			continue
		case InSecondCoordinate:
			if c[0] >= '0' && c[0] <= '9' {
				coordinateBuffer.Write(c)
				continue
			}
			if c[0] == 'R' {
				parsedCol, err := strconv.Atoi(string(coordinateBuffer.Bytes()))
				if err != nil {
					hasError = true
				}
				col = uint32(parsedCol)
				coordinateBuffer.Reset()
				state = SawR
				continue
			}
			l.incompleteData = append(l.incompleteData, c...)
			continue
		case SawR:
			break
		default:
			panic("unreachable")
		}
	}

	// FIXME: Return an actual error if hasError is true
	if hasError {
		println("Some error occurred while parsing VT100 coordinates")
	}
	return row, col, nil
}

func (l *lineEditor) interrupted() {
	if l.isSearching {
		l.searchEditor.interrupted()
		return
	}

	if !l.isEditing {
		return
	}

	l.wasInterrupted = true
	l.handleInterruptEvent()
	if l.interruptHandlerRequestedFinish {
		l.interruptHandlerRequestedFinish = false
		l.finish = false
		l.reallyQuitEventLoop()
		return
	}

	if !l.finish || !l.previousInterruptWasHandledAsInterrupt {
		return
	}

	l.finish = false

	l.repositionCursor(os.Stderr, true)
	if l.suggestionDisplay.cleanup() {
		l.repositionCursor(os.Stderr, true)
	}
	_, _ = os.Stderr.Write([]byte("\n"))

	l.buffer = make([]rune, 0)
	l.charsTouchedInTheMiddle = 0
	l.isEditing = false
	l.restore()
	l.loopChan <- loopExitCodeRetry
}

func (l *lineEditor) resized() {
	l.wasResized = true
	l.previousNumColumns = l.numColumns
	l.getTerminalSize()

	if !l.hasOriginResetScheduled {
		// Reset the origin, but make sure it doesn't blow up if we can't read it
		if l.setOrigin(false) {
			l.handleResizeEvent(false)
		} else {

			l.laterChan <- laterEventCodeHandleResizeEventFalse

			l.hasOriginResetScheduled = true
		}
	}
}

func (l *lineEditor) handleResizeEvent(resetOrigin bool) {
	l.hasOriginResetScheduled = false
	if resetOrigin && !l.setOrigin(false) {
		l.hasOriginResetScheduled = true

		l.laterChan <- laterEventCodeHandleResizeEventTrue

		return
	}

	l.setOriginValue(l.originRow, 1)
	l.repositionCursor(os.Stderr, true)
	l.suggestionDisplay.redisplay(l.suggestionManager, l.numLines, l.numColumns)
	l.originRow = l.suggestionDisplay.originRow()
	l.repositionCursor(os.Stderr, true)

	if l.isSearching {
		l.searchEditor.resized()
	}
}

func (l *lineEditor) Initialize() {
	if l.initialized {
		return
	}

	t, _ := getTermios()
	l.defaultTermios = *t

	l.getTerminalSize()

	t.Lflag &^= unix.ECHO | unix.ICANON
	_ = setTermios(t)

	l.termios = *t

	l.setDefaultKeybinds()
	l.initialized = true
}

func max(a, b uint32) uint32 {
	if a > b {
		return a
	}
	return b
}

func min(a, b uint32) uint32 {
	if a < b {
		return a
	}
	return b
}

func (l *lineEditor) CurrentPromptMetrics() *StringMetrics {
	if l.cachedPromptValid {
		return &l.cachedPromptMetrics
	}
	return &l.oldPromptMetrics
}

func vtMoveRelative(row, col int64, w io.Writer) {
	xOp := 'A'
	yOp := 'D'

	if row > 0 {
		xOp = 'B'
	} else {
		row = -row
	}

	if col > 0 {
		yOp = 'C'
	} else {
		col = -col
	}

	if row > 0 {
		_, _ = w.Write([]byte(fmt.Sprintf("\x1b[%d%c", row, xOp)))
	}
	if col > 0 {
		_, _ = w.Write([]byte(fmt.Sprintf("\x1b[%d%c", col, yOp)))
	}
}

func (l *lineEditor) GetLine(prompt string) (string, error) {
	l.Initialize()
	l.isEditing = true
	oldCols := l.numColumns
	oldLines := l.numLines
	l.getTerminalSize()

	if l.enableBracketedPaste {
		os.Stderr.Write([]byte("\x1b[?2004h"))
	}

	if l.numColumns != oldCols || l.numLines != oldLines {
		l.refreshNeeded = true
	}

	l.SetPrompt(prompt)
	l.Reset()
	l.StripStyles()

	promptLines := max(uint32(len(l.CurrentPromptMetrics().LineMetrics)), 1) - 1
	for i := uint32(0); i < promptLines; i++ {
		_, _ = os.Stderr.Write([]byte("\n"))
	}
	vtMoveRelative(-int64(promptLines), 0, os.Stderr)
	l.setOrigin(true)

	l.historyCursor = uint32(len(l.history))

	l.refreshDisplay()

	l.loopChan = make(chan loopExitCode, 1)
	defer close(l.loopChan)

	l.laterChan = make(chan laterEventCode, 1)
	defer close(l.laterChan)

	go func() {
		defer func() {
			recover()
		}()
		for {
			fds := unix.FdSet{}
			fds.Set(unix.Stdin)

			n, err := unix.Select(1, &fds, nil, nil, nil)
			if err != nil {
				if err == unix.EINTR {
					continue
				}
				l.inputError = err
				l.loopChan <- loopExitCodeExit
				break
			}
			if n == 0 {
				continue
			}
			if !fds.IsSet(unix.Stdin) {
				continue
			}

			l.laterChan <- laterEventCodeTryUpdateOnce
		}
	}()

	if len(l.incompleteData) != 0 {
		l.laterChan <- laterEventCodeTryUpdateOnce
	}

	l.signalChan = make(chan os.Signal, 1)
	defer func() {
		if l.enableSignalHandling {
			signal.Stop(l.signalChan)
		}
		close(l.signalChan)
	}()
	if l.enableSignalHandling {
		signal.Notify(l.signalChan, unix.SIGWINCH, unix.SIGINT)
	}

	for {
		select {
		case sig := <-l.signalChan:
			if sig == unix.SIGWINCH {
				l.resized()
			} else if sig == unix.SIGINT {
				l.interrupted()
			}
		case code := <-l.laterChan:
			if l.finish {
				continue
			}
			if code == laterEventCodeHandleResizeEventFalse {
				l.handleResizeEvent(false)
				continue
			}
			if code == laterEventCodeHandleResizeEventTrue {
				l.handleResizeEvent(true)
				continue
			}
			if code == laterEventCodeTryUpdateOnce {
				l.tryUpdateOnce()
				continue
			}
		case code := <-l.loopChan:
			if code == loopExitCodeExit {
				l.finish = false
				return l.returnedLine, l.inputError
			}
			if code == loopExitCodeRetry {
				return l.GetLine(prompt)
			}
		}
	}
}

func (l *lineEditor) AddToHistory(line string) {
	l.history = append(l.history, historyEntry{
		entry:     line,
		timestamp: time.Now().Unix(),
	})
}

func (l *lineEditor) LoadHistory(path string) error {
	// FIXME: Support the LibLine history format.
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		l.AddToHistory(scanner.Text())
	}

	return scanner.Err()
}

func (l *lineEditor) SaveHistory(path string) error {
	// FIXME: Support the LibLine history format.
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	for _, entry := range l.history {
		_, err := f.WriteString(entry.entry + "\n")
		if err != nil {
			return err
		}
	}

	return nil
}

func (l *lineEditor) RegisterKeybinding(keys []key, binding KeybindingCallback) {
	l.keyCallbackMachine.registerInputCallback(keys, binding)
}

type VTState int

const (
	VTStateFree VTState = iota
	VTStateEscape
	VTStateBracket
	VTStateBracketArgsSemi
	VTStateTitle
)

func (l *lineEditor) ActualRenderedStringMetrics(line string) StringMetrics {
	return l.actualRenderedStringMetricsImpl(line, []maskEntry{})
}

func (l *lineEditor) actualRenderedStringMetricsImpl(line string, masks []maskEntry) StringMetrics {
	metrics := StringMetrics{}
	currentLine := LineMetrics{}
	state := VTStateFree
	runes := []rune(line)
	byteOffset := 0
	var mask *Mask
	maskIt := 0

	for i := 0; i < len(runes); i++ {
		c := runes[i]
		if maskIt < len(masks) && masks[maskIt].start <= uint32(i) {
			mask = masks[maskIt].mask
		}

		if mask != nil && mask.mode == MaskModeReplaceEntireSelection {
			maskIt++
			actualEndOffset := uint32(len(runes))
			if maskIt < len(masks) {
				actualEndOffset = masks[maskIt].start
			}
			endOffset := min(actualEndOffset, uint32(len(runes)))
			j := 0
			for it := 0; it != len(mask.replacementView); it++ {
				itCopy := it
				itCopy++
				nextC := rune(0)
				if itCopy < len(mask.replacementView) {
					nextC = mask.replacementView[itCopy]
				}
				state = l.actualRenderedStringLengthStep(&metrics, j, &currentLine, mask.replacementView[it], nextC, state, nil)
				j++
				if uint32(j) <= actualEndOffset-uint32(i) && j+i >= len(runes) {
					break
				}
			}
			currentLine.MaskedChars = append(currentLine.MaskedChars, MaskedChar{
				Position:       uint32(i),
				OriginalLength: endOffset - uint32(i),
				MaskedLength:   uint32(j),
			})
			i = int(endOffset - 1)

			if maskIt == len(masks) {
				mask = nil
			} else {
				mask = masks[maskIt].mask
			}
			continue
		}

		nextC := rune(0)
		if i+1 < len(runes) {
			nextC = runes[i+1]
		}
		state = l.actualRenderedStringLengthStep(&metrics, byteOffset, &currentLine, c, nextC, state, mask)
		byteOffset += utf8.RuneLen(c)
		if maskIt < len(masks) && masks[maskIt].start == uint32(i) {
			maskItPeek := maskIt + 1
			if maskItPeek < len(masks) && masks[maskItPeek].start > uint32(i) {
				maskIt = maskItPeek
			}
		}
	}

	metrics.LineMetrics = append(metrics.LineMetrics, currentLine)
	for _, lineMetric := range metrics.LineMetrics {
		metrics.MaxLineLength = max(lineMetric.TotalLength(), metrics.MaxLineLength)
	}

	return metrics
}

func (l *lineEditor) SetTabCompletionHandler(handler TabCompletionHandler) {
	l.tabCompletionHandler = handler
}

func (l *lineEditor) SetPasteHandler(handler PasteHandler) {
	l.pasteHandler = handler
}

func (l *lineEditor) SetInterruptHandler(handler func()) {
	l.onInterruptHandled = handler
}

func (l *lineEditor) SetRefreshHandler(handler func(editor Editor)) {
	l.onRefresh = handler
}

func (l *lineEditor) SetLine(line string) {
	l.inlineSearchCursor = min(l.cursor, uint32(len(line)))
	l.cursor = l.inlineSearchCursor
	l.charsTouchedInTheMiddle = uint32(len(l.buffer))
	l.refreshNeeded = true
	l.buffer = []rune(line)
	l.cachedBufferMetrics = l.ActualRenderedStringMetrics(line)
}

func (l *lineEditor) Line() string {
	return l.LineUpTo(uint32(len(l.buffer)))
}

func (l *lineEditor) LineUpTo(n uint32) string {
	return string(l.buffer[:n])
}

func (l *lineEditor) SetPrompt(prompt string) {
	if l.cachedPromptValid {
		l.oldPromptMetrics = l.cachedPromptMetrics
	}
	l.cachedPromptValid = false
	l.cachedPromptMetrics = l.ActualRenderedStringMetrics(prompt)
	l.newPrompt = prompt
}

func (l *lineEditor) InsertString(str string) {
	runes := []rune(str)
	for _, r := range runes {
		l.InsertChar(r)
	}
}

func (l *lineEditor) InsertChar(ch rune) {
	s := string(ch)
	l.pendingChars = append(l.pendingChars, s...)

	if l.cursor == uint32(len(l.buffer)) {
		l.buffer = append(l.buffer, ch)
		l.cursor = uint32(len(l.buffer))
		l.inlineSearchCursor = l.cursor
		return
	}

	b := append([]rune{}, l.buffer[:l.cursor]...)
	b = append(b, ch)
	l.buffer = append(b, l.buffer[l.cursor:]...)
	l.charsTouchedInTheMiddle++
	l.cursor++
	l.inlineSearchCursor = l.cursor
}

type sortableMaskEntrySlice struct {
	entries []maskEntry
}

func (s *sortableMaskEntrySlice) Len() int {
	return len(s.entries)
}

func (s *sortableMaskEntrySlice) Less(i, j int) bool {
	return s.entries[i].start < s.entries[j].start
}

func (s *sortableMaskEntrySlice) Swap(i, j int) {
	s.entries[i], s.entries[j] = s.entries[j], s.entries[i]
}

func (l *lineEditor) Stylize(span Span, style Style) {
	if style.IsEmpty() {
		return
	}

	start := span.Start
	end := span.End

	if start == end {
		return
	}

	if span.Mode == SpanModeByte {
		start, end = l.byteOffsetRangeToCodePointOffsetRange(start, end, 0, false)
	}

	mask := style.Mask

	if mask != nil {
		i := len(l.currentMasks)
		for j := len(l.currentMasks); j > 0; j-- {
			e := l.currentMasks[j-1]
			if e.start < start {
				break
			}
			i = j - 1
		}
		var lastEncounteredEntry *Mask
		if i != len(l.currentMasks) {
			// Delete all overlapping old masks
			for {
				nextI := len(l.currentMasks)
				for j, e := range l.currentMasks {
					if e.start > start {
						break
					}
					nextI = j
				}
				if nextI == len(l.currentMasks) {
					break
				}
				entry := &l.currentMasks[nextI]
				if entry.mask != nil {
					lastEncounteredEntry = entry.mask
				}
				l.currentMasks = append(l.currentMasks[:nextI], l.currentMasks[nextI+1:]...)
			}
		}
		l.currentMasks = append(l.currentMasks, []maskEntry{{start, mask}, {end, nil}}...)
		if lastEncounteredEntry != nil {
			l.currentMasks = append(l.currentMasks, maskEntry{end + 1, lastEncounteredEntry})
		}

		sortable := &sortableMaskEntrySlice{l.currentMasks}
		sort.Sort(sortable)
		l.currentMasks = sortable.entries
		style.Mask = nil
	}

	spansStarting := l.currentSpans.spansStarting
	spansEnding := l.currentSpans.spansEnding

	if spansStarting == nil {
		spansStarting = map[uint32]map[uint32]Style{}
	}
	if spansEnding == nil {
		spansEnding = map[uint32]map[uint32]Style{}
	}

	startingMap, ok := spansStarting[start]
	if !ok {
		startingMap = map[uint32]Style{}
		spansStarting[start] = startingMap
	}
	if _, ok = startingMap[end]; !ok {
		l.refreshNeeded = true
	}
	startingMap[end] = style

	endingMap, ok := spansEnding[end]
	if !ok {
		endingMap = map[uint32]Style{}
		spansEnding[end] = endingMap
	}
	if _, ok = endingMap[start]; !ok {
		l.refreshNeeded = true
	}
	endingMap[start] = style

	l.currentSpans.spansStarting = spansStarting
	l.currentSpans.spansEnding = spansEnding
}

func (l *lineEditor) StripStyles() {
	l.currentSpans = spans{}
	l.currentMasks = l.currentMasks[:0]
	l.refreshNeeded = true
}

func (l *lineEditor) TransformSuggestionOffsets(invariant uint32, static uint32, mode SpanMode) (uint32, uint32) {
	internalStaticOffset := static
	internalInvariantOffset := invariant
	if mode == SpanModeByte {
		start, end := l.byteOffsetRangeToCodePointOffsetRange(static, invariant+static, l.cursor-1, true)
		internalStaticOffset = start
		internalInvariantOffset = end - start
	}
	return internalStaticOffset, internalInvariantOffset
}

func (l *lineEditor) TerminalSize() Winsize {
	return Winsize{
		Row: uint16(l.numLines),
		Col: uint16(l.numColumns),
	}
}

func (l *lineEditor) Finish() {
	if l.inInterruptHandler {
		l.interruptHandlerRequestedFinish = true
	}
	l.finish = true
}

func (l *lineEditor) IsEditing() bool {
	return l.isEditing
}

func (l *lineEditor) Reset() {
	l.cachedBufferMetrics.Reset()
	l.cachedPromptValid = false
	l.cursor = 0
	l.drawnCursor = 0
	l.inlineSearchCursor = 0
	l.searchOffset = 0
	l.searchOffsetState = searchOffsetStateUnbiased
	l.oldPromptMetrics = l.cachedPromptMetrics
	l.setOriginValue(0, 0)
	l.promptLinesAtSuggestionInitiation = 0
	l.refreshNeeded = true
	l.inputError = nil
	l.returnedLine = ""
	l.charsTouchedInTheMiddle = 0
	l.drawnEndOfLineOffset = 0
	l.drawnSpans = spans{}
	l.pasteBuffer = []rune{}
}

func (l *lineEditor) recalculateOrigin() {
	// Changing the columns can affect our origin if
	// the new size is smaller than our prompt, which would
	// cause said prompt to take up more space, so we should
	// compensate for that.
	if l.cachedPromptMetrics.MaxLineLength >= l.numColumns {
		l.originRow += (l.cachedPromptMetrics.MaxLineLength+1)/l.numColumns - 1
	}

	// We also need to recalculate our cursor position,
	// but that will be calculated and applied at the next
	// refresh cycle.
}

func (l *lineEditor) cleanup() {
	currentBufferMetrics := l.actualRenderedStringMetricsImpl(string(l.buffer), l.currentMasks)
	newLines := l.CurrentPromptMetrics().LinesWithAddition(&currentBufferMetrics, l.numColumns)
	shownLines := l.NumLines()
	if newLines < shownLines {
		l.extraForwardLines = max(shownLines-newLines, l.extraForwardLines)
	}

	l.repositionCursor(os.Stderr, true)
	currentLine := l.NumLines()
	vtClearLines(currentLine, l.extraForwardLines, os.Stderr)
	l.extraForwardLines = 0
	l.repositionCursor(os.Stderr, false)
}

func (l *lineEditor) NumLines() uint32 {
	return l.CurrentPromptMetrics().LinesWithAddition(&l.cachedBufferMetrics, l.numColumns)
}

func (l *lineEditor) refreshDisplay() {
	outputBuffer := bytes.NewBuffer(nil)
	defer func() {
		_, _ = os.Stderr.Write(outputBuffer.Bytes())
	}()

	hasCleanedUp := false
	if l.wasResized {
		if l.previousNumColumns != l.numColumns {
			l.cachedPromptValid = false
			l.refreshNeeded = true
			t := l.numColumns
			l.numColumns = l.previousNumColumns
			l.previousNumColumns = t
			l.recalculateOrigin()
			l.cleanup()
			t = l.numColumns
			l.numColumns = l.previousNumColumns
			l.previousNumColumns = t
			hasCleanedUp = true
		}
		l.wasResized = false
	}

	// We might be at the last line, and have more than one line;
	// Refreshing the display will cause the terminal to scroll,
	// so note that fact and bring origin up, making sure to
	// reserve the space for however many lines we move it up.
	currentNumLines := l.NumLines()
	if l.originRow+currentNumLines > l.numLines {
		if currentNumLines > l.numLines {
			for i := uint32(0); i < l.numLines; i++ {
				_, _ = outputBuffer.WriteString("\n")
			}
			l.originRow = 0
		} else {
			oldOriginRow := l.originRow
			l.originRow = l.numLines - currentNumLines + 1
			for i := uint32(0); i < oldOriginRow-l.originRow; i++ {
				_, _ = outputBuffer.WriteString("\n")
			}
		}
	}

	// Do not call hook on pure cursor movement.
	if l.cachedPromptValid && !l.refreshNeeded && len(l.pendingChars) == 0 {
		// Probably just moving around
		l.repositionCursor(outputBuffer, false)
		l.cachedBufferMetrics = l.actualRenderedStringMetricsImpl(string(l.buffer), l.currentMasks)
		l.drawnEndOfLineOffset = uint32(len(l.buffer))
		return
	}

	if l.onRefresh != nil {
		l.onRefresh(l)
	}

	if l.cachedPromptValid {
		if !l.refreshNeeded && l.cursor == uint32(len(l.buffer)) {
			// Just write the characters out and continue,
			// no need to refresh the entire line
			outputBuffer.Write(l.pendingChars)
			l.pendingChars = []byte{}
			l.drawnCursor = l.cursor
			l.drawnEndOfLineOffset = uint32(len(l.buffer))
			l.cachedBufferMetrics = l.actualRenderedStringMetricsImpl(string(l.buffer), l.currentMasks)
			l.drawnSpans = l.currentSpans
			return
		}
	}

	applyStyles := func(i uint32) {
		ends := l.currentSpans.spansEnding[i]
		starts := l.currentSpans.spansStarting[i]

		if len(ends) > 0 {
			style := Style{}
			for _, applicableStyle := range ends {
				style.UnifyWith(applicableStyle)
			}

			vtApplyStyle(style, outputBuffer, false)
			style = l.findApplicableStyle(i)
			vtApplyStyle(style, outputBuffer, true)
		}
		if len(starts) > 0 {
			style := Style{}
			for _, applicableStyle := range starts {
				style.UnifyWith(applicableStyle)
			}

			vtApplyStyle(style, outputBuffer, true)
		}
	}

	printCharacterAt := func(i uint32) {
		var c interface{}
		it := len(l.currentMasks)
		for j, e := range l.currentMasks {
			if e.start > i {
				break
			}
			it = j
		}
		if it < len(l.currentMasks) && l.currentMasks[it].mask != nil {
			offset := i - l.currentMasks[it].start
			mask := l.currentMasks[it].mask
			if mask.mode == MaskModeReplaceEntireSelection {
				v := mask.replacementView
				if offset >= uint32(len(v)) {
					return
				}
				c = v[offset]
				it++
				nextOffset := l.drawnEndOfLineOffset
				if it < len(l.currentMasks) {
					nextOffset = l.currentMasks[it].start
				}
				if i+1 == nextOffset {
					c = v[offset : len(v)-1]
				}
			} else {
				c = mask.replacementView
			}
		} else {
			c = l.buffer[i]
		}
		printSingleCharacter := func(c rune) {
			shouldPrintMasked := c == 0x7f || c < 0x20 && c != '\n'
			shouldPrintCaret := c < 64 && shouldPrintMasked
			s := ""
			if shouldPrintCaret {
				s = "^" + string(c+64)
			} else if shouldPrintMasked {
				s = "\\x" + strconv.FormatInt(int64(c), 16)
			} else {
				s = string(c)
			}

			if shouldPrintMasked {
				outputBuffer.WriteString("\x1b[7m")
			}
			outputBuffer.WriteString(s)
			if shouldPrintMasked {
				outputBuffer.WriteString("\x1b[27m")
			}
		}

		switch c.(type) {
		case rune:
			printSingleCharacter(c.(rune))
		case []rune:
			for _, r := range c.([]rune) {
				printSingleCharacter(r)
			}
		}
	}

	if !l.alwaysRefresh && l.cachedPromptValid && l.charsTouchedInTheMiddle == 0 && l.drawnSpans.containsUpToOffset(&l.currentSpans, l.drawnCursor) {
		initialStyle := l.findApplicableStyle(l.drawnEndOfLineOffset)
		vtApplyStyle(initialStyle, outputBuffer, true)

		for i := l.drawnEndOfLineOffset; i < uint32(len(l.buffer)); i++ {
			applyStyles(i)
			printCharacterAt(i)
		}

		vtApplyStyle(StyleReset, outputBuffer, true)
		l.pendingChars = []byte{}
		l.refreshNeeded = false
		l.cachedBufferMetrics = l.actualRenderedStringMetricsImpl(string(l.buffer), l.currentMasks)
		l.charsTouchedInTheMiddle = 0
		l.drawnCursor = l.cursor
		l.drawnEndOfLineOffset = uint32(len(l.buffer))

		// No need to reposition the cursor, it's already in the right place
		return
	}

	// Ouch, reflow entire line
	if !hasCleanedUp {
		l.cleanup()
	}

	vtMoveAbsolute(l.originRow, l.originColumn, outputBuffer)
	outputBuffer.WriteString(l.newPrompt)

	vtClearToEndOfLine(outputBuffer)

	for i := uint32(0); i < uint32(len(l.buffer)); i++ {
		applyStyles(i)
		printCharacterAt(i)
	}

	vtApplyStyle(StyleReset, outputBuffer, true) // Don't bleed to EOL

	l.pendingChars = []byte{}
	l.refreshNeeded = false
	l.cachedBufferMetrics = l.actualRenderedStringMetricsImpl(string(l.buffer), l.currentMasks)
	l.charsTouchedInTheMiddle = 0
	l.drawnSpans = l.currentSpans
	l.drawnEndOfLineOffset = uint32(len(l.buffer))
	l.cachedPromptValid = true

	l.repositionCursor(outputBuffer, false)
}

func (l *lineEditor) findApplicableStyle(offset uint32) Style {
	style := StyleReset
	unify := func(key uint32, value map[uint32]Style) {
		if key >= offset {
			return
		}

		for key, applicableStyle := range value {
			if key <= offset {
				return
			}

			style.UnifyWith(applicableStyle)
		}
	}

	for k, v := range l.currentSpans.spansStarting {
		unify(k, v)
	}

	return style
}

func (l *lineEditor) actualRenderedStringLengthStep(metrics *StringMetrics, index int, currentLine *LineMetrics, c, nextC rune, state VTState, mask *Mask) VTState {
	switch state {
	case VTStateFree:
		if c == '\x1b' {
			return VTStateEscape
		}
		if c == '\r' {
			currentLine.MaskedChars = []MaskedChar{}
			currentLine.Length = 0
			if len(metrics.LineMetrics) != 0 {
				metrics.LineMetrics[len(metrics.LineMetrics)-1] = LineMetrics{}
			}
			return state
		}
		if c == '\n' {
			metrics.LineMetrics = append(metrics.LineMetrics, *currentLine)
			currentLine.MaskedChars = []MaskedChar{}
			currentLine.Length = 0
			return state
		}
		maskedLength := 0
		isControl := false
		if c == 0x7f || c < 0x20 {
			isControl = true
			if mask != nil {
				currentLine.MaskedChars = append(currentLine.MaskedChars, MaskedChar{
					Position:       uint32(index),
					OriginalLength: 1,
					MaskedLength:   uint32(len(mask.replacementView)),
				})
			} else {
				maskedLength = 2
				if c > 64 {
					maskedLength = 4
				}
				currentLine.MaskedChars = append(currentLine.MaskedChars, MaskedChar{
					Position:       uint32(index),
					OriginalLength: 1,
					MaskedLength:   uint32(maskedLength),
				})
			}
		}
		if mask != nil {
			currentLine.Length += uint32(len(mask.replacementView))
			metrics.TotalLength += uint32(len(mask.replacementView))
		} else if isControl {
			currentLine.Length += uint32(maskedLength)
			metrics.TotalLength += uint32(maskedLength)
		} else {
			currentLine.Length++
			metrics.TotalLength++
		}
		return state
	case VTStateEscape:
		if c == ']' {
			if nextC == '0' {
				return VTStateTitle
			}
			return state
		}
		if c == '[' {
			return VTStateBracket
		}
		return state
	case VTStateBracket:
		if c >= '0' && c <= '9' {
			return VTStateBracketArgsSemi
		}
		return state
	case VTStateBracketArgsSemi:
		if c == ';' {
			return VTStateBracket
		}
		if c >= '0' && c <= '9' {
			return state
		}

		return VTStateFree
	case VTStateTitle:
		if c == 7 {
			return VTStateFree
		}
		return state
	default:
		return state
	}
}

func (l *lineEditor) byteOffsetRangeToCodePointOffsetRange(startByteOffset, endByteOffset, scanCodePointOffset uint32, reverse bool) (start, end uint32) {
	byteOffset := uint32(0)
	codePointOffset := scanCodePointOffset
	if reverse {
		codePointOffset++
	}

	for {
		if !reverse {
			if codePointOffset >= uint32(len(l.buffer)) {
				break
			}
		} else {
			if codePointOffset == 0 {
				break
			}
		}

		if byteOffset >= endByteOffset {
			break
		}

		if byteOffset < startByteOffset {
			start++
		}

		if byteOffset < endByteOffset {
			end++
		}

		v := codePointOffset
		if reverse {
			codePointOffset--
			v--
		} else {
			codePointOffset++
		}
		byteOffset += uint32(utf8.RuneLen(l.buffer[v]))
	}

	return
}

func vtApplyStyle(style Style, w io.Writer, isStarting bool) {
	if isStarting {
		b := 22
		if style.Bold {
			b = 1
		}
		u := 24
		if style.Underline {
			u = 4
		}
		i := 23
		if style.Italic {
			i = 3
		}
		_, _ = fmt.Fprintf(w, "\x1b[%d;%d;%dm%s%s%s",
			b, u, i,
			style.ForegroundColor.toVTString(true),
			style.BackgroundColor.toVTString(false),
			style.Hyperlink.toVTString(true))
	} else {
		_, _ = w.Write([]byte(style.Hyperlink.toVTString(false)))
	}
}

func (c *Color) toVTString(foreground bool) string {
	if !c.HasValue {
		return ""
	}

	if c.IsXterm && c.Xterm8 == XtermColorUnchanged {
		return ""
	}

	x := 40
	if foreground {
		x = 30
	}
	if c.IsXterm {
		return fmt.Sprintf("\x1b[%dm", int(c.Xterm8)+x)
	}

	return fmt.Sprintf("\x1b[%d;2;%d;%d;%dm", x+8, c.R, c.G, c.B)
}

func (h *Hyperlink) toVTString(starting bool) string {
	l := ""
	if starting {
		l = string(*h)
	}
	return fmt.Sprintf("\x1b]8;;%s\x1b\\", l)
}

func vtClearLines(countAbove, countBelow uint32, w io.Writer) {
	if countAbove+countBelow == 0 {
		_, _ = w.Write([]byte("\x1b[2K"))
	} else {
		// Go down countBelow lines...
		if countBelow > 0 {
			_, _ = w.Write([]byte(fmt.Sprintf("\x1b[%dB", countBelow)))
		}
		// ...and clear lines going up.
		for i := countAbove + countBelow; i > 0; i-- {
			_, _ = w.Write([]byte("\x1b[2K"))
			if i != 1 {
				_, _ = w.Write([]byte("\x1b[A"))
			}
		}
	}
}

func vtClearToEndOfLine(w io.Writer) {
	_, _ = w.Write([]byte("\x1b[K"))
}

func vtMoveAbsolute(row, col uint32, w io.Writer) {
	_, _ = fmt.Fprintf(w, "\x1b[%d;%dH", row, col)
}

func vtSaveCursor(w io.Writer) {
	_, _ = w.Write([]byte("\x1b[s"))
}

func vtRestoreCursor(w io.Writer) {
	_, _ = w.Write([]byte("\x1b[u"))
}

func (s *spans) containsUpToOffset(other *spans, offset uint32) bool {
	compare := func(left, right *map[uint32]map[uint32]Style) bool {
		for entryKey, entryValue := range *right {
			if entryKey > offset+1 {
				continue
			}

			leftMap, ok := (*left)[entryKey]
			if !ok {
				return false
			}

			for leftEntryKey, leftEntryValue := range leftMap {
				valueMap, ok := entryValue[leftEntryKey]
				if ok {
					if valueMap != leftEntryValue {
						return false
					}
				} else {
					// Might have the same thing with a longer span
					found := false
					for possiblyLongerSpanEntryKey, possiblyLongerSpanEntryValue := range entryValue {
						if possiblyLongerSpanEntryKey > leftEntryKey && possiblyLongerSpanEntryKey > offset && leftEntryValue == possiblyLongerSpanEntryValue {
							found = true
							break
						}
					}
					if found {
						continue
					}
					return false
				}
			}
		}
		return true
	}

	return compare(&s.spansStarting, &other.spansStarting)
}

func (l *lineEditor) tryUpdateOnce() {
	if l.wasInterrupted {
		l.handleInterruptEvent()
	}

	l.handleReadEvent()

	if l.alwaysRefresh {
		l.refreshNeeded = true
	}

	l.refreshDisplay()

	if l.finish {
		l.reallyQuitEventLoop()
	}
}

func (l *lineEditor) reallyQuitEventLoop() {
	l.repositionCursor(os.Stderr, true)
	os.Stderr.WriteString("\r\n")

	str := l.Line()
	l.buffer = []rune{}
	l.charsTouchedInTheMiddle = 0

	if l.initialized {
		l.restore()
	}

	l.returnedLine = str

	l.loopChan <- loopExitCodeExit

}

var csiParameterBytes []byte
var csiIntermediateBytes []byte

func (l *lineEditor) handleReadEvent() {
	if l.prohibitInputProcessing {
		l.haveUnprocessedReadEvent = true
		return
	}

	l.prohibitInputProcessing = true
	defer func() {
		l.prohibitInputProcessing = false
	}()

	keyBuf := make([]byte, 16)
	var nread int
	var err error

	if len(l.incompleteData) == 0 {
		nread, err = unix.Read(unix.Stdin, keyBuf)
		if err == nil && nread == 0 {
			return
		}
		// FIXME: Somehow this sneaks in here when the user presses Ctrl-C
		if nread == 1 && keyBuf[0] == byte(ctrl('C')) {
			l.handleInterruptEvent()
			return
		}
	}

	if err != nil {
		if err == syscall.EINTR {
			if !l.wasInterrupted {
				if l.wasResized {
					return
				}

				l.Finish()
				return
			}

			l.handleInterruptEvent()
			return
		}

		fmt.Fprintf(os.Stderr, "Error reading from stdin: %s\n", err)
		l.inputError = err
		l.Finish()
		return
	}

	l.incompleteData = append(l.incompleteData, keyBuf[:nread]...)
	availableBytes := len(l.incompleteData)

	if availableBytes == 0 {
		l.inputError = syscall.ECANCELED
		l.Finish()
		return
	}

	reverseTab := false

	validBytes := 0
	for availableBytes > 0 {
		validBytes = utf8.RuneCount(l.incompleteData)
		if validBytes != 0 {
			break
		}
		l.incompleteData = l.incompleteData[1:]
		availableBytes--
	}

	inputView := []rune(string(l.incompleteData))
	consumedCodePoints := 0

	csiParameters := make([]uint32, 0, 4)
	csiFinal := byte(0)

	for _, codePoint := range inputView {
		if func() iterationDecision {
			if l.finish {
				return iterationDecisionBreak
			}

			consumedCodePoints++

			if codePoint == 0 {
				return iterationDecisionContinue
			}

			switch l.state {
			case inputStateGotEscape:
				switch codePoint {
				case '[':
					l.state = inputStateCSIExpectParameter
					return iterationDecisionContinue
				default:
					l.keyCallbackMachine.keyPressed(key{
						modifiers: ModifierAlt,
						key:       uint32(codePoint),
					}, l)
					l.state = inputStateFree
					return iterationDecisionContinue
				}
			case inputStateCSIExpectParameter:
				if codePoint >= 0x30 && codePoint <= 0x3f { // '0123456789:;<=>?'
					csiParameterBytes = append(csiParameterBytes, byte(codePoint))
					return iterationDecisionContinue
				}
				l.state = inputStateCSIExpectIntermediate
				fallthrough
			case inputStateCSIExpectIntermediate:
				if codePoint >= 0x20 && codePoint <= 0x2f { // ' !"#$%&\'()*+,-./'
					csiIntermediateBytes = append(csiIntermediateBytes, byte(codePoint))
					return iterationDecisionContinue
				}
				l.state = inputStateCSIExpectFinal
				fallthrough
			case inputStateCSIExpectFinal:
				l.state = l.previousFreeState
				isInPaste := l.state == inputStatePaste
				for _, p := range strings.Split(string(csiParameterBytes), ";") {
					value, err := strconv.Atoi(p)
					if err != nil {
						value = 0
					}
					csiParameters = append(csiParameters, uint32(value))
				}
				var param1, param2 uint32
				if len(csiParameters) > 0 {
					param1 = csiParameters[0]
				}
				if len(csiParameters) > 1 {
					param2 = csiParameters[1]
				}
				modifiers := param2 - 1
				if param2 == 0 {
					modifiers = 0
				}

				if isInPaste && codePoint != '~' && param1 != 201 {
					// The only valid escape to process in paste mode is the stop-paste sequence.
					// so treat everything else as part of the pasted data.
					l.InsertChar('\x1b')
					l.InsertChar('[')
					l.InsertString(string(csiParameterBytes))
					l.InsertString(string(csiIntermediateBytes))
					l.InsertChar(codePoint)
					return iterationDecisionContinue
				}
				if !(codePoint >= 0x40 && codePoint <= 0x7f) {
					fmt.Fprintf(os.Stderr, "Invalid CSI: %02x (%c)\n", codePoint, codePoint)
					return iterationDecisionContinue
				}

				csiFinal = byte(codePoint)
				csiParameters = csiParameters[:0]
				csiParameterBytes = csiParameterBytes[:0]
				csiIntermediateBytes = csiIntermediateBytes[:0]

				if csiFinal == 'Z' {
					// "reverse tab"
					reverseTab = true
					break
				}

				l.cleanupSuggestions()

				switch csiFinal {
				case 'A': // ^[[A: Arrow up
					searchBackwards(l)
					return iterationDecisionContinue
				case 'B': // ^[[B: Arrow down
					searchForwards(l)
					return iterationDecisionContinue
				case 'D': // ^[[D: Arrow left
					if modifiers == ModifierAlt || modifiers == ModifierCtrl {
						cursorLeftWord(l)
					} else {
						cursorLeftCharacter(l)
					}
					return iterationDecisionContinue
				case 'C': // ^[[C: Arrow right
					if modifiers == ModifierAlt || modifiers == ModifierCtrl {
						cursorRightWord(l)
					} else {
						cursorRightCharacter(l)
					}
					return iterationDecisionContinue
				case 'H': // ^[[H: Home
					goHome(l)
					return iterationDecisionContinue
				case 'F': // ^[[F: End
					goEnd(l)
					return iterationDecisionContinue
				case '~':
					if param1 == 3 { // ^[[3~: Delete
						if modifiers == ModifierCtrl {
							eraseAlnumWordForwards(l)
						} else {
							eraseCharacterForwards(l)
						}
						l.searchOffset = 0
						return iterationDecisionContinue
					}
					if l.enableBracketedPaste {
						// ^[[200~: Start paste mode
						// ^[[201~: Stop paste mode
						if !isInPaste && param1 == 200 {
							l.state = inputStatePaste
							return iterationDecisionContinue
						}
						if isInPaste && param1 == 201 {
							l.state = inputStateFree
							if l.pasteHandler != nil {
								l.pasteHandler(string(l.pasteBuffer), l)
								l.pasteBuffer = l.pasteBuffer[:0]
							}
							if len(l.pasteBuffer) != 0 {
								l.InsertString(string(l.pasteBuffer))
							}
							return iterationDecisionContinue
						}
						fmt.Fprintf(os.Stderr, "Unknown '~': %d\n", param1)
						return iterationDecisionContinue
					}
				default:
					fmt.Fprintf(os.Stderr, "Unknown Final: %02x (%c)\n", csiFinal, csiFinal)
					return iterationDecisionContinue
				}

				panic("unreachable")
			case inputStateVerbatim:
				l.state = inputStateFree
				// Verbatim mode will bypass all mechanisms and just insert the character.
				l.InsertChar(codePoint)
				return iterationDecisionContinue
			case inputStatePaste:
				if codePoint == 27 {
					l.previousFreeState = inputStatePaste
					l.state = inputStateGotEscape
					return iterationDecisionContinue
				}
				if l.pasteHandler != nil {
					l.pasteBuffer = append(l.pasteBuffer, codePoint)
				} else {
					l.InsertChar(codePoint)
				}
				return iterationDecisionContinue
			case inputStateFree:
				l.previousFreeState = inputStateFree
				if codePoint == 27 {
					l.keyCallbackMachine.keyPressed(key{key: uint32(codePoint)}, l)
					if l.keyCallbackMachine.shouldProcessLastPressedKey() {
						l.state = inputStateGotEscape
					}
					return iterationDecisionContinue
				}
				if codePoint == 22 { // ^v
					l.keyCallbackMachine.keyPressed(key{key: uint32(codePoint)}, l)
					if l.keyCallbackMachine.shouldProcessLastPressedKey() {
						l.state = inputStateVerbatim
					}
					return iterationDecisionContinue
				}
			}

			// There are no sequences past this point, so short of 'tab', we will want to cleanup the suggestions
			shouldCleanupSuggestions := true
			defer func() {
				if shouldCleanupSuggestions {
					l.cleanupSuggestions()
				}
			}()

			// Normally ^d, `stty eof \^n` can change it to ^N (or whatever).
			// Process this here since keybinds might override its behaviour
			// This only applies when the buffer is empty, at any other time, the behaviour should be configurable.
			if codePoint == rune(l.termios.Cc[unix.VEOF]) && len(l.buffer) == 0 {
				finishEdit(l)
				return iterationDecisionContinue
			}

			l.keyCallbackMachine.keyPressed(key{key: uint32(codePoint)}, l)
			if !l.keyCallbackMachine.shouldProcessLastPressedKey() {
				return iterationDecisionContinue
			}

			l.searchOffset = 0 // reset search offset on any key

			if codePoint == '\t' || reverseTab {
				shouldCleanupSuggestions = false
				if l.tabCompletionHandler == nil {
					return iterationDecisionContinue
				}

				// Reverse tab can count as regular tab here.
				l.timesTabPressed++

				tokenStart := l.cursor

				if l.timesTabPressed == 1 {
					l.suggestionManager.setSuggestions(l.tabCompletionHandler(l))
					l.suggestionManager.setStartIndex(0)
					l.promptLinesAtSuggestionInitiation = l.NumLines()
					if l.suggestionManager.count() == 0 {
						// There are no suggestions, beep
						os.Stderr.Write([]byte{'\a'})
					}
				}

				// Adjust already incremented / decremented index when switching tab direction
				if reverseTab && l.tabDirection != tabDirectionBackward {
					l.suggestionManager.previous()
					l.suggestionManager.previous()
					l.tabDirection = tabDirectionBackward
				}
				if !reverseTab && l.tabDirection != tabDirectionForward {
					l.suggestionManager.next()
					l.suggestionManager.next()
					l.tabDirection = tabDirectionForward
				}
				reverseTab = false

				var mode completionMode
				switch l.timesTabPressed {
				case 1:
					mode = completionModeCompletePrefix
				case 2:
					mode = completionModeShowSuggestions
				default:
					mode = completionModeCycleSuggestions
				}

				l.InsertString(string(l.rememberedSuggestionStaticData))
				l.rememberedSuggestionStaticData = l.rememberedSuggestionStaticData[:0]

				completionResult := l.suggestionManager.attemptCompletion(mode, tokenStart)
				newCursor := l.cursor

				newCursor += completionResult.newCursorOffset
				for i := completionResult.offsetStartToRemove; i < completionResult.offsetEndToRemove; i++ {
					l.removeAtIndex(newCursor)
				}

				newCursor -= completionResult.staticOffsetFromCursor
				for i := uint32(0); i < completionResult.staticOffsetFromCursor; i++ {
					l.rememberedSuggestionStaticData = append(l.rememberedSuggestionStaticData, l.buffer[newCursor])
					l.removeAtIndex(newCursor)
				}

				l.cursor = newCursor
				l.inlineSearchCursor = l.cursor
				l.refreshNeeded = true
				l.charsTouchedInTheMiddle++

				l.InsertString(string(completionResult.insert))

				l.repositionCursor(os.Stderr, false)

				if completionResult.hasStyleToApply {
					// Apply the style of the last suggestion
					l.Stylize(Span{l.suggestionManager.currentSuggestion().StartIndex, l.cursor, SpanModeRune}, completionResult.styleToApply)
				}

				switch completionResult.newCompletionMode {
				case completionModeDontComplete:
					l.timesTabPressed = 0
					l.rememberedSuggestionStaticData = l.rememberedSuggestionStaticData[:0]
				case completionModeCompletePrefix:
					l.timesTabPressed++
					l.timesTabPressed--
				default:
					l.timesTabPressed++
				}

				if l.timesTabPressed > 1 && l.suggestionManager.count() > 0 {
					if l.suggestionDisplay.cleanup() {
						l.repositionCursor(os.Stderr, false)
					}
					l.suggestionDisplay.setInitialPromptLines(l.promptLinesAtSuggestionInitiation)
					l.suggestionDisplay.display(l.suggestionManager)
					l.originRow = l.suggestionDisplay.originRow()
				}

				if l.timesTabPressed > 2 {
					if l.tabDirection == tabDirectionForward {
						l.suggestionManager.next()
					} else {
						l.suggestionManager.previous()
					}
				}

				if l.suggestionManager.count() < 2 && !completionResult.avoidCommittingToSingleSuggestion {
					// We have none, or just one suggestion,
					// we should just commit that and continue
					// after it, as if it were auto-completed.
					l.repositionCursor(os.Stderr, true)
					l.cleanupSuggestions()
					l.rememberedSuggestionStaticData = l.rememberedSuggestionStaticData[:0]
				}
				return iterationDecisionContinue
			}

			// If we got here, manually cleanup the suggestions and then insert the new code point.
			l.rememberedSuggestionStaticData = l.rememberedSuggestionStaticData[:0]
			shouldCleanupSuggestions = false
			l.cleanupSuggestions()
			l.InsertChar(codePoint)

			return iterationDecisionContinue
		}() == iterationDecisionBreak {
			break
		}
	}

	if consumedCodePoints == len(l.incompleteData) {
		l.incompleteData = l.incompleteData[:0]
	} else {
		l.incompleteData = l.incompleteData[consumedCodePoints:]
	}

	if len(l.incompleteData) != 0 && !l.finish {
		l.laterChan <- laterEventCodeTryUpdateOnce
	}
}

func (l *lineEditor) cleanupSuggestions() {
	if l.timesTabPressed != 0 {
		// Apply the style of the last suggestion
		l.Stylize(Span{l.suggestionManager.currentSuggestion().StartIndex, l.cursor, SpanModeRune}, l.suggestionManager.currentSuggestion().Style)
		// We probably have some suggestions drawn,
		// let's clean them up.
		if l.suggestionDisplay.cleanup() {
			l.repositionCursor(os.Stderr, false)
			l.refreshNeeded = true
		}
		l.suggestionManager.reset()
		l.suggestionDisplay.finish()
	}
	l.timesTabPressed = 0
}

func (l *lineEditor) removeAtIndex(index uint32) {
	cp := l.buffer[index]
	l.buffer = append(l.buffer[:index], l.buffer[index+1:]...)
	if cp == '\n' {
		l.extraForwardLines++
	}
	l.charsTouchedInTheMiddle++
}

func (l *lineEditor) search(phrase string, allowEmpty bool, fromBeginning bool) bool {
	lastMatchingOffset := -1
	found := false

	// Do not search for empty strings.
	if allowEmpty || len(phrase) > 0 {
		searchOffset := l.searchOffset
		for i := l.historyCursor; i > 0; i-- {
			entry := &l.history[i-1]
			contains := false
			if fromBeginning {
				contains = strings.HasPrefix(entry.entry, phrase)
			} else {
				contains = strings.Contains(entry.entry, phrase)
			}

			if contains {
				lastMatchingOffset = int(i - 1)
				if searchOffset == 0 {
					found = true
					break
				}
				searchOffset--
			}
		}

		if !found {
			os.Stderr.Write([]byte("\a"))
		}
	}

	if found {
		// We plan to clear the buffer, so mark the entire thing as touched.
		l.charsTouchedInTheMiddle = uint32(len(l.buffer))
		l.buffer = l.buffer[:0]
		l.cursor = 0
		l.InsertString(l.history[lastMatchingOffset].entry)
		// Always needed, as we have cleared the buffer.
		l.refreshNeeded = true
	}

	return found
}

func (l *lineEditor) endSearch() {
	l.isSearching = false
	l.refreshNeeded = true
	l.searchOffset = 0
	if l.resetBufferOnSearchEnd {
		l.buffer = l.buffer[:0]
		l.buffer = append(l.buffer, l.preSearchBuffer...)
		l.cursor = l.preSearchCursor
	}
	l.resetBufferOnSearchEnd = true
	l.searchEditor = nil
}

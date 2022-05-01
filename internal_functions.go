package line

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"unicode"
)

func isAlphaNumeric(c rune) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
func isSpace(c rune) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

func finish(editor *lineEditor) {
	editor.Finish()
}

func finishEdit(editor *lineEditor) {
	fmt.Fprintf(os.Stdout, "<EOF>\n")
	if !editor.alwaysRefresh {
		editor.inputError = syscall.ECANCELED
		editor.Finish()
	}
}

func cursorLeftWord(editor *lineEditor) {
	if editor.cursor > 0 {
		skippedAtLeastOneCharacter := false
		for {
			if editor.cursor == 0 {
				break
			}
			if skippedAtLeastOneCharacter && isAlphaNumeric(editor.buffer[editor.cursor-1]) {
				break
			}
			skippedAtLeastOneCharacter = true
			editor.cursor--
		}
	}
	editor.inlineSearchCursor = editor.cursor
}
func cursorLeftCharacter(editor *lineEditor) {
	if editor.cursor > 0 {
		editor.cursor--
	}
	editor.inlineSearchCursor = editor.cursor
}
func cursorRightWord(editor *lineEditor) {
	if editor.cursor < uint32(len(editor.buffer)) {
		// Temporarily put a space at the end of the our buffer,
		// doing this greatly simplifies the logic below.
		editor.buffer = append(editor.buffer, ' ')
		for {
			if editor.cursor >= uint32(len(editor.buffer)) {
				break
			}
			editor.cursor++
			if !isAlphaNumeric(editor.buffer[editor.cursor]) {
				break
			}
		}
		editor.buffer = editor.buffer[:len(editor.buffer)-1]
	}
	editor.inlineSearchCursor = editor.cursor
	editor.searchOffset = 0
}
func cursorRightCharacter(editor *lineEditor) {
	if editor.cursor < uint32(len(editor.buffer)) {
		editor.cursor++
	}
	editor.inlineSearchCursor = editor.cursor
	editor.searchOffset = 0
}
func goHome(editor *lineEditor) {
	editor.cursor = 0
	editor.inlineSearchCursor = editor.cursor
	editor.searchOffset = 0
}
func goEnd(editor *lineEditor) {
	editor.cursor = uint32(len(editor.buffer))
	editor.inlineSearchCursor = editor.cursor
	editor.searchOffset = 0
}
func eraseCharacterBackwards(editor *lineEditor) {
	if editor.isSearching {
		return
	}
	if editor.cursor == 0 {
		os.Stderr.Write([]byte("\a"))
		return
	}
	editor.removeAtIndex(editor.cursor - 1)
	editor.cursor--
	editor.inlineSearchCursor = editor.cursor
	editor.refreshNeeded = true
}
func eraseCharacterForwards(editor *lineEditor) {
	if editor.cursor == uint32(len(editor.buffer)) {
		os.Stderr.Write([]byte("\a"))
		return
	}
	editor.removeAtIndex(editor.cursor)
	editor.refreshNeeded = true
}
func eraseAlnumWordBackwards(editor *lineEditor) {
	hasSeenAlnum := false
	for editor.cursor > 0 {
		if !isAlphaNumeric(editor.buffer[editor.cursor-1]) {
			if hasSeenAlnum {
				break
			}
		} else {
			hasSeenAlnum = true
		}
		eraseCharacterBackwards(editor)
	}
}
func eraseAlnumWordForwards(editor *lineEditor) {
	// A word here is contiguous alnums, `foo=bar baz` is three words.
	hasSeenAlnum := false
	for editor.cursor < uint32(len(editor.buffer)) {
		if !isAlphaNumeric(editor.buffer[editor.cursor]) {
			if hasSeenAlnum {
				break
			}
		} else {
			hasSeenAlnum = true
		}
		eraseCharacterForwards(editor)
	}
}
func eraseWordBackwards(editor *lineEditor) {
	hasSeenNonSpace := false
	for editor.cursor > 0 {
		if isSpace(editor.buffer[editor.cursor-1]) {
			if hasSeenNonSpace {
				break
			}
		} else {
			hasSeenNonSpace = true
		}
		eraseCharacterBackwards(editor)
	}
}
func clearScreen(editor *lineEditor) {
	os.Stderr.Write([]byte("\x1b[3J\x1b[H\x1b[2J"))
	vtMoveAbsolute(1, 1, os.Stderr)
	editor.setOriginValue(1, 1)
	editor.refreshNeeded = true
	editor.cachedPromptValid = false
}
func searchForwards(editor *lineEditor) {
	defer func(original uint32) {
		editor.inlineSearchCursor = original
	}(editor.inlineSearchCursor)

	searchPhrase := string(editor.buffer[:editor.inlineSearchCursor])
	if editor.searchOffsetState == searchOffsetStateBackwards {
		editor.searchOffset--
	}
	if editor.searchOffset > 0 {
		original := editor.searchOffset
		defer func() {
			editor.searchOffset = original
		}()
		editor.searchOffset--
		if editor.search(searchPhrase, true, true) {
			editor.searchOffsetState = searchOffsetStateForwards
			original = editor.searchOffset
		} else {
			editor.searchOffsetState = searchOffsetStateUnbiased
		}
	} else {
		editor.searchOffsetState = searchOffsetStateUnbiased
		editor.charsTouchedInTheMiddle = uint32(len(editor.buffer))
		editor.cursor = 0
		editor.buffer = editor.buffer[:0]
		editor.InsertString(searchPhrase)
		editor.refreshNeeded = true
	}
}
func searchBackwards(editor *lineEditor) {
	defer func(original uint32) {
		editor.inlineSearchCursor = original
	}(editor.inlineSearchCursor)

	searchPhrase := string(editor.buffer[:editor.inlineSearchCursor])
	if editor.searchOffsetState == searchOffsetStateForwards {
		editor.searchOffset++
	}
	if editor.search(searchPhrase, true, true) {
		editor.searchOffsetState = searchOffsetStateBackwards
		editor.searchOffset++
	} else {
		editor.searchOffsetState = searchOffsetStateUnbiased
		editor.searchOffset--
	}
}
func eraseToEnd(editor *lineEditor) {
	for editor.cursor < uint32(len(editor.buffer)) {
		eraseCharacterForwards(editor)
	}
}
func enterSearch(editor *lineEditor) {
	if editor.isSearching {
		panic("already searching")
	}

	editor.isSearching = true
	editor.searchOffset = 0
	editor.preSearchBuffer = append(editor.preSearchBuffer[:0], editor.buffer...)
	editor.preSearchCursor = editor.cursor

	editor.ensureFreeLinesFromOrigin(editor.NumLines() + 1)

	editor.searchEditor = NewEditor().(*lineEditor)
	editor.searchEditor.enableSignalHandling = false
	editor.searchEditor.alwaysRefresh = true
	editor.searchEditor.Initialize()

	editor.searchEditor.onRefresh = func(_ Editor) {
		// Remove the search editor prompt before updating ourselves (this avoids artifacts when we move the search editor around).
		editor.searchEditor.cleanup()

		searchPhrase := string(editor.searchEditor.buffer)
		if !editor.search(searchPhrase, false, false) {
			editor.charsTouchedInTheMiddle = uint32(len(editor.buffer))
			editor.refreshNeeded = true
			editor.buffer = editor.buffer[:0]
			editor.cursor = 0
		}

		editor.refreshDisplay()

		// Move the search prompt below ours and tell it to redraw itself.
		promptEndLine := editor.CurrentPromptMetrics().LinesWithAddition(&editor.cachedBufferMetrics, editor.numColumns)
		editor.searchEditor.setOriginValue(promptEndLine+editor.originRow, 1)
		editor.searchEditor.refreshNeeded = true
	}

	// Whenever the search editor gets a ^R, cycle between history entries.
	editor.searchEditor.RegisterKeybinding([]key{{key: ctrl('R')}}, func(_ []key, _ Editor) bool {
		editor.searchOffset++
		editor.searchEditor.refreshNeeded = true
		return false // Don't process this key event
	})

	// ^C should cancel the search.
	editor.searchEditor.RegisterKeybinding([]key{{key: ctrl('C')}}, func(_ []key, _ Editor) bool {
		editor.searchEditor.Finish()
		editor.resetBufferOnSearchEnd = true
		editor.searchEditor.endSearch()
		editor.searchEditor.loopChan <- loopExitCodeExit
		return false
	})

	// ^L - This is a source of issues, as the search editor refreshes first,
	// and we end up with the wrong order of prompts, so we will first refresh
	// ourselves, and then refresh the search editor, and tell it not to process
	// this event.
	editor.searchEditor.RegisterKeybinding([]key{{key: ctrl('L')}}, func(_ []key, _ Editor) bool {
		// Clear screen
		os.Stderr.Write([]byte("\x1b[3J\x1b[H\x1b[2J"))

		// Refresh our own prompt
		editor.alwaysRefresh = true
		editor.setOriginValue(1, 1)
		editor.refreshNeeded = true
		editor.refreshDisplay()
		editor.alwaysRefresh = false

		// Move the search prompt below ours and tell it to redraw itself.
		promptEndLine := editor.CurrentPromptMetrics().LinesWithAddition(&editor.cachedPromptMetrics, editor.numLines)
		editor.searchEditor.setOriginValue(promptEndLine+editor.originRow, 1)
		editor.searchEditor.refreshNeeded = true
		return false
	})

	// \t, Quit without clearing the curren buffer.
	editor.searchEditor.RegisterKeybinding([]key{{key: '\t'}}, func(_ []key, _ Editor) bool {
		editor.searchEditor.Finish()
		editor.resetBufferOnSearchEnd = false
		return false
	})

	// While the search editor is active, we do not want editing events.
	editor.isEditing = false

	// We still want to process signals, so spin up a goroutine here that handles them.
	stopChan := make(chan struct{})
	defer close(stopChan)
	go func() {
		for {
			select {
			case <-stopChan:
				return
			case sig := <-editor.signalChan:
				if sig == syscall.SIGWINCH {
					editor.resized()
				} else if sig == syscall.SIGINT {
					editor.interrupted()
				}
			}
		}
	}()

	searchPrompt := "\x1b[32msearch:\x1b[0m "
	searchStringResult, err := editor.searchEditor.GetLine(searchPrompt)

	// Stop the goroutine that handles signals since we'll be returning to our own loop.
	stopChan <- struct{}{}

	// Grab where the search origin last was, anything up to this point will be cleared.
	searchEndRow := editor.searchEditor.originRow

	editor.searchEditor = nil
	editor.isSearching = false
	editor.isEditing = true
	editor.searchOffset = 0

	if err != nil {
		// Something broke, fail.
		editor.inputError = err
		editor.Finish()
		return
	}

	// Manually cleanup the search line.
	editor.repositionCursor(os.Stderr, false)
	searchMetrics := editor.ActualRenderedStringMetrics(searchStringResult)
	promptMetrics := editor.ActualRenderedStringMetrics(searchPrompt)
	vtClearLines(0, promptMetrics.LinesWithAddition(&searchMetrics, editor.numLines)+searchEndRow-editor.originRow-1, os.Stderr)

	editor.repositionCursor(os.Stderr, false)
	editor.refreshNeeded = true
	editor.cachedPromptValid = false
	editor.charsTouchedInTheMiddle = 1

	if !editor.resetBufferOnSearchEnd || searchMetrics.TotalLength == 0 {
		// If the search entry was empty or we purposely quit without a newline,
		// do not return anything; instead, just end the search.
		editor.endSearch()
		return
	}

	// Otherwise, return the result
	editor.Finish()
}
func transposeCharacters(editor *lineEditor) {
	if editor.cursor > 0 && len(editor.buffer) >= 2 {
		if editor.cursor < uint32(len(editor.buffer)) {
			editor.cursor++
		}
		t := editor.buffer[editor.cursor-1]
		editor.buffer[editor.cursor-1] = editor.buffer[editor.cursor-2]
		editor.buffer[editor.cursor-2] = t
		editor.refreshNeeded = true
		editor.charsTouchedInTheMiddle += 2
	}
}
func editInExternalEditor(editor *lineEditor) {
	panic("TODO!")
}

type caseChangeOp int

const (
	caseChangeOpCapital caseChangeOp = iota
	caseChangeOpLower
	caseChangeOpUpper
)

func caseChangeWord(editor *lineEditor, op caseChangeOp) {
	// A word here is contiguous alnums.
	for editor.cursor < uint32(len(editor.buffer)) && !isAlphaNumeric(editor.buffer[editor.cursor]) {
		editor.cursor++
	}
	start := editor.cursor
	for editor.cursor < uint32(len(editor.buffer)) && isAlphaNumeric(editor.buffer[editor.cursor]) {
		if op == caseChangeOpUpper || (op == caseChangeOpCapital && editor.cursor == start) {
			editor.buffer[editor.cursor] = unicode.ToUpper(editor.buffer[editor.cursor])
		} else {
			editor.buffer[editor.cursor] = unicode.ToLower(editor.buffer[editor.cursor])
		}
		editor.cursor++
		editor.refreshNeeded = true
	}
}

func capitalizeWord(editor *lineEditor) {
	caseChangeWord(editor, caseChangeOpCapital)
}
func lowercaseWord(editor *lineEditor) {
	caseChangeWord(editor, caseChangeOpLower)
}
func uppercaseWord(editor *lineEditor) {
	caseChangeWord(editor, caseChangeOpUpper)
}
func killLine(editor *lineEditor) {
	for i := uint32(0); i < editor.cursor; i++ {
		editor.removeAtIndex(0)
	}
	editor.cursor = 0
	editor.inlineSearchCursor = 0
	editor.refreshNeeded = true
}
func transposeWords(editor *lineEditor) {
	panic("TODO!")
}
func insertLastWords(editor *lineEditor) {
	if len(editor.history) == 0 {
		return
	}

	// FIXME: This isn't quite right, if the last arg was `"foo bar"` or `foo\ bar` (but not `foo\\ bar`), we should insert that whole arg as last token.
	lastWords := strings.Split(editor.history[len(editor.history)-1].entry, " ")
	if len(lastWords) != 0 {
		editor.InsertString(lastWords[len(lastWords)-1])
	}
}

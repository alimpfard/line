package line

import (
	"fmt"
	"os"
	"syscall"
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
		editor.reallyQuitEventLoop()
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

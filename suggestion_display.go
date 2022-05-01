package line

import (
	"fmt"
	"os"
	"sort"
)

func newSuggestionDisplay() suggestionDisplay {
	return &suggestionDisplayImpl{}
}

type pageRange struct {
	start uint32
	end   uint32
}

type suggestionDisplayImpl struct {
	originRowValue       uint32
	originColumnValue    uint32
	isShowingSuggestions bool

	linesUsedForLastSuggestion        uint32
	numLines                          uint32
	numColumns                        uint32
	promptLinesAtSuggestionInitiation uint32
	pages                             []pageRange
}

func (s *suggestionDisplayImpl) display(manager suggestionManager) {
	s.isShowingSuggestions = true

	longestSuggestionLength := uint32(0)
	longestSuggestionByteLength := uint32(0)
	longestSuggestionByteLengthWithoutTrivia := uint32(0)

	manager.setStartIndex(0)
	manager.forEachSuggestion(func(completion *Completion, _ uint32) iterationDecision {
		longestSuggestionLength = max(longestSuggestionLength, uint32(len(completion.textView)+len(completion.displayTriviaView)))
		longestSuggestionByteLength = max(longestSuggestionByteLength, uint32(len(completion.Text)+len(completion.DisplayTrivia)))
		longestSuggestionByteLengthWithoutTrivia = max(longestSuggestionByteLengthWithoutTrivia, uint32(len(completion.Text)))
		return iterationDecisionContinue
	})

	numPrinted := uint32(0)
	linesUsed := uint32(1)

	vtSaveCursor(os.Stderr)
	vtClearLines(0, s.linesUsedForLastSuggestion, os.Stderr)
	vtRestoreCursor(os.Stderr)

	spansEntireLine := false
	var lines []LineMetrics
	for i := uint32(0); i < s.promptLinesAtSuggestionInitiation-1; i++ {
		lines = append(lines, LineMetrics{})
	}
	lines = append(lines, LineMetrics{Length: longestSuggestionLength})
	metrics := StringMetrics{LineMetrics: lines}
	maxLineCount := metrics.LinesWithAddition(&StringMetrics{LineMetrics: []LineMetrics{{Length: 0}}}, s.numColumns)

	if longestSuggestionLength >= s.numColumns-2 {
		spansEntireLine = true
		// We should make enough space for the biggest entry in
		// the suggestion list to fit in the prompt line.
		start := maxLineCount - s.promptLinesAtSuggestionInitiation
		for i := start; i < maxLineCount; i++ {
			os.Stderr.WriteString("\n")
		}
		linesUsed += maxLineCount
		longestSuggestionLength = 0
	}

	vtMoveAbsolute(maxLineCount+s.originRowValue, 1, os.Stderr)

	if len(s.pages) == 0 {
		numPrinted := uint32(0)
		linesUsed := uint32(1)
		// cache the pages.
		manager.setStartIndex(0)
		pageStart := uint32(0)
		manager.forEachSuggestion(func(suggestion *Completion, index uint32) iterationDecision {
			nextColumn := numPrinted + uint32(len(suggestion.textView)) + longestSuggestionLength + 2
			if nextColumn > s.numColumns {
				lines := (uint32(len(suggestion.textView)) + s.numLines - 1) / s.numLines
				linesUsed += lines
				numPrinted = 0
			}

			if linesUsed+s.promptLinesAtSuggestionInitiation >= s.numLines {
				s.pages = append(s.pages, pageRange{pageStart, index})
				pageStart = index
				linesUsed = 1
				numPrinted = 0
			}

			if spansEntireLine {
				numPrinted += s.numColumns
			} else {
				numPrinted += longestSuggestionLength + 2
			}
			return iterationDecisionContinue
		})
		// Append the last page
		s.pages = append(s.pages, pageRange{pageStart, manager.count()})
	}

	pageIndex := s.fitToPageBoundary(manager.nextIndex())

	manager.setStartIndex(s.pages[pageIndex].start)
	manager.forEachSuggestion(func(suggestion *Completion, index uint32) iterationDecision {
		nextColumn := numPrinted + uint32(len(suggestion.textView)) + longestSuggestionLength + 2

		if nextColumn > s.numColumns {
			lines := (uint32(len(suggestion.textView)) + s.numLines - 1) / s.numLines
			linesUsed += lines
			os.Stderr.WriteString("\n")
			numPrinted = 0
		}

		// Show just enough suggestions to fill up the screen
		// without moving the prompt out of view
		if linesUsed+s.promptLinesAtSuggestionInitiation >= s.numLines {
			return iterationDecisionBreak
		}

		// Only apply color to selection if something is actually added to the buffer
		if manager.isCurrentSuggestionComplete() && index == manager.nextIndex() {
			vtApplyStyle(Style{ForegroundColor: MakeXtermColor(XtermColorBlue)}, os.Stderr, true)
		}

		if spansEntireLine {
			numPrinted += s.numColumns
			_, _ = os.Stderr.WriteString(suggestion.Text)
			_, _ = os.Stderr.WriteString(suggestion.DisplayTrivia)
		} else {
			field := fmt.Sprintf("%-*s  %s", longestSuggestionByteLengthWithoutTrivia, suggestion.Text, suggestion.DisplayTrivia)
			display := fmt.Sprintf("%-*s", longestSuggestionByteLength+2, field)
			_, _ = os.Stderr.WriteString(display)
			numPrinted += longestSuggestionByteLength + 2
		}

		if manager.isCurrentSuggestionComplete() && index == manager.nextIndex() {
			vtApplyStyle(StyleReset, os.Stderr, true)
		}

		return iterationDecisionContinue
	})

	s.linesUsedForLastSuggestion = linesUsed

	// The last line of a prompt is the same line as the first line of the buffer, so we need to subtract one here
	linesUsed += s.promptLinesAtSuggestionInitiation - 1

	if s.originRowValue+linesUsed >= s.numLines {
		s.originRowValue = s.numLines - linesUsed
	}

	if len(s.pages) > 1 {
		leftArrow := '<'
		if pageIndex == 0 {
			leftArrow = ' '
		}
		rightArrow := '>'
		if pageIndex == uint32(len(s.pages)-1) {
			rightArrow = ' '
		}

		str := fmt.Sprintf("%c page %d of %d %c", leftArrow, pageIndex+1, len(s.pages), rightArrow)

		if uint32(len(str)) > s.numColumns-1 {
			// This would overflow into the next line, so just don't print an indicator
			return
		}

		vtMoveAbsolute(s.originRowValue+linesUsed, s.numColumns-uint32(len(str))-1, os.Stderr)
		vtApplyStyle(Style{BackgroundColor: Color{Xterm8: XtermColorGreen, IsXterm: true, HasValue: true}}, os.Stderr, true)
		_, _ = os.Stderr.WriteString(str)
		vtApplyStyle(StyleReset, os.Stderr, true)
	}
}

func (s *suggestionDisplayImpl) redisplay(manager suggestionManager, lines uint32, columns uint32) {
	if s.isShowingSuggestions {
		s.cleanup()
		s.setVTSize(lines, columns)
		s.display(manager)
	} else {
		s.setVTSize(lines, columns)
	}
}

func (s *suggestionDisplayImpl) cleanup() bool {
	s.isShowingSuggestions = false
	if s.linesUsedForLastSuggestion != 0 {
		vtClearLines(0, s.linesUsedForLastSuggestion, os.Stderr)
		s.linesUsedForLastSuggestion = 0
		return true
	}

	return false
}

func (s *suggestionDisplayImpl) finish() {
	s.pages = nil
}

func (s *suggestionDisplayImpl) setInitialPromptLines(u uint32) {
	s.promptLinesAtSuggestionInitiation = u
}

func (s *suggestionDisplayImpl) setVTSize(lines uint32, columns uint32) {
	s.numLines = lines
	s.numColumns = columns
	s.pages = nil
}

func (s *suggestionDisplayImpl) setOrigin(row uint32, column uint32) {
	s.originRowValue = row
	s.originColumnValue = column
}

func (s *suggestionDisplayImpl) originRow() uint32 {
	return s.originRowValue
}

func (s *suggestionDisplayImpl) fitToPageBoundary(selectionIndex uint32) uint32 {
	index := sort.Search(len(s.pages), func(i int) bool {
		return s.pages[i].start >= selectionIndex
	})

	if index == len(s.pages) {
		return uint32(len(s.pages) - 1)
	}
	return uint32(index)
}

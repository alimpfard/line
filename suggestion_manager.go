package line

func newSuggestionManager() suggestionManager {
	return &suggestionManagerImpl{}
}

type suggestionManagerImpl struct {
	suggestions                         []Completion
	lastShownSuggestion                 Completion
	lastShownSuggestionDisplayLength    uint32
	lastShownSuggestionWasComplete      bool
	nextSuggestionIndex                 uint32
	largestCommonSuggestionPrefixLength uint32
	lastDisplayedSuggestionIndex        uint32
	lastSelectedSuggestionIndex         uint32
}

func (s *suggestionManagerImpl) setSuggestions(suggestions []Completion) {
	s.suggestions = suggestions

	for i := range s.suggestions {
		suggestion := &s.suggestions[i]
		suggestion.textView = []rune(suggestion.Text)
		suggestion.trailingTriviaView = []rune(suggestion.TrailingTrivia)
		suggestion.displayTriviaView = []rune(suggestion.DisplayTrivia)
	}

	commonSuggestionPrefix := uint32(0)
	if len(s.suggestions) == 1 {
		s.largestCommonSuggestionPrefixLength = uint32(len(s.suggestions[0].Text))
	} else if len(s.suggestions) > 1 {
		lastValidSuggestionCodePoint := rune(0)
		for ; ; commonSuggestionPrefix++ {
			if uint32(len(s.suggestions[0].textView)) <= commonSuggestionPrefix {
				goto noMoreCommons
			}

			lastValidSuggestionCodePoint = s.suggestions[0].textView[commonSuggestionPrefix]
			for _, suggestion := range s.suggestions {
				if uint32(len(suggestion.textView)) <= commonSuggestionPrefix || suggestion.textView[commonSuggestionPrefix] != lastValidSuggestionCodePoint {
					goto noMoreCommons
				}
			}
		}
	noMoreCommons:
		s.largestCommonSuggestionPrefixLength = commonSuggestionPrefix
	} else {
		s.largestCommonSuggestionPrefixLength = 0
	}
}

func (s *suggestionManagerImpl) setCurrentSuggestionInitiationIndex(index uint32) {
	suggestion := &s.suggestions[s.nextSuggestionIndex]
	if s.lastShownSuggestionDisplayLength > 0 {
		s.lastShownSuggestion.StartIndex = index - suggestion.StaticOffset - s.lastShownSuggestionDisplayLength
	} else {
		s.lastShownSuggestion.StartIndex = index - suggestion.StaticOffset - suggestion.InvariantOffset
	}

	s.lastShownSuggestionDisplayLength = uint32(len(s.lastShownSuggestion.textView))
	s.lastShownSuggestionWasComplete = false
}

func (s *suggestionManagerImpl) count() uint32 {
	return uint32(len(s.suggestions))
}

func (s *suggestionManagerImpl) displayLength() uint32 {
	return s.lastShownSuggestionDisplayLength
}

func (s *suggestionManagerImpl) startIndex() uint32 {
	return s.lastDisplayedSuggestionIndex
}

func (s *suggestionManagerImpl) nextIndex() uint32 {
	return s.nextSuggestionIndex
}

func (s *suggestionManagerImpl) setStartIndex(u uint32) {
	s.lastDisplayedSuggestionIndex = u
}

func (s *suggestionManagerImpl) forEachSuggestion(f func(*Completion, uint32) iterationDecision) uint32 {
	startIndex := uint32(0)
	for _, suggestion := range s.suggestions {
		i := startIndex
		startIndex++
		if i < s.lastDisplayedSuggestionIndex {
			continue
		}
		if f(&suggestion, i) == iterationDecisionBreak {
			break
		}
	}
	return startIndex
}

func (s *suggestionManagerImpl) attemptCompletion(mode completionMode, initiationStartIndex uint32) completionAttemptResult {
	result := completionAttemptResult{
		newCompletionMode: mode,
	}

	if s.nextSuggestionIndex < uint32(len(s.suggestions)) {
		nextSuggestion := &s.suggestions[s.nextSuggestionIndex]
		if mode == completionModeCompletePrefix && !nextSuggestion.AllowCommitWithoutListing {
			result.newCompletionMode = completionModeShowSuggestions
			result.avoidCommittingToSingleSuggestion = true
			s.lastShownSuggestionDisplayLength = 0
			s.lastShownSuggestionWasComplete = false
			s.lastShownSuggestion = Completion{}
			return result
		}

		canComplete := nextSuggestion.InvariantOffset <= s.largestCommonSuggestionPrefixLength
		var actualOffset int64
		shownLength := int64(s.lastShownSuggestionDisplayLength)
		switch mode {
		case completionModeCompletePrefix:
			actualOffset = 0
		case completionModeShowSuggestions:
			actualOffset = int64(0) - int64(s.largestCommonSuggestionPrefixLength) + int64(nextSuggestion.InvariantOffset)
			if canComplete && nextSuggestion.AllowCommitWithoutListing {
				shownLength = int64(s.largestCommonSuggestionPrefixLength + uint32(len(s.lastShownSuggestion.trailingTriviaView)))
			}
		default:
			if s.lastShownSuggestionDisplayLength == 0 {
				actualOffset = 0
			} else {
				actualOffset = int64(0) - int64(s.lastShownSuggestionDisplayLength) + int64(nextSuggestion.InvariantOffset)
			}
		}

		suggestion := s.suggest()
		s.setCurrentSuggestionInitiationIndex(initiationStartIndex)

		result.offsetStartToRemove = nextSuggestion.InvariantOffset
		result.offsetEndToRemove = uint32(shownLength)
		result.newCursorOffset = uint32(actualOffset)
		result.staticOffsetFromCursor = nextSuggestion.StaticOffset

		if mode == completionModeCompletePrefix {
			// Only autocomplete *if possible*.
			if canComplete {
				result.insert = append(result.insert, suggestion.textView[suggestion.InvariantOffset:s.largestCommonSuggestionPrefixLength]...)
				s.lastShownSuggestionDisplayLength = s.largestCommonSuggestionPrefixLength
				// Do not increment the suggestion index, as the first tab should only be a peek.
				if len(s.suggestions) == 1 {
					// if there's one suggestion, commit and forget.
					result.newCompletionMode = completionModeDontComplete
					// Add in the trivia of the last selected suggestion.
					result.insert = append(result.insert, s.lastShownSuggestion.trailingTriviaView...)
					s.lastShownSuggestionDisplayLength = 0
					result.styleToApply = suggestion.Style
					result.hasStyleToApply = !suggestion.Style.IsEmpty()
					s.lastShownSuggestionWasComplete = true
					return result
				}
			} else {
				s.lastShownSuggestionDisplayLength = 0
			}
			result.newCompletionMode = completionModeShowSuggestions
			s.lastShownSuggestionWasComplete = false
			s.lastShownSuggestion = Completion{}
		} else {
			result.insert = append(result.insert, suggestion.textView[suggestion.InvariantOffset:len(suggestion.textView)]...)
			// Add in the trivia of the last selected suggestion
			result.insert = append(result.insert, s.lastShownSuggestion.trailingTriviaView...)
			s.lastShownSuggestionDisplayLength += uint32(len(suggestion.trailingTriviaView))
		}
	} else {
		s.nextSuggestionIndex = 0
	}
	return result
}

func (s *suggestionManagerImpl) next() {
	if len(s.suggestions) > 0 {
		s.nextSuggestionIndex = (s.nextSuggestionIndex + 1) % uint32(len(s.suggestions))
	} else {
		s.nextSuggestionIndex = 0
	}
}

func (s *suggestionManagerImpl) previous() {
	if s.nextSuggestionIndex == 0 {
		s.nextSuggestionIndex = uint32(len(s.suggestions))
	}
	s.nextSuggestionIndex--
}

func (s *suggestionManagerImpl) suggest() *Completion {
	s.lastShownSuggestion = s.suggestions[s.nextSuggestionIndex]
	s.lastSelectedSuggestionIndex = s.nextSuggestionIndex
	return &s.lastShownSuggestion
}

func (s *suggestionManagerImpl) currentSuggestion() *Completion {
	return &s.lastShownSuggestion
}

func (s *suggestionManagerImpl) isCurrentSuggestionComplete() bool {
	return s.lastShownSuggestionWasComplete
}

func (s *suggestionManagerImpl) reset() {
	s.lastShownSuggestion = Completion{}
	s.lastShownSuggestionDisplayLength = 0
	s.suggestions = []Completion{}
	s.lastDisplayedSuggestionIndex = 0
	s.nextSuggestionIndex = 0
}

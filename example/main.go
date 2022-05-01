package main

import (
	"fmt"
	"github.com/alimpfard/line"
	"strings"
)

func main() {
	editor := line.NewEditor()
	editor.SetRefreshHandler(func(_ line.Editor) {
		l := editor.Line()
		editor.StripStyles()
		count := 0
		for i, ch := range []rune(l) {
			if ch == 'x' {
				count++
				editor.Stylize(line.Span{
					Start: uint32(i),
					End:   uint32(i + 1),
					Mode:  line.SpanModeRune,
				}, line.Style{
					ForegroundColor: line.MakeXtermColor(line.XtermColorBlue),
				})
			}
		}
		editor.SetPrompt(fmt.Sprintf("I highlight x's (%d so far): ", count))
	})
	editor.SetTabCompletionHandler(func(_ line.Editor) []line.Completion {
		l := editor.Line()
		parts := strings.Split(l, " ")
		if strings.HasPrefix("exit", parts[len(parts)-1]) {
			return []line.Completion{
				{
					Text:                      "exit",
					InvariantOffset:           uint32(len(parts[len(parts)-1])),
					AllowCommitWithoutListing: true,
				},
			}
		}
		return []line.Completion{
			{
				Text:         "lol no actual completions",
				StaticOffset: uint32(len(parts[len(parts)-1])),
			},
			{
				Text:         "no really, no actual completions",
				StaticOffset: uint32(len(parts[len(parts)-1])),
			},
		}
	})

	for {
		line, err := editor.GetLine("I highlight x's (0 so far): ")
		if err != nil {
			println("Error:", err.Error())
			break
		}

		if line == "exit" {
			break
		}
	}
}
